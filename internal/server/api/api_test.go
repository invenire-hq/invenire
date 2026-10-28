package api_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/athosbes/invenire/internal/server/api"
	"github.com/athosbes/invenire/internal/server/pki"
	"github.com/athosbes/invenire/internal/server/store"
	"github.com/athosbes/invenire/internal/testdb"
	"github.com/athosbes/invenire/pkg/snapshot"
)

type env struct {
	t      *testing.T
	q      *store.Queries
	srv    *httptest.Server
	ca     *pki.CA
	tenant store.Tenant
}

func newEnv(t *testing.T) *env {
	pool := testdb.New(t)
	ca, err := pki.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := api.New(pool, ca, slog.New(slog.NewTextHandler(io.Discard, nil)))
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

	q := store.New(pool)
	tenant, err := q.CreateTenant(context.Background(), "teste")
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, q: q, srv: srv, ca: ca, tenant: tenant}
}

func (e *env) token() string {
	tok, _, err := api.NewEnrollmentToken(context.Background(), e.q, e.tenant.ID, "teste", time.Hour)
	if err != nil {
		e.t.Fatal(err)
	}
	return tok
}

// client faz o papel do agente: confia só na CA interna e, depois do
// cadastro, apresenta o certificado cliente.
func (e *env) client(cert *tls.Certificate) *http.Client {
	cfg := &tls.Config{RootCAs: e.ca.Pool(), ServerName: "127.0.0.1"}
	if cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
}

func (e *env) post(c *http.Client, path string, body []byte, hdr map[string]string) (int, []byte) {
	e.t.Helper()
	req, _ := http.NewRequest("POST", e.srv.URL+path, bytes.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

var testHost = snapshot.Host{Hostname: "PMC-ARR-01", SMBIOSUUID: "081B0196-A1A3-65A2-8D37-08626699AC78"}

// enroll gera a chave do agente, cadastra e devolve o certificado pronto.
func (e *env) enroll(token string, host snapshot.Host) (int, api.EnrollResponse, *tls.Certificate) {
	e.t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "agente"}}, key)
	body, _ := json.Marshal(api.EnrollRequest{
		Token:        token,
		CSR:          string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})),
		AgentVersion: "0.1.0",
		Host:         host,
		OS:           snapshot.OS{Family: "windows", Name: "Windows 11 Pro"},
	})
	status, b := e.post(e.client(nil), "/v1/enroll", body, nil)
	var resp api.EnrollResponse
	if status != http.StatusCreated {
		return status, resp, nil
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		e.t.Fatal(err)
	}
	cb, _ := pem.Decode([]byte(resp.Certificate))
	return status, resp, &tls.Certificate{Certificate: [][]byte{cb.Bytes}, PrivateKey: key}
}

func newSnapshot(host snapshot.Host, prev string) *snapshot.Snapshot {
	s := snapshot.New("0.1.0", time.Now().UTC().Truncate(time.Second))
	s.Host = host
	s.OS = snapshot.OS{Family: "windows", Name: "Windows 11 Pro"}
	s.PrevSHA256 = prev
	s.Software = []snapshot.Software{{Source: "win_registry", Name: "Google Chrome", Version: "130.0.6723.70"}}
	return s
}

