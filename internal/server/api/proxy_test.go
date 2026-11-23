// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/invenire-hq/invenire/internal/agent/transport"
	"github.com/invenire-hq/invenire/internal/server/api"
	"github.com/invenire-hq/invenire/internal/server/pki"
	"github.com/invenire-hq/invenire/internal/server/store"
	"github.com/invenire-hq/invenire/internal/testdb"
	"github.com/invenire-hq/invenire/internal/wire"
	"github.com/invenire-hq/invenire/pkg/snapshot"
)

// proxyEnv é o servidor no modo --behind-proxy (HTTP puro) com um proxy
// reverso na frente terminando o TLS com um certificado "público" (o do
// httptest), como o Nginx da instituição.
type proxyEnv struct {
	t       *testing.T
	q       *store.Queries
	tenant  store.Tenant
	backend *httptest.Server
	front   *httptest.Server
	roots   *x509.CertPool
}

func newProxyEnv(t *testing.T, trusted string) *proxyEnv {
	pool := testdb.New(t)
	mk, err := pki.NewMasterKey(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	s, err := api.New(pool, api.Config{MasterKey: mk, BehindProxy: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	prefixes, err := api.ParseTrustedProxies(trusted)
	if err != nil {
		t.Fatal(err)
	}
	backend := httptest.NewServer(api.ProxyHeaders(prefixes, s.Handler()))
	t.Cleanup(backend.Close)
	target, _ := url.Parse(backend.URL)
	// Como o Nginx com proxy_pass e os X-Forwarded-*: não pede certificado
	// cliente, então o crachá no TLS morre aqui.
	front := httptest.NewUnstartedServer(&httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)
		pr.SetXForwarded()
	}})
	front.StartTLS()
	t.Cleanup(front.Close)
	roots := x509.NewCertPool()
	roots.AddCert(front.Certificate())

	q := store.New(pool)
	tenant, err := q.CreateTenant(context.Background(), "teste")
	if err != nil {
		t.Fatal(err)
	}
	return &proxyEnv{t: t, q: q, tenant: tenant, backend: backend, front: front, roots: roots}
}

