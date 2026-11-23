// SPDX-License-Identifier: AGPL-3.0-only

package runner_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/invenire-hq/invenire/internal/agent/runner"
	"github.com/invenire-hq/invenire/internal/agent/state"
	"github.com/invenire-hq/invenire/internal/server/api"
	"github.com/invenire-hq/invenire/internal/server/pki"
	"github.com/invenire-hq/invenire/internal/server/store"
	"github.com/invenire-hq/invenire/internal/testdb"
	"github.com/invenire-hq/invenire/internal/wire"
	"github.com/invenire-hq/invenire/pkg/snapshot"
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
	// proxy, se definido, fica entre o agente e o servidor: recebe cada
	// requisição e o handler do servidor, para simular um proxy mal
	// configurado.
	proxy atomic.Pointer[func(w http.ResponseWriter, r *http.Request, next http.Handler)]
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
		if p := e.proxy.Load(); p != nil {
			(*p)(w, r, h)
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

// Um proxy mal configurado no caminho não pode parar o agente nem fazer
// perder snapshots: o 401 que não é de revogação e o 413 do proxy deixam o
// snapshot na fila, e ele sai quando o proxy é corrigido.
func TestMisconfiguredProxy(t *testing.T) {
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
	if _, err := a.Enqueue(ctx); err != nil {
		t.Fatal(err)
	}
	pending := func() int {
		n, err := a.Pending()
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	rejected := func() int {
		ents, err := os.ReadDir(filepath.Join(dir.QueueDir(), "rejected"))
		if err != nil {
			t.Fatal(err)
		}
		return len(ents)
	}
	// Proxy que termina o TLS: o servidor só vê a assinatura.
	terminatesTLS := func(r *http.Request) *http.Request {
		r = r.Clone(r.Context())
		r.TLS = nil
		return r
	}
	snapshots := func(r *http.Request) bool {
		return r.Method == http.MethodPost && r.URL.Path == wire.PathSnapshots
	}

	cases := []struct {
		name  string
		proxy func(w http.ResponseWriter, r *http.Request, next http.Handler)
		want  string
	}{
		{"proxy que reescreve o corpo", func(w http.ResponseWriter, r *http.Request, next http.Handler) {
			r = terminatesTLS(r)
			if snapshots(r) {
				body, _ := io.ReadAll(r.Body)
				body = append(body, 0) // o corpo muda depois da assinatura
				r.Body, r.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
			}
			next.ServeHTTP(w, r)
		}, "assinatura inválida"},
		{"proxy que não repassa a assinatura", func(w http.ResponseWriter, r *http.Request, next http.Handler) {
			r = terminatesTLS(r)
			r.Header.Del(wire.AgentCertHeader)
			next.ServeHTTP(w, r)
		}, "crachá do agente obrigatório"},
		{"proxy com login próprio", func(w http.ResponseWriter, r *http.Request, next http.Handler) {
			w.Header().Set("WWW-Authenticate", `Basic realm="intranet"`)
			http.Error(w, "<html>401 Authorization Required</html>", http.StatusUnauthorized)
		}, "401 Authorization Required"},
		{"proxy com limite de corpo baixo", func(w http.ResponseWriter, r *http.Request, next http.Handler) {
			if snapshots(r) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				io.WriteString(w, "<html><center><h1>413 Request Entity Too Large</h1></center><hr><center>nginx</center></html>")
				return
			}
			next.ServeHTTP(w, r)
		}, "413 Request Entity Too Large"},
	}
	for _, c := range cases {
		e.proxy.Store(&c.proxy)
		_, err := a.Flush(ctx)
		if err == nil || errors.Is(err, runner.ErrRevoked) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err=%v, esperava erro momentâneo com %q", c.name, err, c.want)
		}
		if pending() != 1 || rejected() != 0 {
			t.Errorf("%s: fila=%d rejeitados=%d, esperava o snapshot na fila", c.name, pending(), rejected())
		}
	}

	// Proxy corrigido (e terminando o TLS): o snapshot que esperou é entregue.
	fixed := func(w http.ResponseWriter, r *http.Request, next http.Handler) { next.ServeHTTP(w, terminatesTLS(r)) }
	e.proxy.Store(&fixed)
	if sent, err := a.Flush(ctx); err != nil || sent != 1 || pending() != 0 {
		t.Fatalf("proxy corrigido: enviados=%d err=%v fila=%d", sent, err, pending())
	}

	// Revogação atrás do proxy continua parando o agente, sem perder o
	// snapshot.
	if err := e.q.RevokeAgent(ctx, mustUUID(t, cfg.AgentID)); err != nil {
		t.Fatal(err)
	}
	// O servidor marca a recusa com o código de revogação (wire.Error.Code),
	// que é o que o agente confere.
	var codes []string
	recording := func(w http.ResponseWriter, r *http.Request, next http.Handler) {
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, terminatesTLS(r))
		var body wire.Error
		if rec.Code == http.StatusUnauthorized && json.Unmarshal(rec.Body.Bytes(), &body) == nil {
			codes = append(codes, body.Code)
		}
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		w.Write(rec.Body.Bytes())
	}
	e.proxy.Store(&recording)
	a.Enqueue(ctx)
	if _, err := a.Flush(ctx); !errors.Is(err, runner.ErrRevoked) || pending() != 1 || rejected() != 0 {
		t.Fatalf("revogado atrás do proxy: err=%v fila=%d rejeitados=%d", err, pending(), rejected())
	}
	if len(codes) != 1 || codes[0] != wire.ErrorCodeAgentRevoked {
		t.Fatalf("códigos do 401: %q", codes)
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