func gz(b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

func (e *env) send(c *http.Client, body []byte, sum string) (int, api.SnapshotResponse, string) {
	e.t.Helper()
	status, b := e.post(c, "/v1/snapshots", gz(body), map[string]string{
		"Content-Encoding": "gzip",
		"Content-Type":     "application/json",
		api.SHA256Header:   sum,
	})
	var resp api.SnapshotResponse
	json.Unmarshal(b, &resp)
	return status, resp, string(b)
}

func TestEnroll(t *testing.T) {
	e := newEnv(t)

	if status, _, _ := e.enroll("inv_naoexiste", testHost); status != http.StatusUnauthorized {
		t.Fatalf("token inexistente: %d", status)
	}
	if status, _, _ := e.enroll(e.token(), snapshot.Host{Hostname: "sem-id"}); status != http.StatusUnprocessableEntity {
		t.Fatalf("host sem identificação: %d", status)
	}

	tok := e.token()
	status, resp, _ := e.enroll(tok, testHost)
	if status != http.StatusCreated || resp.CA != string(e.ca.PEM()) {
		t.Fatalf("cadastro: %d %+v", status, resp)
	}
	if status, _, _ := e.enroll(tok, testHost); status != http.StatusUnauthorized {
		t.Fatalf("token reutilizado: %d", status)
	}

	// A mesma máquina cadastrada de novo continua sendo o mesmo ativo.
	_, again, _ := e.enroll(e.token(), testHost)
	if again.AssetID != resp.AssetID || again.AgentID == resp.AgentID {
		t.Fatalf("recadastro: %+v depois de %+v", again, resp)
	}

	changes, err := e.q.ListChangesAfter(context.Background(), store.ListChangesAfterParams{TenantID: e.tenant.ID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, c := range changes {
		kinds = append(kinds, c.Kind)
	}
	if got := strings.Join(kinds, ","); got != "asset_new,agent_enrolled,agent_enrolled" {
		t.Fatalf("change_log: %s", got)
	}
}

func TestSnapshots(t *testing.T) {
	e := newEnv(t)
	_, enrolled, cert := e.enroll(e.token(), testHost)
	agent := e.client(cert)

	s1 := newSnapshot(testHost, "")
	s1.AgentID = enrolled.AgentID.String()
	body1, sum1, err := snapshot.Marshal(s1)
	if err != nil {
		t.Fatal(err)
	}

	if status, _, _ := e.send(e.client(nil), body1, sum1); status != http.StatusUnauthorized {
		t.Fatalf("sem certificado: %d", status)
	}
	if status, _, msg := e.send(agent, body1, strings.Repeat("0", 64)); status != http.StatusBadRequest {
		t.Fatalf("hash errado: %d %s", status, msg)
	}

	status, r1, msg := e.send(agent, body1, sum1)
	if status != http.StatusAccepted || r1.Duplicate || r1.SHA256 != sum1 {
		t.Fatalf("envio: %d %s", status, msg)
	}
	status, dup, _ := e.send(agent, body1, sum1)
	if status != http.StatusOK || !dup.Duplicate || dup.SnapshotID != r1.SnapshotID {
		t.Fatalf("reenvio: %d %+v", status, dup)
	}

	// Encadeado corretamente: nenhum alerta.
	body2, sum2, _ := snapshot.Marshal(newSnapshot(testHost, sum1))
	if status, _, msg := e.send(agent, body2, sum2); status != http.StatusAccepted {
		t.Fatalf("segundo snapshot: %d %s", status, msg)
	}
	// prev_sha256 errado: aceito, mas vira alerta no change_log.
	s3 := newSnapshot(testHost, sum1)
	s3.CollectedAt = s3.CollectedAt.Add(time.Minute) // senão sai idêntico ao segundo
	body3, sum3, _ := snapshot.Marshal(s3)
	if status, _, msg := e.send(agent, body3, sum3); status != http.StatusAccepted {
		t.Fatalf("snapshot com encadeamento quebrado: %d %s", status, msg)
	}
	changes, _ := e.q.ListChangesAfter(context.Background(), store.ListChangesAfterParams{TenantID: e.tenant.ID, Limit: 10})
	if last := changes[len(changes)-1]; last.Kind != "snapshot_chain_broken" || len(changes) != 3 {
		t.Fatalf("esperava um snapshot_chain_broken no fim: %+v", changes)
	}

	other := snapshot.Host{Hostname: "OUTRA", MachineID: "57cfe8c7-106c-48ba-b00b-3b4b61bd3d87"}
	body4, sum4, _ := snapshot.Marshal(newSnapshot(other, ""))
	if status, _, msg := e.send(agent, body4, sum4); status != http.StatusForbidden {
		t.Fatalf("snapshot de outro ativo: %d %s", status, msg)
	}

	bad := bytes.Replace(body1, []byte(`"win_registry"`), []byte(`"chocolatey"`), 1)
	if status, _, msg := e.send(agent, bad, snapshot.Sum(bad)); status != http.StatusUnprocessableEntity {
		t.Fatalf("fora do schema: %d %s", status, msg)
	}

	if err := e.q.RevokeAgent(context.Background(), enrolled.AgentID); err != nil {
		t.Fatal(err)
	}
	body5, sum5, _ := snapshot.Marshal(newSnapshot(testHost, sum3))
	if status, _, _ := e.send(agent, body5, sum5); status != http.StatusUnauthorized {
		t.Fatalf("agente revogado: %d", status)
	}
}

func TestForeignCA(t *testing.T) {
	e := newEnv(t)
	foreign, err := pki.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	certPEM, _, err := foreign.SignAgentCSR(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}), "intruso")
	if err != nil {
		t.Fatal(err)
	}
	cb, _ := pem.Decode(certPEM)
	c := e.client(&tls.Certificate{Certificate: [][]byte{cb.Bytes}, PrivateKey: key})
	req, _ := http.NewRequest("POST", e.srv.URL+"/v1/snapshots", strings.NewReader("{}"))
	if resp, err := c.Do(req); err == nil {
		resp.Body.Close()
		t.Fatalf("certificado de outra CA aceito: %d", resp.StatusCode)
	}
}