// agent é o cliente do agente, de verdade, apontado para o proxy. badge nulo
// é o agente antes do cadastro.
func (e *proxyEnv) agent(badge *tls.Certificate) *transport.Client {
	e.t.Helper()
	cfg := transport.SystemTLS(e.roots)
	if badge != nil {
		cfg.Certificates = []tls.Certificate{*badge}
	}
	c, err := transport.NewClient(e.front.URL, cfg)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *proxyEnv) enroll(host snapshot.Host) (wire.EnrollResponse, *tls.Certificate) {
	e.t.Helper()
	tok, _, err := api.NewEnrollmentToken(context.Background(), e.q, e.tenant.ID, nil, "teste", time.Hour)
	if err != nil {
		e.t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	resp, err := e.agent(nil).Enroll(context.Background(), wire.EnrollRequest{
		Token: tok, CSR: csrPEM(e.t, key), AgentVersion: "0.1.0", Host: host,
		OS: snapshot.OS{Family: "windows", Name: "Windows 11 Pro"},
	})
	if err != nil {
		e.t.Fatalf("cadastro pelo proxy: %v", err)
	}
	return resp, &tls.Certificate{Certificate: [][]byte{pemDER(e.t, resp.Certificate)}, PrivateKey: key}
}

func csrPEM(t *testing.T, key *ecdsa.PrivateKey) string {
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

func pemDER(t *testing.T, s string) []byte {
	b, _ := pem.Decode([]byte(s))
	if b == nil {
		t.Fatalf("PEM inválido: %q", s)
	}
	return b.Bytes
}

func status(err error) int {
	var he *transport.HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	if err == nil {
		return http.StatusOK
	}
	return -1
}

// Cadastro, snapshot, renovação e revogação, tudo pelo proxy que termina TLS.
func TestBehindProxy(t *testing.T) {
	e := newProxyEnv(t, "127.0.0.1/32,::1/128")
	ctx := context.Background()

	enrolled, a := e.enroll(testHost)
	if enrolled.CA != "" {
		t.Fatalf("atrás de proxy o cadastro devolveu CA: %q", enrolled.CA)
	}
	agentA := e.agent(a)

	info, _, err := agentA.Agent(ctx)
	if err != nil || info.AgentID != enrolled.AgentID {
		t.Fatalf("GET /v1/agent pelo proxy: %+v %v", info, err)
	}

	body, sum, _ := snapshot.Marshal(newSnapshot(testHost, ""))
	resp, err := agentA.SendSnapshot(ctx, body, sum)
	if err != nil || resp.Duplicate {
		t.Fatalf("snapshot pelo proxy: %+v %v", resp, err)
	}
	// Reenviar (a fila do agente, ou alguém repetindo a requisição) é inofensivo.
	if again, err := agentA.SendSnapshot(ctx, body, sum); err != nil || !again.Duplicate || again.SnapshotID != resp.SnapshotID {
		t.Fatalf("snapshot repetido: %+v %v", again, err)
	}

	// Renovação: crachá novo para a mesma chave; os dois valem.
	ren, err := agentA.Renew(ctx, csrPEM(t, a.PrivateKey.(*ecdsa.PrivateKey)))
	if err != nil {
		t.Fatalf("renovação pelo proxy: %v", err)
	}
	b := &tls.Certificate{Certificate: [][]byte{pemDER(t, ren.Certificate)}, PrivateKey: a.PrivateKey}
	for name, c := range map[string]*tls.Certificate{"A": a, "B": b} {
		if info, _, err := e.agent(c).Agent(ctx); err != nil || info.AgentID != enrolled.AgentID {
			t.Fatalf("crachá %s depois da renovação: %+v %v", name, info, err)
		}
	}
	body2, sum2, _ := snapshot.Marshal(newSnapshot(testHost, sum))
	if _, err := e.agent(b).SendSnapshot(ctx, body2, sum2); err != nil {
		t.Fatalf("snapshot com o crachá renovado: %v", err)
	}

	// Revogado: nada passa.
	if err := e.q.RevokeAgent(ctx, enrolled.AgentID); err != nil {
		t.Fatal(err)
	}
	agentB := e.agent(b)
	if _, _, err := agentB.Agent(ctx); status(err) != http.StatusUnauthorized {
		t.Fatalf("GET /v1/agent revogado: %v", err)
	}
	body3, sum3, _ := snapshot.Marshal(newSnapshot(testHost, sum2))
	if _, err := agentB.SendSnapshot(ctx, body3, sum3); status(err) != http.StatusUnauthorized {
		t.Fatalf("snapshot de agente revogado: %v", err)
	}
	if _, err := agentB.Renew(ctx, csrPEM(t, a.PrivateKey.(*ecdsa.PrivateKey))); status(err) != http.StatusUnauthorized {
		t.Fatalf("renovação de agente revogado: %v", err)
	}
}

// signed monta uma requisição assinada para o proxy, para os testes que
// mexem nela depois de assinar.
func (e *proxyEnv) signed(badge *tls.Certificate, method, path string, body []byte, at time.Time) *http.Request {
	e.t.Helper()
	req, err := http.NewRequest(method, e.front.URL+path, bytes.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	if err := transport.SignRequest(req, badge, path, body, at); err != nil {
		e.t.Fatal(err)
	}
	return req
}

func (e *proxyEnv) do(req *http.Request) (int, string) {
	e.t.Helper()
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: transport.SystemTLS(e.roots)}}
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestSignatureRejected(t *testing.T) {
	e := newProxyEnv(t, "127.0.0.1/32,::1/128")
	_, a := e.enroll(testHost)
	now := time.Now()

	if code, msg := e.do(e.signed(a, "GET", wire.PathAgent, nil, now)); code != http.StatusOK {
		t.Fatalf("requisição assinada válida: %d %s", code, msg)
	}

	body, sum, _ := snapshot.Marshal(newSnapshot(testHost, ""))
	gzBody := gz(body)
	snapReq := func(sent []byte) *http.Request {
		req := e.signed(a, "POST", wire.PathSnapshots, gzBody, now)
		req.Body = io.NopCloser(bytes.NewReader(sent))
		req.ContentLength = int64(len(sent))
		req.Header.Set("Content-Encoding", "gzip")
		req.Header.Set(wire.SHA256Header, sum)
		return req
	}
	// Corpo trocado depois de assinar.
	other, _, _ := snapshot.Marshal(newSnapshot(snapshot.Host{Hostname: "OUTRA", MachineID: "57cfe8c7-106c-48ba-b00b-3b4b61bd3d87"}, ""))
	if code, msg := e.do(snapReq(gz(other))); code != http.StatusUnauthorized || !strings.Contains(msg, "assinatura inválida") {
		t.Fatalf("corpo adulterado: %d %s", code, msg)
	}

	// Assinatura adulterada.
	req := e.signed(a, "GET", wire.PathAgent, nil, now)
	sig, _ := base64.StdEncoding.DecodeString(req.Header.Get(wire.SignatureHeader))
	sig[len(sig)/2] ^= 0xff
	req.Header.Set(wire.SignatureHeader, base64.StdEncoding.EncodeToString(sig))
	if code, msg := e.do(req); code != http.StatusUnauthorized {
		t.Fatalf("assinatura adulterada: %d %s", code, msg)
	}

	// Timestamp trocado no cabeçalho (a assinatura é do original).
	req = e.signed(a, "GET", wire.PathAgent, nil, now)
	req.Header.Set(wire.TimestampHeader, now.Add(time.Minute).UTC().Format(time.RFC3339))
	if code, msg := e.do(req); code != http.StatusUnauthorized {
		t.Fatalf("timestamp trocado: %d %s", code, msg)
	}

	// Assinada para uma rota, enviada para outra.
	req = e.signed(a, "POST", wire.PathAgentRenew, nil, now)
	req.URL.Path = wire.PathSnapshots
	if code, msg := e.do(req); code != http.StatusUnauthorized {
		t.Fatalf("caminho trocado: %d %s", code, msg)
	}

	// Fora da janela, para trás e para frente, mesmo bem assinada.
	for _, d := range []time.Duration{-wire.SignatureWindow - time.Hour, wire.SignatureWindow + time.Hour} {
		code, msg := e.do(e.signed(a, "GET", wire.PathAgent, nil, now.Add(d)))
		if code != http.StatusUnauthorized || !strings.Contains(msg, "relógio") {
			t.Fatalf("timestamp %s: %d %s", d, code, msg)
		}
	}
	// Dentro da janela (relógio 20h atrasado) passa.
	if code, msg := e.do(e.signed(a, "GET", wire.PathAgent, nil, now.Add(-20*time.Hour))); code != http.StatusOK {
		t.Fatalf("relógio 20h atrasado: %d %s", code, msg)
	}

	// Crachá de um agente assinado com outra chave.
	stranger, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if code, msg := e.do(e.signed(&tls.Certificate{Certificate: a.Certificate, PrivateKey: stranger}, "GET", wire.PathAgent, nil, now)); code != http.StatusUnauthorized {
		t.Fatalf("crachá alheio: %d %s", code, msg)
	}

	// Crachá de outra CA (não emitido pelo tenant), com a chave certa.
	ca, _ := pki.LoadOrCreate(t.TempDir())
	forged, _, err := ca.SignAgentCSR([]byte(csrPEM(t, a.PrivateKey.(*ecdsa.PrivateKey))), "forjado")
	if err != nil {
		t.Fatal(err)
	}
	fake := &tls.Certificate{Certificate: [][]byte{pemDER(t, string(forged))}, PrivateKey: a.PrivateKey}
	if code, msg := e.do(e.signed(fake, "GET", wire.PathAgent, nil, now)); code != http.StatusUnauthorized {
		t.Fatalf("crachá de outra CA: %d %s", code, msg)
	}

	// Sem assinatura (agente antigo, só mTLS): o certificado não passa o proxy.
	plain, _ := http.NewRequest("GET", e.front.URL+wire.PathAgent, nil)
	if code, msg := e.do(plain); code != http.StatusUnauthorized {
		t.Fatalf("sem assinatura: %d %s", code, msg)
	}

	// Corpo acima do limite, declarado no Content-Length: recusado antes de ler.
	big := make([]byte, 20<<10)
	req = e.signed(a, "POST", wire.PathAgentRenew, big, now)
	if code, msg := e.do(req); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("corpo grande: %d %s", code, msg)
	}
}

// Repetir uma renovação capturada derrubaria o crachá que o agente já usa
// (veja migrations/00011). O servidor só aceita timestamp maior que o da
// última renovação assinada.
func TestRenewReplay(t *testing.T) {
	e := newProxyEnv(t, "127.0.0.1/32")
	_, a := e.enroll(testHost)
	body := []byte(`{"csr":` + jsonString(csrPEM(t, a.PrivateKey.(*ecdsa.PrivateKey))) + `}`)
	at := time.Now().Add(-time.Minute)
	capture := func() *http.Request {
		req := e.signed(a, "POST", wire.PathAgentRenew, body, at)
		req.Header.Set("Content-Type", "application/json")
		return req
	}
	if code, msg := e.do(capture()); code != http.StatusOK {
		t.Fatalf("renovação: %d %s", code, msg)
	}
	if code, msg := e.do(capture()); code != http.StatusUnauthorized || !strings.Contains(msg, "repetida") {
		t.Fatalf("renovação repetida: %d %s", code, msg)
	}
	// Uma nova, de verdade, passa.
	if _, err := e.agent(a).Renew(context.Background(), csrPEM(t, a.PrivateKey.(*ecdsa.PrivateKey))); err != nil {
		t.Fatalf("renovação seguinte: %v", err)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// Máquina com o relógio dias atrasado: a primeira tentativa cai fora da
// janela, o agente acerta o timestamp pelo Date da resposta e tenta de novo.
func TestClockSkewRetry(t *testing.T) {
	e := newProxyEnv(t, "127.0.0.1/32")
	enrolled, a := e.enroll(testHost)
	c := e.agent(a)
	c.Now = func() time.Time { return time.Now().Add(-72 * time.Hour) }
	info, _, err := c.Agent(context.Background())
	if err != nil || info.AgentID != enrolled.AgentID {
		t.Fatalf("agente com relógio 72h atrasado: %+v %v", info, err)
	}
}

// Um proxy fora de --trusted-proxies (aqui, o da frente conecta de
// 127.0.0.1, e só 10.0.0.0/8 é confiável) não pode contar quem é o cliente.
func TestUntrustedProxy(t *testing.T) {
	e := newProxyEnv(t, "10.0.0.0/8")
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, err := e.agent(nil).Enroll(context.Background(), wire.EnrollRequest{Token: "inv_x", CSR: csrPEM(t, key), AgentVersion: "0.1.0", Host: testHost})
	var he *transport.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusForbidden || !strings.Contains(he.Msg, "trusted-proxies") {
		t.Fatalf("X-Forwarded-For de origem não confiável: %v", err)
	}
	// Sem os cabeçalhos, direto no servidor (o healthcheck local), passa.
	resp, err := http.Get(e.backend.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("healthz direto: %d", resp.StatusCode)
	}
}

func TestProxyHeaders(t *testing.T) {
	trusted, err := api.ParseTrustedProxies("127.0.0.1, 10.0.0.0/8 ,::1/128")
	if err != nil {
		t.Fatal(err)
	}
	var seen string
	h := api.ProxyHeaders(trusted, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.RemoteAddr
		w.WriteHeader(http.StatusNoContent)
	}))
	run := func(remote string, hdr map[string]string) int {
		seen = ""
		r := httptest.NewRequest("GET", "/v1/agent", nil)
		r.RemoteAddr = remote
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	cases := []struct {
		name, remote string
		hdr          map[string]string
		want         int
		seen         string
	}{
		{"de fora, sem cabeçalho", "203.0.113.5:4000", nil, 204, "203.0.113.5:4000"},
		{"de fora, X-Forwarded-For", "203.0.113.5:4000", map[string]string{"X-Forwarded-For": "10.1.1.1"}, 403, ""},
		{"de fora, X-Forwarded-Proto", "203.0.113.5:4000", map[string]string{"X-Forwarded-Proto": "https"}, 403, ""},
		{"de fora, Forwarded", "203.0.113.5:4000", map[string]string{"Forwarded": "for=10.1.1.1"}, 403, ""},
		{"proxy, cliente no XFF", "10.0.0.2:5000", map[string]string{"X-Forwarded-For": "198.51.100.7", "X-Forwarded-Proto": "https"}, 204, "198.51.100.7:0"},
		// O cliente escreveu um XFF falso; vale o mais à direita não confiável.
		{"proxy, XFF forjado pelo cliente", "10.0.0.2:5000", map[string]string{"X-Forwarded-For": "1.2.3.4, 198.51.100.7, 10.0.0.9"}, 204, "198.51.100.7:0"},
		{"proxy, IPv6 local", "[::1]:5000", map[string]string{"X-Forwarded-For": "2001:db8::1"}, 204, "[2001:db8::1]:0"},
		{"proxy, recebeu por HTTP", "127.0.0.1:5000", map[string]string{"X-Forwarded-Proto": "http"}, 403, ""},
	}
	for _, c := range cases {
		if got := run(c.remote, c.hdr); got != c.want || seen != c.seen {
			t.Errorf("%s: %d %q, esperava %d %q", c.name, got, seen, c.want, c.seen)
		}
	}

	for _, bad := range []string{"", " , ", "10.0.0.0/33", "proxy.local"} {
		if _, err := api.ParseTrustedProxies(bad); err == nil {
			t.Errorf("--trusted-proxies %q aceito", bad)
		}
	}
}

// No modo direto, o agente novo (assinatura) e o antigo (só mTLS) convivem;
// os testes de mTLS de api_test.go continuam valendo.
func TestDirectSignatureWithoutClientCert(t *testing.T) {
	e := newEnv(t)
	_, enrolled, cert := e.enroll(e.token(), testHost)
	req, _ := http.NewRequest("GET", e.srv.URL+wire.PathAgent, nil)
	if err := transport.SignRequest(req, cert, wire.PathAgent, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	resp, err := e.client(nil).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), enrolled.AgentID.String()) {
		t.Fatalf("assinatura no modo direto: %d %s", resp.StatusCode, b)
	}
}
