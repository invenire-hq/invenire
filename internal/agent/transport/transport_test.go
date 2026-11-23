// SPDX-License-Identifier: AGPL-3.0-only

package transport_test

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/invenire-hq/invenire/internal/agent/transport"
	"github.com/invenire-hq/invenire/internal/server/pki"
	"github.com/invenire-hq/invenire/internal/wire"
)

// Servidor com certificado só para invenire.local, chamado por 127.0.0.1:
// o erro tem de dizer o que fazer.
func TestPinnedTLSHostnameHint(t *testing.T) {
	ca, err := pki.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cert, err := ca.ServerCert([]string{"invenire.local"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.NotFoundHandler())
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	defer srv.Close()

	pin := sha256.Sum256(ca.Cert.Raw)
	get := func(pin []byte, host string) error {
		hc := &http.Client{Transport: &http.Transport{TLSClientConfig: transport.PinnedTLS(pin, host)}}
		resp, err := hc.Get(srv.URL)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	// Por IP: o nome também é conferido (o Go não o põe no SNI).
	if err := get(pin[:], "127.0.0.1"); err == nil || !strings.Contains(err.Error(), "suba o servidor com 127.0.0.1 em --hosts") {
		t.Fatalf("erro sem a dica: %v", err)
	}
	// O nome certo passa, mesmo conectando pelo IP.
	if err := get(pin[:], "invenire.local"); err != nil {
		t.Fatalf("nome certo recusado: %v", err)
	}
	bad := sha256.Sum256([]byte("outra CA"))
	err = get(bad[:], "invenire.local")
	if err == nil || !strings.Contains(err.Error(), "não confere com o pin") {
		t.Fatalf("pin errado: %v", err)
	}
}

// Só a recusa do crachá em si é revogação; só o 413 do próprio servidor é
// definitivo.
func TestHTTPErrorClass(t *testing.T) {
	cases := []struct {
		e                  transport.HTTPError
		revoked, permanent bool
	}{
		{transport.HTTPError{Status: 401, Msg: "agente desconhecido ou revogado", Code: wire.ErrorCodeAgentRevoked}, true, false},
		{transport.HTTPError{Status: 401, Msg: "certificado cliente inválido ou vencido", Code: wire.ErrorCodeBadgeInvalid}, true, false},
		{transport.HTTPError{Status: 401, Msg: "outro texto", Code: wire.ErrorCodeAgentRevoked}, true, false},
		// Servidor anterior ao código: reconhecido pela mensagem.
		{transport.HTTPError{Status: 401, Msg: "agente desconhecido ou revogado"}, true, false},
		{transport.HTTPError{Status: 401, Msg: "certificado cliente inválido ou vencido"}, true, false},
		{transport.HTTPError{Status: 401, Msg: "assinatura inválida"}, false, false},
		{transport.HTTPError{Status: 401, Msg: "crachá do agente obrigatório (certificado cliente ou assinatura)"}, false, false},
		{transport.HTTPError{Status: 401, Msg: "<html>401 Authorization Required</html>"}, false, false},
		{transport.HTTPError{Status: 403, Msg: "agente desconhecido ou revogado", Code: wire.ErrorCodeAgentRevoked}, false, true},
		{transport.HTTPError{Status: 413, Msg: "<html>413 Request Entity Too Large</html>"}, false, false},
		{transport.HTTPError{Status: 413, Msg: "corpo grande demais"}, false, false},
		{transport.HTTPError{Status: 413, Msg: "snapshot grande demais", Code: wire.ErrorCodeTooLarge}, false, true},
		{transport.HTTPError{Status: 400, Msg: "JSON inválido"}, false, true},
		{transport.HTTPError{Status: 429}, false, false},
		{transport.HTTPError{Status: 503}, false, false},
	}
	for _, c := range cases {
		if got := c.e.Revoked(); got != c.revoked {
			t.Errorf("%d %q %q: Revoked=%v, esperava %v", c.e.Status, c.e.Msg, c.e.Code, got, c.revoked)
		}
		if got := c.e.Permanent(); got != c.permanent {
			t.Errorf("%d %q %q: Permanent=%v, esperava %v", c.e.Status, c.e.Msg, c.e.Code, got, c.permanent)
		}
	}
}

// O código de erro do corpo chega ao HTTPError.
func TestHTTPErrorCode(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(wire.Error{Error: "recusado", Code: wire.ErrorCodeAgentRevoked})
	}))
	defer srv.Close()
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	c, err := transport.NewClient(srv.URL, transport.SystemTLS(roots))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.SendSnapshot(context.Background(), []byte("{}"), "x")
	var he *transport.HTTPError
	if !errors.As(err, &he) || he.Code != wire.ErrorCodeAgentRevoked || he.Msg != "recusado" || !he.Revoked() {
		t.Fatalf("err=%#v", err)
	}
}
