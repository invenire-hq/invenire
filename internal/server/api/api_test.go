// SPDX-License-Identifier: AGPL-3.0-only

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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/invenire-hq/invenire/internal/server/api"
	"github.com/invenire-hq/invenire/internal/server/pki"
	"github.com/invenire-hq/invenire/internal/server/store"
	"github.com/invenire-hq/invenire/internal/testdb"
	"github.com/invenire-hq/invenire/internal/wire"
	"github.com/invenire-hq/invenire/pkg/snapshot"
)

type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	api    *api.Server
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
	mk, err := pki.NewMasterKey(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	s, err := api.New(pool, api.Config{ServerCA: ca, MasterKey: mk}, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
	return &env{t: t, pool: pool, api: s, q: q, srv: srv, ca: ca, tenant: tenant}
}

func (e *env) token() string {
	tok, _, err := api.NewEnrollmentToken(context.Background(), e.q, e.tenant.ID, nil, "teste", time.Hour)
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
func (e *env) enroll(token string, host snapshot.Host) (int, wire.EnrollResponse, *tls.Certificate) {
	e.t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "agente"}}, key)
	body, _ := json.Marshal(wire.EnrollRequest{
		Token:        token,
		CSR:          string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})),
		AgentVersion: "0.1.0",
		Host:         host,
		OS:           snapshot.OS{Family: "windows", Name: "Windows 11 Pro"},
	})
	status, b := e.post(e.client(nil), "/v1/enroll", body, nil)
	var resp wire.EnrollResponse
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

