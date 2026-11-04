package ingest

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/athosbes/invenire/internal/server/store"
	"github.com/athosbes/invenire/pkg/snapshot"
)

// entriesOf limpa as entradas de software do snapshot. Entradas sem nome são
// descartadas: não há o que identificar nelas.
func entriesOf(sw []snapshot.Software) []entry {
	out := make([]entry, 0, len(sw))
	for _, s := range sw {
		e := entry{
			raw:     rawKey{source: s.Source, name: clean(s.Name, maxName), vendor: clean(s.Publisher, maxVendor)},
			version: clean(s.Version, maxVersion),
			path:    clean(s.InstallPath, maxPath),
		}
		if e.raw.name != "" {
			out = append(out, e)
		}
	}
	return out
}

// ensureRaws grava os nomes brutos que faltam e devolve o software_id de cada
// um (nil se não identificado). Os ainda sem produto passam pelas regras.
func (w *Worker) ensureRaws(ctx context.Context, q *store.Queries, tenantID uuid.UUID, entries []entry) (map[rawKey]*uuid.UUID, int, error) {
	seen := make(map[rawKey]bool, len(entries))
	var p store.InsertSoftwareRawsParams
	for _, e := range entries {
		if seen[e.raw] {
			continue
		}
		seen[e.raw] = true
		p.Sources = append(p.Sources, e.raw.source)
		p.Names = append(p.Names, e.raw.name)
		p.Vendors = append(p.Vendors, e.raw.vendor)
	}
	out := make(map[rawKey]*uuid.UUID, len(seen))
	if len(seen) == 0 {
		return out, 0, nil
	}
	if err := q.InsertSoftwareRaws(ctx, p); err != nil {
		return nil, 0, err
	}
	rows, err := q.GetSoftwareRaws(ctx, store.GetSoftwareRawsParams(p))
	if err != nil {
		return nil, 0, err
	}

	var normActor *uuid.UUID
	mapped := 0
	for _, r := range rows {
		k := rawKey{r.Source, r.RawName, r.RawVendor}
		out[k] = r.SoftwareID
		if r.SoftwareID != nil || w.normalizer == nil {
			continue
		}
		m, ok := w.normalizer.Normalize(r.Source, r.RawName, r.RawVendor)
		if !ok || clean(m.Product, maxName) == "" {
			continue
		}
		if normActor == nil {
			a, err := q.EnsureSystemActor(ctx, store.EnsureSystemActorParams{TenantID: tenantID, Name: "normalize"})
			if err != nil {
				return nil, 0, err
			}
			normActor = &a.ID
		}
		swID, err := ensureProduct(ctx, q, m)
		if err != nil {
			return nil, 0, err
		}
		conf, rule := m.Confidence, m.Rule
		if err := q.MapSoftwareRaw(ctx, store.MapSoftwareRawParams{
			ID: r.ID, SoftwareID: &swID, Confidence: &conf, MappedBy: normActor, MappedRule: &rule,
		}); err != nil {
			return nil, 0, err
		}
		if err := appendChange(ctx, q, tenantID, "software_mapped", nil, normActor, map[string]any{
			"software_raw_id": r.ID, "software_id": swID, "rule": rule, "confidence": conf,
		}); err != nil {
			return nil, 0, err
		}
		out[k] = &swID
		mapped++
	}
	return out, mapped, nil
}

func ensureProduct(ctx context.Context, q *store.Queries, m Match) (uuid.UUID, error) {
	var vendorID *uuid.UUID
	if v := clean(m.Vendor, maxVendor); v != "" {
		vd, err := q.EnsureVendor(ctx, v)
		if err != nil {
			return uuid.Nil, err
		}
		vendorID = &vd.ID
	}
	sw, err := q.EnsureSoftware(ctx, store.EnsureSoftwareParams{VendorID: vendorID, Product: clean(m.Product, maxName)})
	return sw.ID, err
}

// currentInstallations transforma as entradas identificadas em chaves de
// installation (versão + caminho). Duas entradas que caem na mesma chave
// são a mesma instalação.
func currentInstallations(ctx context.Context, q *store.Queries, entries []entry, raws map[rawKey]*uuid.UUID) (map[instKey]entry, int, error) {
	type svKey struct {
		softwareID uuid.UUID
		version    string
	}
	unmapped := 0
	seen := map[svKey]bool{}
	var p store.InsertSoftwareVersionsParams
	for _, e := range entries {
		sw := raws[e.raw]
		if sw == nil {
			unmapped++
			continue
		}
		k := svKey{*sw, e.version}
		if !seen[k] {
			seen[k] = true
			p.SoftwareIds = append(p.SoftwareIds, k.softwareID)
			p.Versions = append(p.Versions, k.version)
		}
	}
	current := map[instKey]entry{}
	if len(seen) == 0 {
		return current, unmapped, nil
	}
	if err := q.InsertSoftwareVersions(ctx, p); err != nil {
		return nil, 0, err
	}
	rows, err := q.GetSoftwareVersions(ctx, store.GetSoftwareVersionsParams(p))
	if err != nil {
		return nil, 0, err
	}
	versions := make(map[svKey]uuid.UUID, len(rows))
	for _, r := range rows {
		versions[svKey{r.SoftwareID, r.Version}] = r.ID
	}
	for _, e := range entries {
		if sw := raws[e.raw]; sw != nil {
			current[instKey{versions[svKey{*sw, e.version}], e.path}] = e
		}
	}
	return current, unmapped, nil
}

func softwareFailed(s *snapshot.Snapshot) bool {
	for _, e := range s.CollectorErrors {
		if e.Collector == "software" {
			return true
		}
	}
	return false
}

// updateAsset grava os atributos do snapshot no ativo e registra o que mudou.
func updateAsset(ctx context.Context, q *store.Queries, a store.Asset, s *snapshot.Snapshot, collectedAt time.Time, change func(string, any) error) error {
	p := store.UpdateAssetFromSnapshotParams{
		ID:           a.ID,
		Hostname:     cleanPtr(s.Host.Hostname, maxAttr),
		Serial:       cleanPtr(s.Host.Serial, maxAttr),
		Manufacturer: cleanPtr(s.Host.Manufacturer, maxAttr),
		Model:        cleanPtr(s.Host.Model, maxAttr),
		OsFamily:     cleanPtr(s.OS.Family, maxAttr),
		OsName:       cleanPtr(s.OS.Name, maxAttr),
		OsVersion:    cleanPtr(s.OS.Version, maxAttr),
		CollectedAt:  collectedAt,
	}
	diff := map[string]any{}
	for _, f := range []struct {
		name     string
		old, new *string
	}{
		{"hostname", a.Hostname, p.Hostname},
		{"serial", a.Serial, p.Serial},
		{"manufacturer", a.Manufacturer, p.Manufacturer},
		{"model", a.Model, p.Model},
		{"os_family", a.OsFamily, p.OsFamily},
		{"os_name", a.OsName, p.OsName},
		{"os_version", a.OsVersion, p.OsVersion},
	} {
		if deref(f.old) != deref(f.new) {
			diff[f.name] = map[string]*string{"old": f.old, "new": f.new}
		}
	}
	if err := q.UpdateAssetFromSnapshot(ctx, p); err != nil {
		return err
	}
	if len(diff) == 0 {
		return nil
	}
	return change("asset_changed", diff)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func appendChange(ctx context.Context, q *store.Queries, tenantID uuid.UUID, kind string, assetID, actorID *uuid.UUID, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = q.AppendChange(ctx, store.AppendChangeParams{TenantID: tenantID, Kind: kind, AssetID: assetID, ActorID: actorID, Data: b})
	return err
}
