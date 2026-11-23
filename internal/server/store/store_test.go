// SPDX-License-Identifier: AGPL-3.0-only

package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/invenire-hq/invenire/internal/server/store"
	"github.com/invenire-hq/invenire/internal/testdb"
)

func setup(t *testing.T) (*store.Queries, *pgxpool.Pool) {
	pool := testdb.New(t)
	return store.New(pool), pool
}

func ptr[T any](v T) *T { return &v }

func TestInventoryFlow(t *testing.T) {
	q, _ := setup(t)
	ctx := context.Background()

	tenant, err := q.CreateTenant(ctx, "Organização de Teste")
	if err != nil {
		t.Fatal(err)
	}

	assetParams := store.UpsertAssetParams{
		TenantID:    tenant.ID,
		Fingerprint: "smbios:081b0196-a1a3-65a2-8d37-08626699ac78",
		Hostname:    ptr("PMC-ARR-01"),
		OsFamily:    ptr("windows"),
	}
	a1, err := q.UpsertAsset(ctx, assetParams)
	if err != nil || !a1.Inserted {
		t.Fatalf("primeiro upsert: %+v %v", a1, err)
	}
	assetParams.Hostname = ptr("PMC-ARR-02")
	a2, err := q.UpsertAsset(ctx, assetParams)
	if err != nil || a2.Inserted || a2.ID != a1.ID || *a2.Hostname != "PMC-ARR-02" {
		t.Fatalf("segundo upsert devia atualizar o mesmo ativo: %+v %v", a2, err)
	}

	agent, err := q.CreateAgent(ctx, store.CreateAgentParams{ID: uuid.New(), AssetID: a1.ID, CertFingerprint: "cert1", AgentVersion: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"schema_version":"1"}`)
	sum := sha256.Sum256(body)
	snapParams := store.InsertSnapshotParams{
		AssetID: a1.ID, AgentID: agent.ID, SchemaVersion: "1",
		CollectedAt: time.Now(), Sha256: sum[:], Payload: body, Body: body,
	}
	s1, err := q.InsertSnapshot(ctx, snapParams)
	if err != nil || !s1.Inserted {
		t.Fatalf("snapshot: %+v %v", s1, err)
	}
	// body precisa bater com sha256: o banco recusa bytes adulterados.
	forged := snapParams
	forged.Sha256 = make([]byte, 32)
	if _, err := q.InsertSnapshot(ctx, forged); err == nil {
		t.Fatal("aceitou body que não bate com o sha256")
	}
	if got, err := q.GetSnapshotBody(ctx, s1.ID); err != nil || string(got) != string(body) {
		t.Fatalf("body guardado: %q %v", got, err)
	}
	again, err := q.InsertSnapshot(ctx, snapParams)
	if err != nil || again.Inserted || again.ID != s1.ID {
		t.Fatalf("reenvio devia devolver o mesmo snapshot: %+v %v", again, err)
	}
	if last, err := q.GetLastSnapshotHash(ctx, a1.ID); err != nil || string(last) != string(sum[:]) {
		t.Fatalf("último hash: %x %v", last, err)
	}
	claimed, err := q.ClaimNextSnapshot(ctx)
	if err != nil || claimed.ID != s1.ID {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	if err := q.FinishSnapshot(ctx, store.FinishSnapshotParams{ID: s1.ID}); err != nil {
		t.Fatal(err)
	}

	// Nome bruto entra sem produto e aparece na lista de não identificados.
	raw, err := q.EnsureSoftwareRaw(ctx, store.EnsureSoftwareRawParams{Source: "win_registry", RawName: "Google Chrome", RawVendor: "Google LLC"})
	if err != nil {
		t.Fatal(err)
	}
	unmapped, err := q.ListUnmappedSoftware(ctx, store.ListUnmappedSoftwareParams{PageSize: 10})
	if err != nil || len(unmapped) != 1 || unmapped[0].ID != raw.ID {
		t.Fatalf("não identificados: %+v %v", unmapped, err)
	}

	sys, err := q.EnsureSystemActor(ctx, store.EnsureSystemActorParams{TenantID: tenant.ID, Name: "normalize"})
	if err != nil {
		t.Fatal(err)
	}
	vendor, err := q.EnsureVendor(ctx, store.EnsureVendorParams{Name: "Google"})
	if err != nil {
		t.Fatal(err)
	}
	sw, err := q.EnsureSoftware(ctx, store.EnsureSoftwareParams{VendorID: &vendor.ID, Product: "Chrome"})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.MapSoftwareRaw(ctx, store.MapSoftwareRawParams{
		ID: raw.ID, SoftwareID: &sw.ID, Confidence: ptr[float32](0.95), MappedBy: &sys.ID, MappedRule: ptr("exact_name"),
	}); err != nil {
		t.Fatal(err)
	}
	if unmapped, _ := q.ListUnmappedSoftware(ctx, store.ListUnmappedSoftwareParams{PageSize: 10}); len(unmapped) != 0 {
		t.Fatalf("mapeado continua na lista: %+v", unmapped)
	}

	// Produto sem fabricante não pode se repetir.
	n1, err := q.EnsureSoftware(ctx, store.EnsureSoftwareParams{Product: "sem-fabricante"})
	if err != nil {
		t.Fatal(err)
	}
	n2, err := q.EnsureSoftware(ctx, store.EnsureSoftwareParams{Product: "sem-fabricante"})
	if err != nil || n2.ID != n1.ID {
		t.Fatalf("produto sem fabricante duplicou: %v %v %v", n1.ID, n2.ID, err)
	}

	sv, err := q.EnsureSoftwareVersion(ctx, store.EnsureSoftwareVersionParams{SoftwareID: sw.ID, Version: "130.0.6723.70"})
	if err != nil {
		t.Fatal(err)
	}

	// Instalação: abre, é vista de novo, é removida e reinstalada.
	inst := store.OpenInstallationParams{AssetID: a1.ID, SoftwareVersionID: sv.ID, InstallPath: `C:\Program Files\Google\Chrome`, FirstSnapshotID: s1.ID}
	i1, err := q.OpenInstallation(ctx, inst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.OpenInstallation(ctx, inst); !isUniqueViolation(err) {
		t.Fatalf("duas instalações abertas iguais: %v", err)
	}
	if err := q.TouchInstallations(ctx, store.TouchInstallationsParams{LastSnapshotID: s1.ID, Ids: []uuid.UUID{i1}}); err != nil {
		t.Fatal(err)
	}
	if err := q.CloseInstallations(ctx, store.CloseInstallationsParams{RemovedSnapshotID: &s1.ID, Ids: []uuid.UUID{i1}}); err != nil {
		t.Fatal(err)
	}
	if open, _ := q.ListOpenInstallations(ctx, a1.ID); len(open) != 0 {
		t.Fatalf("instalação removida continua aberta: %+v", open)
	}
	if _, err := q.OpenInstallation(ctx, inst); err != nil {
		t.Fatalf("reinstalação: %v", err)
	}
	where, err := q.FindSoftwareVersionInstallations(ctx, store.FindSoftwareVersionInstallationsParams{SoftwareVersionID: sv.ID, TenantID: tenant.ID})
	if err != nil || len(where) != 1 || where[0].AssetID != a1.ID {
		t.Fatalf("find_software: %+v %v", where, err)
	}

	if err := q.RevokeAgent(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetActiveAgentByCert(ctx, "cert1"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("agente revogado ainda autentica: %v", err)
	}
}

func TestProposalFlow(t *testing.T) {
	q, _ := setup(t)
	ctx := context.Background()

	tenant, err := q.CreateTenant(ctx, "t")
	if err != nil {
		t.Fatal(err)
	}
	bot, err := q.CreateActor(ctx, store.CreateActorParams{TenantID: tenant.ID, Kind: "ai_agent", Name: "triagem", Scopes: []string{"read", "propose"}})
	if err != nil {
		t.Fatal(err)
	}
	human, err := q.CreateActor(ctx, store.CreateActorParams{TenantID: tenant.ID, Kind: "human", Name: "ana", Scopes: []string{"admin"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateActor(ctx, store.CreateActorParams{TenantID: tenant.ID, Kind: "robot", Name: "x", Scopes: []string{}}); err == nil {
		t.Fatal("kind inválido foi aceito")
	}

	p, err := q.CreateProposal(ctx, store.CreateProposalParams{
		TenantID: tenant.ID, ActorID: bot.ID, Kind: "software_mapping",
		TargetType: "software_raw", TargetID: uuid.NewString(),
		Payload: json.RawMessage(`{"software_id":"x"}`), Evidence: "nome e fabricante batem com o produto", Confidence: ptr[float32](0.8),
	})
	if err != nil || p.Status != "pending" {
		t.Fatalf("proposta: %+v %v", p, err)
	}
	pending, err := q.ListPendingProposals(ctx, store.ListPendingProposalsParams{TenantID: tenant.ID, Limit: 10, Kind: ptr("software_mapping")})
	if err != nil || len(pending) != 1 {
		t.Fatalf("pendentes: %+v %v", pending, err)
	}

	before, err := q.LatestChangeID(ctx, tenant.ID)
	if err != nil || before != 0 {
		t.Fatalf("change_log vazio: %d %v", before, err)
	}
	reviewed, err := q.ReviewProposal(ctx, store.ReviewProposalParams{ID: p.ID, Status: "approved", ReviewedBy: &human.ID})
	if err != nil || reviewed.ReviewedAt == nil {
		t.Fatalf("revisão: %+v %v", reviewed, err)
	}
	if _, err := q.ReviewProposal(ctx, store.ReviewProposalParams{ID: p.ID, Status: "rejected", ReviewedBy: &human.ID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("proposta decidida duas vezes: %v", err)
	}

	id, err := q.AppendChange(ctx, store.AppendChangeParams{
		TenantID: tenant.ID, Kind: "proposal_reviewed", ActorID: &human.ID,
		Data: json.RawMessage(`{"proposal_id":"` + p.ID.String() + `"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	changes, err := q.ListChangesAfter(ctx, store.ListChangesAfterParams{TenantID: tenant.ID, ID: before, Limit: 10})
	if err != nil || len(changes) != 1 || changes[0].ID != id {
		t.Fatalf("changes: %+v %v", changes, err)
	}
	if asOf, _ := q.LatestChangeID(ctx, tenant.ID); asOf != id {
		t.Fatalf("as_of %d, esperava %d", asOf, id)
	}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
