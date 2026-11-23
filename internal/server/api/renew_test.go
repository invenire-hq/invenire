// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/invenire-hq/invenire/internal/server/store"
	"github.com/invenire-hq/invenire/internal/wire"
	"github.com/invenire-hq/invenire/pkg/snapshot"
)

// Um snapshot datado do futuro travaria o ativo: todos os seguintes chegariam
// "fora de ordem". O servidor recusa de vez (422), e o agente o tira da fila.
func TestFutureSnapshot(t *testing.T) {
	e := newEnv(t)
	_, _, cert := e.enroll(e.token(), testHost)
	agent := e.client(cert)

	future := newSnapshot(testHost, "")
	future.CollectedAt = time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	body, sum, _ := snapshot.Marshal(future)
	status, _, msg := e.send(agent, body, sum)
	if status != http.StatusUnprocessableEntity || !strings.Contains(msg, "relógio") {
		t.Fatalf("snapshot do futuro: %d %s", status, msg)
	}

	// Dentro da folga (fuso mal configurado) passa.
	near := newSnapshot(testHost, "")
	near.CollectedAt = time.Now().Add(3 * time.Hour).UTC().Truncate(time.Second)
	body, sum, _ = snapshot.Marshal(near)
	if status, _, msg := e.send(agent, body, sum); status != http.StatusAccepted {
		t.Fatalf("snapshot 3h à frente: %d %s", status, msg)
	}
}

// Recadastrar o ativo revoga o agente anterior: um ativo, um agente.
func TestReenrollRevokes(t *testing.T) {
	e := newEnv(t)
	_, first, cert1 := e.enroll(e.token(), testHost)
	if status := e.get(e.client(cert1), "/v1/agent", nil); status != http.StatusOK {
		t.Fatalf("primeiro agente: %d", status)
	}
	_, second, cert2 := e.enroll(e.token(), testHost)
	if second.AssetID != first.AssetID {
		t.Fatalf("recadastro mudou de ativo: %+v depois de %+v", second, first)
	}
	if status := e.get(e.client(cert1), "/v1/agent", nil); status != http.StatusUnauthorized {
		t.Fatalf("agente antigo depois do recadastro: %d", status)
	}
	if status := e.get(e.client(cert2), "/v1/agent", nil); status != http.StatusOK {
		t.Fatalf("agente novo: %d", status)
	}

	// Outra máquina do mesmo tenant não é afetada.
	other := snapshot.Host{Hostname: "OUTRA", MachineID: "57cfe8c7-106c-48ba-b00b-3b4b61bd3d87"}
	_, _, cert3 := e.enroll(e.token(), other)
	if status := e.get(e.client(cert2), "/v1/agent", nil); status != http.StatusOK {
		t.Fatalf("agente de outro ativo foi revogado: %d", status)
	}
	if status := e.get(e.client(cert3), "/v1/agent", nil); status != http.StatusOK {
		t.Fatalf("terceiro agente: %d", status)
	}

	changes, _ := e.q.ListChangesAfter(context.Background(), store.ListChangesAfterParams{TenantID: e.tenant.ID, Limit: 20})
	var revoked []float64
	for _, c := range changes {
		if c.Kind == "agent_enrolled" {
			var d struct {
				Revoked float64 `json:"revoked_agents"`
			}
			json.Unmarshal(c.Data, &d)
			revoked = append(revoked, d.Revoked)
		}
	}
	if len(revoked) != 3 || revoked[0] != 0 || revoked[1] != 1 || revoked[2] != 0 {
		t.Fatalf("revoked_agents no change_log: %v", revoked)
	}
}

// renew pede um crachá novo apresentando with, para a mesma chave.
func (e *env) renew(with *tls.Certificate) (int, *tls.Certificate) {
	e.t.Helper()
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, with.PrivateKey.(crypto.Signer))
	if err != nil {
		e.t.Fatal(err)
	}
	body, _ := json.Marshal(wire.RenewRequest{CSR: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))})
	status, b := e.post(e.client(with), wire.PathAgentRenew, body, nil)
	if status != http.StatusOK {
		return status, nil
	}
	var resp wire.RenewResponse
	if err := json.Unmarshal(b, &resp); err != nil {
		e.t.Fatal(err)
	}
	cb, _ := pem.Decode([]byte(resp.Certificate))
	return status, &tls.Certificate{Certificate: [][]byte{cb.Bytes}, PrivateKey: with.PrivateKey}
}

func TestRenew(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, enrolled, a := e.enroll(e.token(), testHost)
	ok := func(name string, c *tls.Certificate, want int) {
		t.Helper()
		var info wire.AgentInfo
		status := e.get(e.client(c), "/v1/agent", &info)
		if status != want || (want == http.StatusOK && info.AgentID != enrolled.AgentID) {
			t.Fatalf("crachá %s: %d %+v, esperava %d", name, status, info, want)
		}
	}

	if status, _ := e.post(e.client(nil), wire.PathAgentRenew, []byte(`{"csr":""}`), nil); status != http.StatusUnauthorized {
		t.Fatalf("renovação sem crachá: %d", status)
	}

	// A → B: os dois valem (a resposta com B pode ter se perdido).
	status, b := e.renew(a)
	if status != http.StatusOK {
		t.Fatalf("renovação: %d", status)
	}
	ok("A", a, http.StatusOK)
	ok("B", b, http.StatusOK)

	// B se perdeu e o agente pede de novo com A: sai C, A continua, B cai.
	_, c := e.renew(a)
	ok("A", a, http.StatusOK)
	ok("C", c, http.StatusOK)
	ok("B", b, http.StatusUnauthorized)

	// Agora com C: sai D, C vira o anterior, A cai.
	_, d := e.renew(c)
	ok("C", c, http.StatusOK)
	ok("D", d, http.StatusOK)
	ok("A", a, http.StatusUnauthorized)

	changes, _ := e.q.ListChangesAfter(ctx, store.ListChangesAfterParams{TenantID: e.tenant.ID, Limit: 20})
	n := 0
	for _, ch := range changes {
		if ch.Kind == "agent_cert_renewed" {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("agent_cert_renewed no change_log: %d", n)
	}

	// Revogar o agente derruba o crachá atual e o anterior.
	if err := e.q.RevokeAgent(ctx, enrolled.AgentID); err != nil {
		t.Fatal(err)
	}
	ok("C", c, http.StatusUnauthorized)
	ok("D", d, http.StatusUnauthorized)
	if status, _ := e.renew(d); status != http.StatusUnauthorized {
		t.Fatalf("renovação de agente revogado: %d", status)
	}
}
