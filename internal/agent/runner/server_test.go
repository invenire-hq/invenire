package runner_test

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/athosbes/invenire/internal/agent/runner"
	"github.com/athosbes/invenire/internal/agent/state"
	"github.com/athosbes/invenire/internal/server/api"
	"github.com/athosbes/invenire/internal/server/pki"
	"github.com/athosbes/invenire/internal/server/store"
	"github.com/athosbes/invenire/internal/testdb"
	"github.com/athosbes/invenire/pkg/snapshot"
)

var master, _ = pki.NewMasterKey(make([]byte, 32))

// serve sobe um servidor sobre pool. Com public, o certificado sai de uma CA
// "pública" (devolvida como roots, no papel das CAs do sistema) e o cadastro
// não devolve CA; sem public, é o modo pin de sempre.
func serve(t *testing.T, pool *pgxpool.Pool, public bool) (url string, pin []byte, roots *x509.CertPool) {
	t.Helper()
	ca, err := pki.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := api.Config{MasterKey: master}
	if !public {
		cfg.ServerCA = ca
	}
	s, err := api.New(pool, cfg, quiet)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := ca.ServerCert([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(s.Handler())
	srv.TLS = s.TLSConfig(cert)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	h := sha256.Sum256(ca.Cert.Raw)
	return srv.URL, h[:], ca.Pool()
}

func deliver(t *testing.T, dir state.Dir, roots *x509.CertPool) {
	t.Helper()
	host := thisHost
	a := &runner.Agent{Dir: dir, Version: "test", Log: quiet, Collect: collector(&host), SystemRoots: roots}
	if err := a.Open(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Enqueue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n, err := a.Flush(context.Background()); err != nil || n != 1 {
		t.Fatalf("envio: %d %v", n, err)
	}
}

// Cadastro num servidor com certificado público e, depois, o tenant muda
// para outro servidor (mesmo banco, outra CA de servidor): o agente troca de
// endereço sem recadastrar.
func TestSystemTrustAndSetServer(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	q := store.New(pool)
	tenant, err := q.CreateTenant(ctx, "teste")
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := api.NewEnrollmentToken(ctx, q, tenant.ID, nil, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	pubURL, _, pubRoots := serve(t, pool, true)
	newURL, newPin, _ := serve(t, pool, false)

	path := t.TempDir()
	dir, _ := state.OpenUnrestricted(path)
	cfg, err := runner.Enroll(ctx, dir, runner.EnrollOptions{
		ServerURL: pubURL, Token: tok, SystemRoots: pubRoots, AgentVersion: "test",
		Identity: func(context.Context) (snapshot.Host, snapshot.OS, error) { return thisHost, thisOS, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := dir.Config(); c.ServerTrust != state.TrustSystem {
		t.Fatalf("server_trust: %q", c.ServerTrust)
	}
	if _, err := os.Stat(filepath.Join(path, "ca.crt")); !os.IsNotExist(err) {
		t.Fatalf("ca.crt no modo público: %v", err)
	}
	deliver(t, dir, pubRoots)

	// Pin errado: nada muda.
	if _, err := runner.SetServer(ctx, dir, runner.SetServerOptions{ServerURL: newURL, CAPin: make([]byte, 32)}); err == nil {
		t.Fatal("aceitou pin errado")
	}
	// Servidor que não conhece o agente (outro banco): nada muda.
	strangerURL, strangerPin, _ := serve(t, testdb.New(t), false)
	_, err = runner.SetServer(ctx, dir, runner.SetServerOptions{ServerURL: strangerURL, CAPin: strangerPin})
	if err == nil || !strings.Contains(err.Error(), "não reconhece") {
		t.Fatalf("servidor sem o agente: %v", err)
	}
	if c, _ := dir.Config(); c.ServerURL != pubURL {
		t.Fatalf("config mudou depois de falhar: %+v", c)
	}

	info, err := runner.SetServer(ctx, dir, runner.SetServerOptions{ServerURL: newURL, CAPin: newPin})
	if err != nil || info.AgentID.String() != cfg.AgentID {
		t.Fatalf("set-server: %+v %v", info, err)
	}
	if c, _ := dir.Config(); c.ServerURL != newURL || c.ServerTrust != state.TrustPin {
		t.Fatalf("config depois do set-server: %+v", c)
	}
	deliver(t, dir, nil) // agora pela CA do servidor novo, guardada em ca.crt
}
