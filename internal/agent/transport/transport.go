// Package transport fala HTTP com o invenire-server: o cadastro, com a CA
// conferida por pin, e o envio de snapshots com gzip.
//
// Toda requisição autenticada vai com o crachá duas vezes: no TLS (mTLS, o
// que o servidor confere quando fala TLS direto com o agente) e assinada com
// a chave do crachá (cabeçalhos Invenire-*, o que o servidor confere atrás de
// um proxy que termina o TLS). O agente não precisa saber qual é o caso.
package transport

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
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
//
// host é o nome ou IP da URL do servidor. Vem explícito porque, com IP, o
// Go não preenche ConnectionState.ServerName (IP não vai no SNI), e a
// conferência do nome seria pulada.
func PinnedTLS(pin []byte, host string) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		// A verificação padrão é trocada pela de VerifyConnection, que é mais
		// estrita: só aceita a CA do pin, não as do sistema.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("servidor sem certificado")
			}
			if host == "" {
				return errors.New("nome do servidor vazio")
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
				DNSName:   host,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			})
			var he x509.HostnameError
			if errors.As(err, &he) {
				return fmt.Errorf("%w; suba o servidor com %s em --hosts ou use na URL um dos nomes acima", err, host)
			}
			return err
		},
	}
}

// SystemTLS confia nas CAs do sistema (ou em roots, nos testes): para
// servidores com certificado público.
func SystemTLS(roots *x509.CertPool) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}
}

// Client é o cliente HTTP do agente.
type Client struct {
	base  string
	hc    *http.Client
	onTLS func(*tls.ConnectionState)
	// badge é o crachá com que as requisições autenticadas são assinadas:
	// o mesmo do mTLS (cfg.Certificates[0]). Nulo antes do cadastro.
	badge *tls.Certificate

	mu sync.Mutex
	// skew é quanto o relógio do servidor está à frente do nosso, pelo
	// cabeçalho Date da última resposta. Corrige o timestamp da assinatura
	// numa máquina de relógio errado.
	skew time.Duration
	// Now substitui time.Now nos testes.
	Now func() time.Time
}

// NewClient valida a URL do servidor e monta o cliente com o TLS dado. Se
// cfg trouxer o crachá (Certificates), as requisições autenticadas também
// são assinadas com ele.
func NewClient(serverURL string, cfg *tls.Config) (*Client, error) {
	u, err := url.Parse(serverURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("URL do servidor inválida (precisa ser https://host[:porta]): %q", serverURL)
	}
	c := &Client{
		base: strings.TrimRight(u.String(), "/"),
		hc: &http.Client{
			Timeout:   5 * time.Minute,
			Transport: &http.Transport{TLSClientConfig: cfg, ForceAttemptHTTP2: true},
		},
		Now: time.Now,
	}
	if cfg != nil && len(cfg.Certificates) > 0 && len(cfg.Certificates[0].Certificate) > 0 {
		c.badge = &cfg.Certificates[0]
	}
	return c, nil
}

// SignRequest põe em req os cabeçalhos da assinatura com o crachá badge.
// path é o caminho da API (wire.Path*), sem o prefixo que a URL do servidor
// possa ter; body, os bytes exatos do corpo enviado.
func SignRequest(req *http.Request, badge *tls.Certificate, path string, body []byte, now time.Time) error {
	signer, ok := badge.PrivateKey.(crypto.Signer)
	if !ok || len(badge.Certificate) == 0 {
		return errors.New("crachá sem chave para assinar")
	}
	ts := now.UTC().Format(time.RFC3339)
	msg := wire.SignedMessage(req.Method, path, ts, body)
	var sig []byte
	var err error
	switch signer.Public().(type) {
	case ed25519.PublicKey:
		sig, err = signer.Sign(rand.Reader, msg, crypto.Hash(0))
	case *ecdsa.PublicKey, *rsa.PublicKey:
		h := sha256.Sum256(msg)
		sig, err = signer.Sign(rand.Reader, h[:], crypto.SHA256)
	default:
		return fmt.Errorf("tipo de chave do crachá não suportado: %T", signer.Public())
	}
	if err != nil {
		return fmt.Errorf("assinar a requisição: %w", err)
	}
	req.Header.Set(wire.AgentCertHeader, base64.StdEncoding.EncodeToString(badge.Certificate[0]))
	req.Header.Set(wire.TimestampHeader, ts)
	req.Header.Set(wire.SignatureHeader, base64.StdEncoding.EncodeToString(sig))
	return nil
}

