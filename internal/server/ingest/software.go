package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/athosbes/invenire/internal/server/normalize"
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

// ensureRaws grava os nomes brutos que faltam e devolve o id e o produto de
// cada um para o tenant: o mapeamento manual dele, se houver, senão o da
// regra. Os ainda sem regra passam pelas regras; um nome mapeado agora marca
// para reaplicação os outros ativos que já o tinham.
func (w *Worker) ensureRaws(ctx context.Context, q *store.Queries, tenantID uuid.UUID, entries []entry) (map[rawKey]rawInfo, int, error) {
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
	out := make(map[rawKey]rawInfo, len(seen))
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
		out[k] = rawInfo{id: r.ID, software: r.SoftwareID}
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
		swID, err := mapRaw(ctx, q, r.ID, m, normActor)
		if err != nil {
			return nil, 0, err
		}
		if err := appendChange(ctx, q, tenantID, "software_mapped", nil, normActor, mappingData(r.ID, swID, m)); err != nil {
			return nil, 0, err
		}
		out[k] = rawInfo{id: r.ID, software: &swID}
		mapped++
	}

	ids := make([]uuid.UUID, 0, len(rows))
	byID := make(map[uuid.UUID]rawKey, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
		byID[r.ID] = rawKey{r.Source, r.RawName, r.RawVendor}
	}
	manual, err := q.GetTenantMappings(ctx, store.GetTenantMappingsParams{TenantID: tenantID, RawIds: ids})
	if err != nil {
		return nil, 0, err
	}
	for _, m := range manual {
		k := byID[m.SoftwareRawID]
		out[k] = rawInfo{id: m.SoftwareRawID, software: &m.SoftwareID}
	}
	return out, mapped, nil
}

// mapRaw liga o nome bruto ao produto da regra e marca para reaplicação os
// ativos que têm o nome.
func mapRaw(ctx context.Context, q *store.Queries, rawID uuid.UUID, m normalize.Match, by *uuid.UUID) (uuid.UUID, error) {
	swID, _, err := ensureProduct(ctx, q, nil, m.Vendor, m.Product)
	if err != nil {
		return uuid.Nil, err
	}
	conf, rule := m.Confidence, m.Rule
	if err := q.MapSoftwareRaw(ctx, store.MapSoftwareRawParams{
		ID: rawID, SoftwareID: &swID, Confidence: &conf, MappedBy: by, MappedRule: &rule,
	}); err != nil {
		return uuid.Nil, err
	}
	_, err = q.MarkAssetsForRemap(ctx, rawID)
	return swID, err
}

func mappingData(rawID, swID uuid.UUID, m normalize.Match) map[string]any {
	return map[string]any{
		"software_raw_id": rawID, "software_id": swID, "vendor": m.Vendor, "product": m.Product,
		"rule": m.Rule, "confidence": m.Confidence,
	}
}

// ensureProduct devolve o produto, criando fabricante e produto se faltarem.
// Com tenant nulo (regras), tudo fica no catálogo comum. Com tenant
// (mapeamento manual), um produto ou fabricante que já está no catálogo
// comum é reaproveitado, e o que faltar é criado só para o tenant: o nome de
// um sistema interno não aparece para os outros. private diz se o produto
// é do tenant. Procura antes de criar: os Ensure* escrevem mesmo quando a
// linha existe.
func ensureProduct(ctx context.Context, q *store.Queries, tenant *uuid.UUID, vendor, product string) (id uuid.UUID, private bool, err error) {
	vendor, product = clean(vendor, maxVendor), clean(product, maxName)
	if product == "" {
		return uuid.Nil, false, errors.New("produto vazio")
	}
	var vp *string
	if vendor != "" {
		vp = &vendor
	}
	found, err := q.FindSoftware(ctx, store.FindSoftwareParams{Product: product, Vendor: vp, TenantID: tenant})
	if err == nil || !errors.Is(err, pgx.ErrNoRows) {
		return found.ID, found.TenantID != nil, err
	}
	var vendorID *uuid.UUID
	if vp != nil {
		v, err := q.FindVendor(ctx, store.FindVendorParams{Name: vendor, TenantID: tenant})
		if errors.Is(err, pgx.ErrNoRows) {
			var vd store.Vendor
			vd, err = q.EnsureVendor(ctx, store.EnsureVendorParams{TenantID: tenant, Name: vendor})
			v = vd.ID
		}
		if err != nil {
			return uuid.Nil, false, err
		}
		vendorID = &v
	}
	sw, err := q.EnsureSoftware(ctx, store.EnsureSoftwareParams{TenantID: tenant, VendorID: vendorID, Product: product})
	return sw.ID, tenant != nil, err
}

