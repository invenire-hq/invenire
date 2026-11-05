package transport_test

import (
	"crypto/sha256"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/athosbes/invenire/internal/agent/transport"
	"github.com/athosbes/invenire/internal/server/pki"
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