func (e *env) send(c *http.Client, body []byte, sum string) (int, wire.SnapshotResponse, string) {
	e.t.Helper()
	status, b := e.post(c, "/v1/snapshots", gz(body), map[string]string{
		"Content-Encoding": "gzip",
		"Content-Type":     "application/json",
		wire.SHA256Header:  sum,
	})
	var resp wire.SnapshotResponse
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
	if got := strings.Join(kinds, ","); got != "tenant_ca_created,asset_new,agent_enrolled,agent_enrolled" {
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
	if last := changes[len(changes)-1]; last.Kind != "snapshot_chain_broken" || len(changes) != 4 {
		t.Fatalf("esperava um snapshot_chain_broken no fim: %+v", changes)
	}

	// O histórico pode ser reconferido só com o que ficou no banco: os bytes
	// batem com o hash e só o terceiro snapshot quebra o encadeamento.
	chain, err := e.q.VerifySnapshotChain(context.Background(), enrolled.AssetID)
	if err != nil || len(chain) != 3 {
		t.Fatalf("verificação: %+v %v", chain, err)
	}
	for i, c := range chain {
		if !c.BodyOk || c.ChainOk != (i != 2) {
			t.Errorf("snapshot %d: body_ok=%v chain_ok=%v", i, c.BodyOk, c.ChainOk)
		}
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
	if status := e.get(c, "/v1/agent", nil); status != http.StatusUnauthorized {
		t.Fatalf("certificado de outra CA aceito: %d", status)
	}

	// Mesmo que o banco tivesse um agente com esse crachá, ele não confere
	// com a CA do tenant que diz tê-lo assinado.
	ctx := context.Background()
	_, _, _ = e.enroll(e.token(), testHost) // cria a CA do tenant
	ca, err := e.q.GetSigningCA(ctx, e.tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := e.q.UpsertAsset(ctx, store.UpsertAssetParams{TenantID: e.tenant.ID, Fingerprint: "intruso"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.q.CreateAgent(ctx, store.CreateAgentParams{ID: uuid.New(), AssetID: asset.ID,
		CertFingerprint: pki.Fingerprint(cb.Bytes), AgentVersion: "x", TenantCaID: &ca.ID}); err != nil {
		t.Fatal(err)
	}
	if status := e.get(c, "/v1/agent", nil); status != http.StatusUnauthorized {
		t.Fatalf("crachá de outra CA aceito por estar no banco: %d", status)
	}
}

func (e *env) get(c *http.Client, path string, out any) int {
	e.t.Helper()
	resp, err := c.Get(e.srv.URL + path)
	if err != nil {
		e.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func issuer(t *testing.T, cert *tls.Certificate) string {
	c, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return c.Issuer.CommonName
}

// Cada tenant tem a sua CA, criada no primeiro cadastro; o crachá de um não
// vale com a CA revogada.
func TestTenantCAs(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	other, err := e.q.CreateTenant(ctx, "outro")
	if err != nil {
		t.Fatal(err)
	}
	otherTok, _, err := api.NewEnrollmentToken(ctx, e.q, other.ID, nil, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	_, a1, c1 := e.enroll(e.token(), testHost)
	_, _, c2 := e.enroll(e.token(), snapshot.Host{MachineID: "segunda-maquina"})
	_, _, c3 := e.enroll(otherTok, snapshot.Host{MachineID: "maquina-do-outro"})
	if issuer(t, c1) != "Invenire CA: teste" || issuer(t, c2) != issuer(t, c1) || issuer(t, c3) != "Invenire CA: outro" {
		t.Fatalf("emissores: %q %q %q", issuer(t, c1), issuer(t, c2), issuer(t, c3))
	}
	var info wire.AgentInfo
	if status := e.get(e.client(c1), "/v1/agent", &info); status != http.StatusOK || info.AgentID != a1.AgentID {
		t.Fatalf("GET /v1/agent: %d %+v", status, info)
	}
	changes, _ := e.q.ListChangesAfter(ctx, store.ListChangesAfterParams{TenantID: e.tenant.ID, Limit: 10})
	var created int
	for _, c := range changes {
		if c.Kind == "tenant_ca_created" {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("tenant_ca_created: %d", created)
	}

	ca, err := e.q.GetSigningCA(ctx, e.tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.q.RevokeTenantCA(ctx, ca.ID); err != nil {
		t.Fatal(err)
	}
	if status := e.get(e.client(c1), "/v1/agent", nil); status != http.StatusUnauthorized {
		t.Fatalf("CA revogada aceita: %d", status)
	}
	if status := e.get(e.client(c3), "/v1/agent", nil); status != http.StatusOK {
		t.Fatalf("o outro tenant parou junto: %d", status)
	}
	// O próximo cadastro cria uma CA nova para o tenant.
	_, _, c4 := e.enroll(e.token(), snapshot.Host{MachineID: "terceira"})
	if c4 == nil || e.get(e.client(c4), "/v1/agent", nil) != http.StatusOK {
		t.Fatal("cadastro depois de revogar a CA")
	}
}

// Agentes de antes das CAs por tenant, com crachá da CA da instalação,
// continuam valendo depois de ImportLegacyCA.
func TestLegacyAgents(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	agentID := uuid.New()
	certPEM, fp, err := e.ca.SignAgentCSR(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}), agentID.String())
	if err != nil {
		t.Fatal(err)
	}
	asset, err := e.q.UpsertAsset(ctx, store.UpsertAssetParams{TenantID: e.tenant.ID, Fingerprint: "antiga"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.q.CreateAgent(ctx, store.CreateAgentParams{ID: agentID, AssetID: asset.ID, CertFingerprint: fp, AgentVersion: "0.0.9"}); err != nil {
		t.Fatal(err)
	}
	cb, _ := pem.Decode(certPEM)
	c := e.client(&tls.Certificate{Certificate: [][]byte{cb.Bytes}, PrivateKey: key})
	if status := e.get(c, "/v1/agent", nil); status != http.StatusUnauthorized {
		t.Fatalf("agente sem CA aceito: %d", status)
	}
	if _, err := api.ImportLegacyCA(ctx, e.pool, nil); err == nil {
		t.Fatal("importou sem a CA antiga")
	}
	for range 2 { // de novo não muda nada
		if _, err := api.ImportLegacyCA(ctx, e.pool, e.ca); err != nil {
			t.Fatal(err)
		}
	}
	if status := e.get(c, "/v1/agent", nil); status != http.StatusOK {
		t.Fatalf("agente antigo depois da importação: %d", status)
	}
	// Cadastros novos não saem da CA antiga.
	_, _, fresh := e.enroll(e.token(), testHost)
	if issuer(t, fresh) != "Invenire CA: teste" {
		t.Fatalf("emissor do cadastro novo: %q", issuer(t, fresh))
	}
}

func TestNULInSnapshot(t *testing.T) {
	e := newEnv(t)
	_, _, cert := e.enroll(e.token(), testHost)
	agent := e.client(cert)

	// NUL num texto: o jsonb não guarda, e a recusa precisa ser definitiva
	// (422) para não travar a fila do agente.
	nul := newSnapshot(testHost, "")
	nul.Software[0].Name = "Chrome\x00lixo"
	b, sum, _ := snapshot.Marshal(nul)
	if status, _, msg := e.send(agent, b, sum); status != http.StatusUnprocessableEntity {
		t.Fatalf("NUL: %d %s", status, msg)
	}

	// O texto "\u0000" (barra, u, zeros) é outra coisa e passa.
	lit := newSnapshot(testHost, "")
	lit.Software[0].Name = `C:\u0000`
	b, sum, _ = snapshot.Marshal(lit)
	if status, _, msg := e.send(agent, b, sum); status != http.StatusAccepted {
		t.Fatalf("\\u0000 literal: %d %s", status, msg)
	}
}
