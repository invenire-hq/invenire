package runner_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/athosbes/invenire/internal/agent/runner"
	"github.com/athosbes/invenire/internal/agent/state"
	"github.com/athosbes/invenire/internal/server/api"
	"github.com/athosbes/invenire/internal/server/pki"
	"github.com/athosbes/invenire/internal/server/store"
	"github.com/athosbes/invenire/internal/testdb"
	"github.com/athosbes/invenire/pkg/snapshot"
)

var (
	quiet    = slog.New(slog.NewTextHandler(io.Discard, nil))
	thisHost = snapshot.Host{Hostname: "PMC-ARR-01", SMBIOSUUID: "081B0196-A1A3-65A2-8D37-08626699AC78"}
	thisOS   = snapshot.OS{Family: "windows", Name: "Windows 11 Pro"}
)

type env struct {
	q      *store.Queries
	url    string
	pin    []byte
	tenant store.Tenant
	down   atomic.Bool // servidor fora do ar (503)
}

func newEnv(t *testing.T) *env {
	pool := testdb.New(t)
	ca, err := pki.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mk, err := pki.NewMasterKey(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	s, err := api.New(pool, api.Config{ServerCA: ca, MasterKey: mk}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := ca.ServerCert([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{q: store.New(pool)}
	h := s.Handler()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if e.down.Load() {
			http.Error(w, "fora do ar", http.StatusServiceUnavailable)
			return
		}
		h.ServeHTTP(w, r)
	}))
	srv.TLS = s.TLSConfig(cert)
	srv.StartTLS()
	t.Cleanup(srv.Close)

	pin := sha256.Sum256(ca.Cert.Raw)
	e.url, e.pin = srv.URL, pin[:]
	if e.tenant, err = e.q.CreateTenant(context.Background(), "teste"); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) token(t *testing.T) string {
	tok, _, err := api.NewEnrollmentToken(context.Background(), e.q, e.tenant.ID, nil, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (e *env) enrollOpts(token string) runner.EnrollOptions {
	return runner.EnrollOptions{
		ServerURL: e.url, Token: token, CAPin: e.pin, AgentVersion: "test",
		Identity: func(context.Context) (snapshot.Host, snapshot.OS, error) { return thisHost, thisOS, nil },
	}
}

// collector devolve snapshots deste host, cada um com um instante diferente.
func collector(host *snapshot.Host) func(context.Context, string) *snapshot.Snapshot {
	var n atomic.Int64
	t0 := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	return func(_ context.Context, v string) *snapshot.Snapshot {
		s := snapshot.New(v, t0.Add(time.Duration(n.Add(1))*time.Hour))
		s.Host, s.OS = *host, thisOS
		return s
	}
}

func TestEnrollPin(t *testing.T) {
	e := newEnv(t)
	dir, err := state.OpenUnrestricted(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tok := e.token(t)

	wrong := e.enrollOpts(tok)
	wrong.CAPin = make([]byte, 32)
	if _, err := runner.Enroll(context.Background(), dir, wrong); err == nil || !strings.Contains(err.Error(), "pin") {
		t.Fatalf("pin errado devia falhar no TLS: %v", err)
	}
	// O TLS falhou antes de o token chegar ao servidor: ele continua valendo.
	if _, err := runner.Enroll(context.Background(), dir, e.enrollOpts(tok)); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Enroll(context.Background(), dir, e.enrollOpts(e.token(t))); err == nil {
		t.Fatal("recadastrou sem --force")
	}
	opts := e.enrollOpts(e.token(t))
	opts.Force = true
	if _, err := runner.Enroll(context.Background(), dir, opts); err != nil {
		t.Fatalf("recadastro com --force: %v", err)
	}
}

func TestQueueAndDelivery(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	dir, err := state.OpenUnrestricted(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := runner.Enroll(ctx, dir, e.enrollOpts(e.token(t)))
	if err != nil {
		t.Fatal(err)
	}
	host := thisHost
	a := &runner.Agent{Dir: dir, Version: "test", Log: quiet, Collect: collector(&host)}
	if err := a.Open(); err != nil {
		t.Fatal(err)
	}
	pending := func() int {
		n, err := a.Pending()
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Servidor fora do ar: os snapshots esperam na fila, encadeados.
	e.down.Store(true)
	for range 3 {
		if _, err := a.Enqueue(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Flush(ctx); err == nil || pending() != 3 {
		t.Fatalf("flush com servidor fora: err=%v, fila=%d", err, pending())
	}

	// Volta: a fila sai inteira, na ordem, e o encadeamento fecha no servidor.
	e.down.Store(false)
	if sent, err := a.Flush(ctx); err != nil || sent != 3 || pending() != 0 {
		t.Fatalf("flush: enviados=%d err=%v fila=%d", sent, err, pending())
	}
	assetID := mustUUID(t, cfg.AssetID)
	chain, err := e.q.VerifySnapshotChain(ctx, assetID)
	if err != nil || len(chain) != 3 {
		t.Fatalf("cadeia: %+v %v", chain, err)
	}
	for i, c := range chain {
		if !c.BodyOk || !c.ChainOk {
			t.Errorf("snapshot %d: body_ok=%v chain_ok=%v", i, c.BodyOk, c.ChainOk)
		}
	}

	// Snapshot que o servidor recusa de vez (outro ativo) vai para rejected/
	// e não trava os seguintes.
	host = snapshot.Host{Hostname: "OUTRA", MachineID: "57cfe8c7-106c-48ba-b00b-3b4b61bd3d87"}
	a.Enqueue(ctx)
	host = thisHost
	a.Enqueue(ctx)
	if sent, err := a.Flush(ctx); err != nil || sent != 1 || pending() != 0 {
		t.Fatalf("flush com recusa: enviados=%d err=%v fila=%d", sent, err, pending())
	}

	// Revogado: para, avisa e não perde o snapshot.
	if err := e.q.RevokeAgent(ctx, mustUUID(t, cfg.AgentID)); err != nil {
		t.Fatal(err)
	}
	a.Enqueue(ctx)
	if _, err := a.Flush(ctx); !errors.Is(err, runner.ErrRevoked) || pending() != 1 {
		t.Fatalf("revogado: err=%v fila=%d", err, pending())
	}
}

func mustUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	u, err := uuid.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
