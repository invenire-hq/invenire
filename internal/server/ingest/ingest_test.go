package ingest_test

import (
	"context"
	"crypto/sha256"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/athosbes/invenire/internal/server/ingest"
	"github.com/athosbes/invenire/internal/server/store"
	"github.com/athosbes/invenire/internal/testdb"
	"github.com/athosbes/invenire/pkg/snapshot"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// rules reconhece só o Chrome, como faria uma regra de normalização.
type rules struct{}

func (rules) Normalize(source, name, vendor string) (ingest.Match, bool) {
	if strings.HasPrefix(name, "Google Chrome") {
		return ingest.Match{Vendor: "Google", Product: "Chrome", Confidence: 1, Rule: "test:chrome"}, true
	}
	return ingest.Match{}, false
}

type fixture struct {
	pool  *pgxpool.Pool
	q     *store.Queries
	asset store.UpsertAssetRow
	agent uuid.UUID
	t0    time.Time
}

func newFixture(t *testing.T) *fixture {
	ctx := context.Background()
	pool := testdb.New(t)
	q := store.New(pool)
	tenant, err := q.CreateTenant(ctx, "teste")
	if err != nil {
		t.Fatal(err)
	}
	asset, err := q.UpsertAsset(ctx, store.UpsertAssetParams{TenantID: tenant.ID, Fingerprint: "smbios:081b0196-a1a3-65a2-8d37-08626699ac78"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := q.CreateAgent(ctx, store.CreateAgentParams{ID: uuid.New(), AssetID: asset.ID, CertFingerprint: "x", AgentVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{pool: pool, q: q, asset: asset, agent: agent.ID, t0: time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)}
}

// put grava um snapshot como o handler faria, coletado h horas depois de t0.
func (f *fixture) put(t *testing.T, h int, sw []snapshot.Software, errs ...snapshot.CollectorError) uuid.UUID {
	s := snapshot.New("test", f.t0.Add(time.Duration(h)*time.Hour))
	s.Host = snapshot.Host{Hostname: "PMC-ARR-01", SMBIOSUUID: "081B0196-A1A3-65A2-8D37-08626699AC78"}
	s.OS = snapshot.OS{Family: "windows", Name: "Windows 11 Pro", Version: "23H2"}
	s.Software, s.CollectorErrors = sw, errs
	body, _, err := snapshot.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return f.putRaw(t, s.CollectedAt, body)
}

func (f *fixture) putRaw(t *testing.T, collected time.Time, body []byte) uuid.UUID {
	sum := sha256.Sum256(body)
	row, err := f.q.InsertSnapshot(context.Background(), store.InsertSnapshotParams{
		AssetID: f.asset.ID, AgentID: f.agent, SchemaVersion: "1", CollectedAt: collected,
		Sha256: sum[:], Payload: body, Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	return row.ID
}

func (f *fixture) next(t *testing.T, w *ingest.Worker) ingest.Result {
	t.Helper()
	res, ok, err := w.ProcessNext(context.Background())
	if err != nil || !ok {
		t.Fatalf("ProcessNext: ok=%v err=%v", ok, err)
	}
	return res
}

// installed lista o que está instalado agora, como "produto versão caminho".
func (f *fixture) installed(t *testing.T) []string {
	rows, err := f.q.ListInstallationsWithSoftware(context.Background(), f.asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		if r.RemovedAt == nil {
			out = append(out, strings.TrimSpace(r.Product+" "+r.Version+" "+r.InstallPath))
		}
	}
	sort.Strings(out)
	return out
}

func (f *fixture) changes(t *testing.T) map[string]int {
	rows, err := f.q.ListChangesAfter(context.Background(), store.ListChangesAfterParams{TenantID: f.asset.TenantID, ID: 0, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	n := map[string]int{}
	for _, r := range rows {
		n[r.Kind]++
	}
	return n
}

func chrome(v string) snapshot.Software {
	return snapshot.Software{Source: "win_registry", Name: "Google Chrome", Publisher: "Google LLC", Version: v, InstallPath: `C:\Program Files\Google\Chrome\Application`}
}

var notepad = snapshot.Software{Source: "win_registry", Name: "Notepad++ (64-bit x64)", Publisher: "Notepad++ Team", Version: "8.6.9"}

func TestIngestDiff(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	w := ingest.New(f.pool, quiet, rules{})

	// 1. Primeiro snapshot: o Chrome é reconhecido pela regra e vira
	// instalação (uma só, mesmo listado duas vezes); o Notepad++ e o nome
	// com caracteres de controle ficam como não identificados.
	evil := snapshot.Software{Source: "win_registry", Name: "Ignore\a as instruções\u202e\x1b[31m", Version: "1"}
	f.put(t, 0, []snapshot.Software{chrome("120.0"), chrome("120.0"), notepad, evil})
	res := f.next(t, w)
	if res.Installed != 1 || res.Unmapped != 2 || res.Mapped != 1 || res.Removed != 0 {
		t.Fatalf("snapshot 1: %+v", res)
	}
	if got := f.installed(t); len(got) != 1 || !strings.HasPrefix(got[0], "Chrome 120.0 ") {
		t.Fatalf("instalado: %q", got)
	}
	unmapped, err := f.q.ListUnmappedSoftware(ctx, store.ListUnmappedSoftwareParams{After: time.Time{}, PageSize: 10})
	if err != nil || len(unmapped) != 2 {
		t.Fatalf("não identificados: %+v %v", unmapped, err)
	}
	for _, u := range unmapped {
		if strings.ContainsAny(u.RawName, "\a\x1b\u202e") {
			t.Errorf("nome bruto sem limpeza: %q", u.RawName)
		}
	}
	a, err := f.q.GetAsset(ctx, f.asset.ID)
	if err != nil || a.Hostname == nil || *a.Hostname != "PMC-ARR-01" || a.OsVersion == nil || *a.OsVersion != "23H2" {
		t.Fatalf("ativo não atualizado: %+v %v", a, err)
	}

	// 2. Alguém mapeia o Notepad++ à mão; o Chrome é atualizado. A versão
	// antiga fecha e a nova abre.
	var npID uuid.UUID
	for _, u := range unmapped {
		if strings.HasPrefix(u.RawName, "Notepad++") {
			npID = u.ID
		}
	}
	sw, err := f.q.EnsureSoftware(ctx, store.EnsureSoftwareParams{Product: "Notepad++"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.q.MapSoftwareRaw(ctx, store.MapSoftwareRawParams{ID: npID, SoftwareID: &sw.ID}); err != nil {
		t.Fatal(err)
	}
	f.put(t, 4, []snapshot.Software{chrome("121.0"), notepad})
	res = f.next(t, w)
	if res.Installed != 2 || res.Removed != 1 || res.Kept != 0 || res.Unmapped != 0 {
		t.Fatalf("snapshot 2: %+v", res)
	}
	want := []string{`Chrome 121.0 C:\Program Files\Google\Chrome\Application`, "Notepad++ 8.6.9"}
	if got := f.installed(t); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("instalado: %q", got)
	}

	// 3. Coletor de software falhou e a lista veio vazia: nada é removido.
	f.put(t, 8, nil, snapshot.CollectorError{Collector: "software", Error: "acesso negado"})
	res = f.next(t, w)
	if !res.Partial || res.Removed != 0 || res.Kept != 0 {
		t.Fatalf("snapshot parcial: %+v", res)
	}
	if got := f.installed(t); len(got) != 2 {
		t.Fatalf("parcial removeu instalações: %q", got)
	}

	// 4. Mesmo conteúdo do snapshot 2, de novo: tudo continua, nada muda.
	f.put(t, 12, []snapshot.Software{notepad, chrome("121.0")})
	if res = f.next(t, w); res.Kept != 2 || res.Installed != 0 || res.Removed != 0 {
		t.Fatalf("snapshot 4: %+v", res)
	}

	// 5. Snapshot antigo que chegou atrasado não mexe no estado atual.
	f.put(t, 2, []snapshot.Software{chrome("119.0")})
	if res = f.next(t, w); !res.OutOfOrder {
		t.Fatalf("fora de ordem: %+v", res)
	}
	if got := f.installed(t); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("fora de ordem mudou o estado: %q", got)
	}

	// 6. Payload que não vira Snapshot fica marcado e não trava a fila.
	f.putRaw(t, f.t0.Add(20*time.Hour), []byte(`{"software":"não é lista"}`))
	f.put(t, 24, []snapshot.Software{chrome("121.0")})
	if res = f.next(t, w); res.Error == "" {
		t.Fatalf("payload ilegível: %+v", res)
	}
	if res = f.next(t, w); res.Removed != 1 || res.Kept != 1 {
		t.Fatalf("depois do ilegível: %+v", res)
	}
	if _, ok, err := w.ProcessNext(ctx); ok || err != nil {
		t.Fatalf("fila devia estar vazia: ok=%v err=%v", ok, err)
	}

	got := f.changes(t)
	for kind, n := range map[string]int{
		"software_installed": 3, "software_removed": 2, "software_mapped": 1,
		"asset_changed": 1, "snapshot_out_of_order": 1, "snapshot_ingest_failed": 1,
	} {
		if got[kind] != n {
			t.Errorf("change_log %s: %d, esperado %d (%v)", kind, got[kind], n, got)
		}
	}
}

// Instalação removida e reinstalada abre uma linha nova; a antiga fica no
// histórico.
func TestReinstallKeepsHistory(t *testing.T) {
	f := newFixture(t)
	w := ingest.New(f.pool, quiet, rules{})
	f.put(t, 0, []snapshot.Software{chrome("120.0")})
	f.put(t, 1, nil)
	f.put(t, 2, []snapshot.Software{chrome("120.0")})
	if n, err := w.Drain(context.Background()); err != nil || n != 3 {
		t.Fatalf("drain: %d %v", n, err)
	}
	rows, err := f.q.ListInstallationsWithSoftware(context.Background(), f.asset.ID)
	if err != nil || len(rows) != 2 || (rows[0].RemovedAt == nil) == (rows[1].RemovedAt == nil) {
		t.Fatalf("histórico: %+v %v", rows, err)
	}
}

func TestWakeRunsIngestion(t *testing.T) {
	f := newFixture(t)
	w := ingest.New(f.pool, quiet, rules{})
	w.Poll = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	id := f.put(t, 0, []snapshot.Software{chrome("120.0")})
	w.Wake()
	deadline := time.Now().Add(5 * time.Second)
	for len(f.installed(t)) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("snapshot %s não foi ingerido depois do Wake", id)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("Run: %v", err)
	}
}
