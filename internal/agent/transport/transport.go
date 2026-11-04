// Package transport fala HTTP com o invenire-server: o cadastro, com a CA
// conferida por pin, e o envio de snapshots com mTLS e gzip.
package transport

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/athosbes/invenire/internal/wire"
)

// HTTPError é uma resposta de erro do servidor.
type HTTPError struct {
	Status int
	Msg    string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("servidor respondeu %d: %s", e.Status, e.Msg)
}

// Permanent diz se reenviar o mesmo corpo não adianta: o servidor recusou o
// conteúdo. 401 (credencial), 408 e 429 (momentâneos) não entram aqui.
func (e *HTTPError) Permanent() bool {
	switch e.Status {
	case http.StatusUnauthorized, http.StatusRequestTimeout, http.StatusTooManyRequests:
		return false
	}
	return e.Status >= 400 && e.Status < 500
}

// ParsePin lê um pin no formato "sha256:<hex>" (ou só o hex).
func ParsePin(s string) ([]byte, error) {
	b, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(s), "sha256:"))
	if err != nil || len(b) != sha256.Size {
		return nil, errors.New("pin da CA inválido: esperava sha256:<64 dígitos hex>")
	}
	return b, nil
}

// PinMatches diz se o certificado DER é o que o pin descreve.
func PinMatches(der, pin []byte) bool {
	h := sha256.Sum256(der)
	return bytes.Equal(h[:], pin)
}

// PinnedTLS confia no servidor só se a cadeia que ele apresenta trouxer uma
// CA com o SHA-256 do pin e o certificado do servidor for emitido por ela.
// É o que permite o primeiro contato, antes de o agente ter a CA.
func PinnedTLS(pin []byte) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		// A verificação padrão é trocada pela de VerifyConnection, que é mais
		// estrita: só aceita a CA do pin, não as do sistema.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("servidor sem certificado")
			}
			roots := x509.NewCertPool()
			found := false
			for _, c := range cs.PeerCertificates[1:] {
				if c.IsCA && PinMatches(c.Raw, pin) {
					roots.AddCert(c)
					found = true
				}
			}
			if !found {
				return errors.New("a CA do servidor não confere com o pin")
			}
			_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{
				Roots:     roots,
				DNSName:   cs.ServerName,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			})
			return err
		},
	}
}

// Client é o cliente HTTP do agente.
type Client struct {
	base string
	hc   *http.Client
}

// NewClient valida a URL do servidor e monta o cliente com o TLS dado.
func NewClient(serverURL string, cfg *tls.Config) (*Client, error) {
	u, err := url.Parse(serverURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("URL do servidor inválida (precisa ser https://host[:porta]): %q", serverURL)
	}
	return &Client{
		base: strings.TrimRight(u.String(), "/"),
		hc: &http.Client{
			Timeout:   5 * time.Minute,
			Transport: &http.Transport{TLSClientConfig: cfg, ForceAttemptHTTP2: true},
		},
	}, nil
}

// Enroll faz o cadastro.
func (c *Client) Enroll(ctx context.Context, req wire.EnrollRequest) (wire.EnrollResponse, error) {
	var resp wire.EnrollResponse
	body, err := json.Marshal(req)
	if err != nil {
		return resp, err
	}
	err = c.do(ctx, wire.PathEnroll, body, map[string]string{"Content-Type": "application/json"}, &resp)
	return resp, err
}

// SendSnapshot envia o snapshot em gzip, com o SHA-256 dos bytes originais.
func (c *Client) SendSnapshot(ctx context.Context, body []byte, sum string) (wire.SnapshotResponse, error) {
	var resp wire.SnapshotResponse
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write(body)
	if err := w.Close(); err != nil {
		return resp, err
	}
	err := c.do(ctx, wire.PathSnapshots, gz.Bytes(), map[string]string{
		"Content-Type":     "application/json",
		"Content-Encoding": "gzip",
		wire.SHA256Header:  sum,
	}, &resp)
	if err == nil && resp.SHA256 != sum {
		err = fmt.Errorf("servidor confirmou outro SHA-256 (%s)", resp.SHA256)
	}
	return resp, err
}

func (c *Client) do(ctx context.Context, path string, body []byte, hdr map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e wire.Error
		if json.Unmarshal(b, &e) != nil || e.Error == "" {
			e.Error = strings.TrimSpace(string(b))
		}
		return &HTTPError{Status: resp.StatusCode, Msg: e.Error}
	}
	return json.Unmarshal(b, out)
}