// EnsureProduct é ensureProduct para o mapeamento manual da API.
func EnsureProduct(ctx context.Context, q *store.Queries, tenant uuid.UUID, vendor, product string) (id uuid.UUID, private bool, err error) {
	return ensureProduct(ctx, q, &tenant, vendor, product)
}

// currentInstallations transforma as entradas identificadas em chaves de
// installation (versão + caminho). Duas entradas que caem na mesma chave
// são a mesma instalação. Pacotes Linux levam o PURL na versão.
func currentInstallations(ctx context.Context, q *store.Queries, entries []entry, raws map[rawKey]rawInfo, osID string) (map[instKey]entry, int, error) {
	type svKey struct {
		softwareID uuid.UUID
		version    string
	}
	unmapped := 0
	seen := map[svKey]bool{}
	var p store.InsertSoftwareVersionsParams
	for _, e := range entries {
		sw := raws[e.raw].software
		if sw == nil {
			unmapped++
			continue
		}
		k := svKey{*sw, e.version}
		if !seen[k] {
			seen[k] = true
			p.SoftwareIds = append(p.SoftwareIds, k.softwareID)
			p.Versions = append(p.Versions, k.version)
			p.Purls = append(p.Purls, purl(e, osID))
		}
	}
	current := map[instKey]entry{}
	if len(seen) == 0 {
		return current, unmapped, nil
	}
	if err := q.InsertSoftwareVersions(ctx, p); err != nil {
		return nil, 0, err
	}
	// Um PURL já usado por outra versão (o pacote mudou de produto) faz a
	// linha não entrar; de novo sem PURL, ela entra.
	p.Purls = make([]string, len(p.Purls))
	if err := q.InsertSoftwareVersions(ctx, p); err != nil {
		return nil, 0, err
	}
	rows, err := q.GetSoftwareVersions(ctx, store.GetSoftwareVersionsParams{SoftwareIds: p.SoftwareIds, Versions: p.Versions})
	if err != nil {
		return nil, 0, err
	}
	versions := make(map[svKey]uuid.UUID, len(rows))
	for _, r := range rows {
		versions[svKey{r.SoftwareID, r.Version}] = r.ID
	}
	for _, e := range entries {
		if sw := raws[e.raw].software; sw != nil {
			current[instKey{versions[svKey{*sw, e.version}], e.path}] = e
		}
	}
	return current, unmapped, nil
}

// purl monta o Package URL de um pacote Linux: pkg:deb/ubuntu/curl@8.5.0-2ubuntu10.
// Vazio para outras fontes ou sem distribuição e versão.
func purl(e entry, osID string) string {
	var typ string
	switch e.raw.source {
	case snapshot.SourceDpkg:
		typ = "deb"
	case snapshot.SourceRpm:
		typ = "rpm"
	default:
		return ""
	}
	osID = strings.ToLower(clean(osID, 64))
	if osID == "" || e.version == "" {
		return ""
	}
	version, qualifiers := e.version, ""
	// No rpm a época vai como qualificador; no deb ela faz parte da versão.
	if typ == "rpm" {
		if epoch, rest, ok := strings.Cut(version, ":"); ok {
			version, qualifiers = rest, "?epoch="+purlEscape(epoch)
		}
	}
	return "pkg:" + typ + "/" + purlEscape(osID) + "/" + purlEscape(strings.ToLower(e.raw.name)) + "@" + purlEscape(version) + qualifiers
}

// purlEscape codifica tudo fora de [A-Za-z0-9._~-] (inclusive ':' e '+').
func purlEscape(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '~' || c == '-' {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
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
func updateAsset(ctx context.Context, q *store.Queries, a store.Asset, s *snapshot.Snapshot, collectedAt time.Time, change changeLogger) error {
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