// Enroll faz o cadastro.
func (c *Client) Enroll(ctx context.Context, req wire.EnrollRequest) (wire.EnrollResponse, error) {
	var resp wire.EnrollResponse
	body, err := json.Marshal(req)
	if err != nil {
		return resp, err
	}
	err = c.do(ctx, http.MethodPost, wire.PathEnroll, body, map[string]string{"Content-Type": "application/json"}, &resp)
	return resp, err
}

// Agent pergunta ao servidor quem é o dono do crachá apresentado. Devolve
// também a cadeia do servidor, para quem precisa guardar a CA dele.
func (c *Client) Agent(ctx context.Context) (wire.AgentInfo, []*x509.Certificate, error) {
	var info wire.AgentInfo
	var chain []*x509.Certificate
	c.onTLS = func(cs *tls.ConnectionState) { chain = cs.PeerCertificates }
	defer func() { c.onTLS = nil }()
	err := c.do(ctx, http.MethodGet, wire.PathAgent, nil, nil, &info)
	return info, chain, err
}

// Renew troca o crachá apresentado por um novo, para a chave do CSR.
func (c *Client) Renew(ctx context.Context, csrPEM string) (wire.RenewResponse, error) {
	var resp wire.RenewResponse
	body, err := json.Marshal(wire.RenewRequest{CSR: csrPEM})
	if err != nil {
		return resp, err
	}
	err = c.do(ctx, http.MethodPost, wire.PathAgentRenew, body, map[string]string{"Content-Type": "application/json"}, &resp)
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
	err := c.do(ctx, http.MethodPost, wire.PathSnapshots, gz.Bytes(), map[string]string{
		"Content-Type":     "application/json",
		"Content-Encoding": "gzip",
		wire.SHA256Header:  sum,
	}, &resp)
	if err == nil && resp.SHA256 != sum {
		err = fmt.Errorf("servidor confirmou outro SHA-256 (%s)", resp.SHA256)
	}
	return resp, err
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, hdr map[string]string, out any) error {
	sign := c.badge != nil && path != wire.PathEnroll
	skew := c.clockSkew()
	err := c.try(ctx, method, path, body, hdr, sign, out)
	// Um 401 de requisição assinada pode ser só o relógio desta máquina fora
	// da janela do servidor. Se a resposta mostrou que o relógio está mesmo
	// longe do servidor, tenta uma vez com o timestamp corrigido. Sem isso, o
	// agente tomaria o 401 por revogação e pararia.
	var he *HTTPError
	if sign && errors.As(err, &he) && he.Status == http.StatusUnauthorized {
		if d := c.clockSkew() - skew; d > time.Minute || d < -time.Minute {
			err = c.try(ctx, method, path, body, hdr, sign, out)
		}
	}
	return err
}

func (c *Client) clockSkew() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.skew
}

func (c *Client) try(ctx context.Context, method, path string, body []byte, hdr map[string]string, sign bool, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if sign {
		if err := SignRequest(req, c.badge, path, body, c.Now().Add(c.clockSkew())); err != nil {
			return err
		}
	}
	sent := c.Now()
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if d, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
		// Date tem resolução de um segundo; o ponto médio entre envio e
		// resposta aproxima a hora a que ele se refere.
		mid := sent.Add(c.Now().Sub(sent) / 2)
		c.mu.Lock()
		c.skew = d.Sub(mid).Truncate(time.Second)
		c.mu.Unlock()
	}
	if c.onTLS != nil && resp.TLS != nil {
		c.onTLS(resp.TLS)
	}
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
