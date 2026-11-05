package ingest_test

import (
	"context"
	"testing"

	"github.com/athosbes/invenire/internal/server/ingest"
	"github.com/athosbes/invenire/internal/server/normalize"
	"github.com/athosbes/invenire/internal/server/store"
	"github.com/athosbes/invenire/pkg/snapshot"
)

// none é um conjunto de regras que não reconhece nada.
type none struct{}

func (none) Normalize(string, string, string) (normalize.Match, bool) {
	return normalize.Match{}, false
}

func (f *fixture) unmapped(t *testing.T) []store.ListUnmappedForTenantRow {
	t.Helper()
	rows, err := f.q.ListUnmappedForTenant(context.Background(), store.ListUnmappedForTenantParams{TenantID: f.asset.TenantID, PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// Mapear à mão um nome que já está nos ativos recalcula as instalações deles
// sem esperar o próximo snapshot.
func TestManualMappingRemaps(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	w := ingest.New(f.pool, quiet, nil)

	f.put(t, 0, []snapshot.Software{notepad})
	if res := f.next(t, w); res.Installed != 0 || res.Unmapped != 1 {
		t.Fatalf("snapshot: %+v", res)
	}
	un := f.unmapped(t)
	if len(un) != 1 || un[0].AssetCount != 1 || un[0].RawName != notepad.Name {
		t.Fatalf("não identificados: %+v", un)
	}

	sw, err := f.q.EnsureSoftware(ctx, store.EnsureSoftwareParams{Product: "Notepad++"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.q.MapSoftwareRaw(ctx, store.MapSoftwareRawParams{ID: un[0].ID, SoftwareID: &sw.ID}); err != nil {
		t.Fatal(err)
	}
	if n, err := f.q.MarkAssetsForRemap(ctx, un[0].ID); err != nil || n != 1 {
		t.Fatalf("marcar: %d %v", n, err)
	}
	res, ok, err := w.ProcessRemap(ctx)
	if err != nil || !ok || !res.Remap || res.Installed != 1 {
		t.Fatalf("reaplicar: %+v ok=%v err=%v", res, ok, err)
	}
	if got := f.installed(t); len(got) != 1 || got[0] != "Notepad++ 8.6.9" {
		t.Fatalf("instalado: %q", got)
	}
	if _, ok, _ := w.ProcessRemap(ctx); ok {
		t.Fatal("reaplicação devia ter limpado remap_pending")
	}
	if un := f.unmapped(t); len(un) != 0 {
		t.Fatalf("ainda não identificado: %+v", un)
	}
}

// Regra nova vale para o que já foi ingerido; regra retirada desfaz o que
// ela tinha mapeado.
func TestRemapAllFollowsRules(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.put(t, 0, []snapshot.Software{chrome("120.0"), notepad})
	if res := f.next(t, ingest.New(f.pool, quiet, nil)); res.Installed != 0 {
		t.Fatalf("sem regras: %+v", res)
	}

	w := ingest.New(f.pool, quiet, rules{})
	if n, err := w.RemapAll(ctx); err != nil || n != 1 {
		t.Fatalf("RemapAll: %d %v", n, err)
	}
	if n, err := w.Drain(ctx); err != nil || n != 1 {
		t.Fatalf("drain: %d %v", n, err)
	}
	if got := f.installed(t); len(got) != 1 || got[0] != `Chrome 120.0 C:\Program Files\Google\Chrome\Application` {
		t.Fatalf("instalado: %q", got)
	}
	if n, err := w.RemapAll(ctx); err != nil || n != 0 {
		t.Fatalf("RemapAll repetido mudou algo: %d %v", n, err)
	}

	w = ingest.New(f.pool, quiet, none{})
	if n, err := w.RemapAll(ctx); err != nil || n != 1 {
		t.Fatalf("RemapAll sem a regra: %d %v", n, err)
	}
	if _, err := w.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.installed(t); len(got) != 0 {
		t.Fatalf("regra retirada, instalação devia fechar: %q", got)
	}
	if got := f.changes(t); got["software_mapped"] != 1 || got["software_unmapped"] != 1 || got["software_removed"] != 1 {
		t.Fatalf("change_log: %v", got)
	}
}

// Pacote Linux vira produto pelo nome e leva o PURL na versão.
func TestLinuxPackagesGetPURL(t *testing.T) {
	f := newFixture(t)
	rulesDefault, err := normalize.Default()
	if err != nil {
		t.Fatal(err)
	}
	w := ingest.New(f.pool, quiet, rulesDefault)

	s := snapshot.New("test", f.t0)
	s.Host = snapshot.Host{Hostname: "srv", SMBIOSUUID: "081B0196-A1A3-65A2-8D37-08626699AC78"}
	s.OS = snapshot.OS{Family: "linux", Name: "Ubuntu 24.04", ID: "ubuntu"}
	s.Software = []snapshot.Software{
		{Source: "dpkg", Name: "curl", Version: "8.5.0-2ubuntu10.4"},
		{Source: "dpkg", Name: "libssl3t64", Version: "3.0.13-0ubuntu3.4"},
		{Source: "dpkg", Name: "vim", Version: "2:9.1.0016-1ubuntu7+esm1"},
	}
	body, _, err := snapshot.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	f.putRaw(t, f.t0, body)
	if res := f.next(t, w); res.Installed != 3 || res.Unmapped != 0 {
		t.Fatalf("snapshot: %+v", res)
	}
	want := map[string]string{
		"8.5.0-2ubuntu10.4":        "pkg:deb/ubuntu/curl@8.5.0-2ubuntu10.4",
		"3.0.13-0ubuntu3.4":        "pkg:deb/ubuntu/libssl3t64@3.0.13-0ubuntu3.4",
		"2:9.1.0016-1ubuntu7+esm1": "pkg:deb/ubuntu/vim@2%3A9.1.0016-1ubuntu7%2Besm1",
	}
	for version, p := range want {
		var got *string
		if err := f.pool.QueryRow(context.Background(), "SELECT purl FROM software_version WHERE version = $1", version).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got == nil || *got != p {
			t.Errorf("purl de %s: %v, esperado %s", version, got, p)
		}
	}
	if got := f.installed(t); len(got) != 3 || got[0] != "OpenSSL 3.0.13-0ubuntu3.4" {
		t.Fatalf("instalado: %q", got)
	}
}
