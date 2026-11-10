package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/athosbes/invenire/internal/server/store"
)

// Rotas de consulta. Todas leem num só instante (leitura repetível) e
// devolvem as_of, o último change_log.id que a resposta considera. O que foi
// escrito na máquina inventariada (hostname, modelo, versão, caminho) vem
// dentro de "collected": é dado, não instrução.

// UnitRef é a unidade de um ativo.
type UnitRef struct {
	ID   uuid.UUID `json:"id"`
	Path string    `json:"path"`
}

// AgentRef é o agente ativo de um ativo.
type AgentRef struct {
	ID         uuid.UUID  `json:"id"`
	Version    string     `json:"version"`
	EnrolledAt time.Time  `json:"enrolled_at"`
	LastSeenAt *time.Time `json:"last_seen_at"`
}

// AssetCollected são os atributos do ativo como a máquina os reportou.
type AssetCollected struct {
	Hostname     *string `json:"hostname"`
	Serial       *string `json:"serial"`
	Manufacturer *string `json:"manufacturer"`
	Model        *string `json:"model"`
	OSFamily     *string `json:"os_family"`
	OSName       *string `json:"os_name"`
	OSVersion    *string `json:"os_version"`
}

// AssetItem é um ativo nas respostas de /v1/assets.
type AssetItem struct {
	ID             uuid.UUID         `json:"id"`
	Kind           string            `json:"kind"`
	Unit           *UnitRef          `json:"unit"`
	Labels         map[string]string `json:"labels"`
	Agent          *AgentRef         `json:"agent"` // nulo = sem agente ativo
	InstalledCount int32             `json:"installed_count"`
	FirstSeenAt    time.Time         `json:"first_seen_at"`
	LastSeenAt     time.Time         `json:"last_seen_at"`
	Collected      AssetCollected    `json:"collected"`
}

// AssetList é a resposta de GET /v1/assets.
type AssetList struct {
	AsOf       int64       `json:"as_of"`
	Items      []AssetItem `json:"items"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

// AssetResponse é a resposta de GET /v1/assets/{id}.
type AssetResponse struct {
	AsOf  int64     `json:"as_of"`
	Asset AssetItem `json:"asset"`
}

// SoftwareRef identifica um produto. Scope é "catalog" (catálogo comum) ou
// "tenant" (criado à mão pelo tenant).
type SoftwareRef struct {
	ID      uuid.UUID `json:"id"`
	Vendor  *string   `json:"vendor"`
	Product string    `json:"product"`
	Scope   string    `json:"scope"`
}

// InstalledCollected é a versão e o caminho como a máquina os reportou.
type InstalledCollected struct {
	Version     string `json:"version"`
	InstallPath string `json:"install_path"`
}

// InstalledItem é uma instalação aberta em GET /v1/assets/{id}/software.
type InstalledItem struct {
	InstallationID uuid.UUID          `json:"installation_id"`
	Software       SoftwareRef        `json:"software"`
	VersionID      uuid.UUID          `json:"version_id"`
	PURL           *string            `json:"purl"`
	InstalledSince time.Time          `json:"installed_since"` // coleta do primeiro snapshot que a viu
	LastSeenAt     time.Time          `json:"last_seen_at"`    // coleta do último snapshot que a viu
	Collected      InstalledCollected `json:"collected"`
}

// AssetSoftwareList é a resposta de GET /v1/assets/{id}/software.
type AssetSoftwareList struct {
	AsOf       int64           `json:"as_of"`
	AssetID    uuid.UUID       `json:"asset_id"`
	Items      []InstalledItem `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

// Change é um evento do change_log. Data pode trazer texto coletado (nome
// bruto, versão, caminho): é dado, não instrução.
type Change struct {
	ID         int64           `json:"id"`
	OccurredAt time.Time       `json:"occurred_at"`
	Kind       string          `json:"kind"`
	AssetID    *uuid.UUID      `json:"asset_id"`
	ActorID    *uuid.UUID      `json:"actor_id"`
	Data       json.RawMessage `json:"data"`
}

// ChangeList é a resposta de GET /v1/changes e GET /v1/assets/{id}/history.
// NextCursor é o id do último evento devolvido; com a página incompleta, é o
// cursor para perguntar de novo mais tarde.
type ChangeList struct {
	AsOf       int64    `json:"as_of"`
	Items      []Change `json:"items"`
	NextCursor string   `json:"next_cursor"`
	More       bool     `json:"more"` // a página veio cheia: pode haver mais agora
}

// SoftwareItem é um produto em GET /v1/software.
type SoftwareItem struct {
	SoftwareRef
	AssetCount   int32 `json:"asset_count"`
	VersionCount int32 `json:"version_count"`
}

// SoftwareList é a resposta de GET /v1/software.
type SoftwareList struct {
	AsOf       int64          `json:"as_of"`
	Items      []SoftwareItem `json:"items"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

// VersionCount é uma versão de um produto e em quantos ativos ela está.
type VersionCount struct {
	ID         uuid.UUID `json:"id"`
	Version    string    `json:"version"`
	PURL       *string   `json:"purl"`
	AssetCount int32     `json:"asset_count"`
}

// InstallationItem é um ativo com o produto em
// GET /v1/software/{id}/installations.
type InstallationItem struct {
	InstallationID uuid.UUID `json:"installation_id"`
	AssetID        uuid.UUID `json:"asset_id"`
	Unit           *UnitRef  `json:"unit"`
	VersionID      uuid.UUID `json:"version_id"`
	PURL           *string   `json:"purl"`
	InstalledSince time.Time `json:"installed_since"`
	LastSeenAt     time.Time `json:"last_seen_at"`
	Collected      struct {
		Hostname    *string `json:"hostname"`
		Version     string  `json:"version"`
		InstallPath string  `json:"install_path"`
	} `json:"collected"`
}

// InstallationList é a resposta de GET /v1/software/{id}/installations.
type InstallationList struct {
	AsOf       int64              `json:"as_of"`
	Software   SoftwareRef        `json:"software"`
	Versions   []VersionCount     `json:"versions"` // todas, com o filtro de unidade
	Items      []InstallationItem `json:"items"`
	NextCursor string             `json:"next_cursor,omitempty"`
}

// readQuery roda fn numa leitura repetível, só leitura, e devolve o as_of
// do mesmo instante.
func (s *Server) readQuery(ctx context.Context, tenant uuid.UUID, fn func(q *store.Queries) error) (int64, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	asOf, err := q.LatestChangeID(ctx, tenant)
	if err != nil {
		return 0, err
	}
	return asOf, fn(q)
}

func pageSize(qs url.Values) (int32, error) {
	v := qs.Get("limit")
	if v == "" {
		return defaultPage, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > maxPage {
		return 0, errorf(http.StatusBadRequest, "limit entre 1 e %d", maxPage)
	}
	return int32(n), nil
}

// keyCursor é o cursor das listas em ordem de nome: a chave de ordenação e
// o id do último item, opacos para o cliente.
func keyCursor(key string, id uuid.UUID) string {
	b, _ := json.Marshal([]string{key, id.String()})
	return base64.RawURLEncoding.EncodeToString(b)
}

func parseKeyCursor(qs url.Values) (*string, *uuid.UUID, error) {
	c := qs.Get("cursor")
	if c == "" {
		return nil, nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(c)
	var parts []string
	if err == nil {
		err = json.Unmarshal(b, &parts)
	}
	if err != nil || len(parts) != 2 {
		return nil, nil, errorf(http.StatusBadRequest, "cursor inválido")
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return nil, nil, errorf(http.StatusBadRequest, "cursor inválido")
	}
	return &parts[0], &id, nil
}

// likePattern busca o texto em qualquer parte, sem que % e _ do usuário
// virem curingas.
func likePattern(qs url.Values, name string) *string {
	v := strings.TrimSpace(qs.Get(name))
	if v == "" {
		return nil
	}
	v = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(v)
	v = "%" + v + "%"
	return &v
}

func optional(qs url.Values, name string) *string {
	if v := qs.Get(name); v != "" {
		return &v
	}
	return nil
}

// unitScope é o filtro de unidade e o mapa de caminhos para as respostas.
type unitScope struct {
	mode  string // "", "none" ou "in"
	ids   []uuid.UUID
	paths map[uuid.UUID]string
}

func (u unitScope) ref(id *uuid.UUID) *UnitRef {
	if id == nil {
		return nil
	}
	return &UnitRef{ID: *id, Path: u.paths[*id]}
}

// loadUnits lê a árvore e interpreta ?unit= (id, caminho ou "none") e
// ?subtree=false (só a unidade, sem as de baixo).
func loadUnits(ctx context.Context, q *store.Queries, tenant uuid.UUID, qs url.Values) (unitScope, error) {
	rows, err := q.ListUnits(ctx, tenant)
	if err != nil {
		return unitScope{}, err
	}
	sc := unitScope{paths: make(map[uuid.UUID]string, len(rows)), ids: []uuid.UUID{}}
	children := map[uuid.UUID][]uuid.UUID{}
	byPath := map[string]uuid.UUID{}
	for _, r := range rows {
		sc.paths[r.ID] = r.Path
		byPath[r.Path] = r.ID
		if r.ParentID != nil {
			children[*r.ParentID] = append(children[*r.ParentID], r.ID)
		}
	}
	v := qs.Get("unit")
	switch {
	case v == "":
		return sc, nil
	case v == "none":
		sc.mode = "none"
		return sc, nil
	}
	root, err := uuid.Parse(v)
	if err != nil {
		var ok bool
		if root, ok = byPath[strings.Trim(v, "/")]; !ok {
			return sc, errorf(http.StatusNotFound, "unidade %q não encontrada", v)
		}
	} else if _, ok := sc.paths[root]; !ok {
		return sc, errorf(http.StatusNotFound, "unidade não encontrada")
	}
	sc.mode = "in"
	sc.ids = append(sc.ids, root)
	if qs.Get("subtree") != "false" {
		for i := 0; i < len(sc.ids); i++ {
			sc.ids = append(sc.ids, children[sc.ids[i]]...)
		}
	}
	return sc, nil
}

func scopeOf(tenant *uuid.UUID) string {
	if tenant == nil {
		return "catalog"
	}
	return "tenant"
}

// listAssets: GET /v1/assets?unit=&subtree=&hostname=&os_family=&label=k=v&software_id=&limit=&cursor=
func (s *Server) listAssets(w http.ResponseWriter, r *http.Request) {
	actor := actorFrom(r.Context())
	qs := r.URL.Query()
	resp := AssetList{Items: []AssetItem{}}
	var err error
	resp.AsOf, err = s.readQuery(r.Context(), actor.TenantID, func(q *store.Queries) error {
		p, err := assetParams(r.Context(), q, actor.TenantID, qs)
		if err != nil {
			return err
		}
		units, err := loadUnits(r.Context(), q, actor.TenantID, qs)
		if err != nil {
			return err
		}
		p.UnitMode, p.UnitIds = units.mode, units.ids
		rows, err := q.ListAssets(r.Context(), p)
		if err != nil {
			return err
		}
		if resp.Items, err = assetItems(r.Context(), q, rows, units); err != nil {
			return err
		}
		if len(rows) == int(p.PageSize) {
			last := rows[len(rows)-1]
			host := ""
			if last.Hostname != nil {
				host = strings.ToLower(*last.Hostname)
			}
			resp.NextCursor = keyCursor(host, last.ID)
		}
		return nil
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func assetParams(ctx context.Context, q *store.Queries, tenant uuid.UUID, qs url.Values) (store.ListAssetsParams, error) {
	p := store.ListAssetsParams{TenantID: tenant, Labels: []string{}, UnitIds: []uuid.UUID{}}
	var err error
	if p.PageSize, err = pageSize(qs); err != nil {
		return p, err
	}
	if p.AfterKey, p.AfterID, err = parseKeyCursor(qs); err != nil {
		return p, err
	}
	p.Hostname = likePattern(qs, "hostname")
	p.OsFamily = optional(qs, "os_family")
	for _, l := range qs["label"] {
		k, v, ok := strings.Cut(l, "=")
		if !ok || !labelKey.MatchString(k) {
			return p, errorf(http.StatusBadRequest, "label=chave=valor, com chave de etiqueta válida")
		}
		p.Labels = append(p.Labels, k+"="+v)
	}
	if v := qs.Get("software_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			return p, errorf(http.StatusBadRequest, "software_id inválido")
		}
		p.SoftwareID = &id
	}
	return p, nil
}

// assetItems completa as linhas com unidade, etiquetas e agente ativo.
func assetItems(ctx context.Context, q *store.Queries, rows []store.ListAssetsRow, units unitScope) ([]AssetItem, error) {
	items := make([]AssetItem, 0, len(rows))
	if len(rows) == 0 {
		return items, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	agents, err := q.ActiveAgentsOf(ctx, ids)
	if err != nil {
		return nil, err
	}
	agentOf := map[uuid.UUID]*AgentRef{}
	for _, a := range agents {
		agentOf[a.AssetID] = &AgentRef{ID: a.ID, Version: a.AgentVersion, EnrolledAt: a.EnrolledAt, LastSeenAt: a.LastSeenAt}
	}
	labels, err := q.LabelsOf(ctx, ids)
	if err != nil {
		return nil, err
	}
	labelsOf := map[uuid.UUID]map[string]string{}
	for _, l := range labels {
		if labelsOf[l.AssetID] == nil {
			labelsOf[l.AssetID] = map[string]string{}
		}
		labelsOf[l.AssetID][l.Key] = l.Value
	}
	for _, r := range rows {
		it := AssetItem{
			ID: r.ID, Kind: r.Kind, Unit: units.ref(r.UnitID), Labels: labelsOf[r.ID], Agent: agentOf[r.ID],
			InstalledCount: r.InstalledCount, FirstSeenAt: r.FirstSeenAt, LastSeenAt: r.LastSeenAt,
			Collected: AssetCollected{
				Hostname: r.Hostname, Serial: r.Serial, Manufacturer: r.Manufacturer, Model: r.Model,
				OSFamily: r.OsFamily, OSName: r.OsName, OSVersion: r.OsVersion,
			},
		}
		if it.Labels == nil {
			it.Labels = map[string]string{}
		}
		items = append(items, it)
	}
	return items, nil
}

// getAsset: GET /v1/assets/{id}.
func (s *Server) getAsset(w http.ResponseWriter, r *http.Request) {
	actor := actorFrom(r.Context())
	id, ok := pathID(w, r, "ativo")
	if !ok {
		return
	}
	var resp AssetResponse
	var err error
	resp.AsOf, err = s.readQuery(r.Context(), actor.TenantID, func(q *store.Queries) error {
		units, err := loadUnits(r.Context(), q, actor.TenantID, url.Values{})
		if err != nil {
			return err
		}
		rows, err := q.ListAssets(r.Context(), store.ListAssetsParams{
			TenantID: actor.TenantID, AssetID: &id, Labels: []string{}, UnitIds: []uuid.UUID{}, PageSize: 1,
		})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errorf(http.StatusNotFound, "ativo não encontrado")
		}
		items, err := assetItems(r.Context(), q, rows, units)
		if err != nil {
			return err
		}
		resp.Asset = items[0]
		return nil
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// requireAsset responde 404 para ativo que não existe ou é de outro tenant.
func requireAsset(ctx context.Context, q *store.Queries, id, tenant uuid.UUID) error {
	a, err := q.GetAsset(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && a.TenantID != tenant) {
		return errorf(http.StatusNotFound, "ativo não encontrado")
	}
	return err
}

// assetSoftware: GET /v1/assets/{id}/software?product=&limit=&cursor=
func (s *Server) assetSoftware(w http.ResponseWriter, r *http.Request) {
	actor := actorFrom(r.Context())
	id, ok := pathID(w, r, "ativo")
	if !ok {
		return
	}
	qs := r.URL.Query()
	resp := AssetSoftwareList{AssetID: id, Items: []InstalledItem{}}
	var err error
	resp.AsOf, err = s.readQuery(r.Context(), actor.TenantID, func(q *store.Queries) error {
		if err := requireAsset(r.Context(), q, id, actor.TenantID); err != nil {
			return err
		}
		p := store.ListAssetSoftwareParams{AssetID: id, Product: likePattern(qs, "product")}
		var err error
		if p.PageSize, err = pageSize(qs); err != nil {
			return err
		}
		if p.AfterKey, p.AfterID, err = parseKeyCursor(qs); err != nil {
			return err
		}
		rows, err := q.ListAssetSoftware(r.Context(), p)
		if err != nil {
			return err
		}
		for _, row := range rows {
			resp.Items = append(resp.Items, InstalledItem{
				InstallationID: row.ID,
				Software:       SoftwareRef{ID: row.SoftwareID, Vendor: row.Vendor, Product: row.Product, Scope: scopeOf(row.SoftwareTenantID)},
				VersionID:      row.VersionID, PURL: row.Purl,
				InstalledSince: row.InstalledSince, LastSeenAt: row.LastSeenAt,
				Collected: InstalledCollected{Version: row.Version, InstallPath: row.InstallPath},
			})
		}
		if len(rows) == int(p.PageSize) {
			last := rows[len(rows)-1]
			resp.NextCursor = keyCursor(strings.ToLower(last.Product), last.ID)
		}
		return nil
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// changeParams lê ?after= (id do último evento visto), ?kind= (repetível) e
// ?limit=.
func changeParams(qs url.Values) (after int64, kinds []string, limit int32, err error) {
	if v := qs.Get("after"); v != "" {
		if after, err = strconv.ParseInt(v, 10, 64); err != nil || after < 0 {
			return 0, nil, 0, errorf(http.StatusBadRequest, "after é o id do último evento visto")
		}
	}
	kinds = []string{}
	for _, k := range qs["kind"] {
		kinds = append(kinds, strings.Split(k, ",")...)
	}
	limit, err = pageSize(qs)
	return after, kinds, limit, err
}

func changeList(rows []store.ChangeLog, after int64, limit int32) ChangeList {
	l := ChangeList{Items: make([]Change, 0, len(rows)), NextCursor: strconv.FormatInt(after, 10), More: len(rows) == int(limit)}
	for _, c := range rows {
		l.Items = append(l.Items, Change{ID: c.ID, OccurredAt: c.OccurredAt, Kind: c.Kind, AssetID: c.AssetID, ActorID: c.ActorID, Data: c.Data})
		l.NextCursor = strconv.FormatInt(c.ID, 10)
	}
	return l
}

// assetHistory: GET /v1/assets/{id}/history?since=&kind=&after=&limit=
func (s *Server) assetHistory(w http.ResponseWriter, r *http.Request) {
	actor := actorFrom(r.Context())
	id, ok := pathID(w, r, "ativo")
	if !ok {
		return
	}
	qs := r.URL.Query()
	after, kinds, limit, err := changeParams(qs)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var since *time.Time
	if v := qs.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			if t, err = time.Parse(time.DateOnly, v); err != nil {
				writeError(w, http.StatusBadRequest, "since em RFC 3339 (2026-03-01T00:00:00-03:00) ou data (2026-03-01)")
				return
			}
		}
		since = &t
	}
	var resp ChangeList
	asOf, err := s.readQuery(r.Context(), actor.TenantID, func(q *store.Queries) error {
		if err := requireAsset(r.Context(), q, id, actor.TenantID); err != nil {
			return err
		}
		rows, err := q.ListAssetChanges(r.Context(), store.ListAssetChangesParams{
			TenantID: actor.TenantID, AssetID: &id, AfterID: after, Since: since, Kinds: kinds, PageSize: limit,
		})
		resp = changeList(rows, after, limit)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	resp.AsOf = asOf
	writeJSON(w, http.StatusOK, resp)
}

// listChanges: GET /v1/changes?after=&kind=&limit=
// Quem acompanha guarda next_cursor e pergunta de novo com after=next_cursor.
func (s *Server) listChanges(w http.ResponseWriter, r *http.Request) {
	actor := actorFrom(r.Context())
	after, kinds, limit, err := changeParams(r.URL.Query())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var resp ChangeList
	asOf, err := s.readQuery(r.Context(), actor.TenantID, func(q *store.Queries) error {
		rows, err := q.ListChanges(r.Context(), store.ListChangesParams{TenantID: actor.TenantID, AfterID: after, Kinds: kinds, PageSize: limit})
		resp = changeList(rows, after, limit)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	resp.AsOf = asOf
	writeJSON(w, http.StatusOK, resp)
}

// listSoftware: GET /v1/software?product=&unit=&subtree=&limit=&cursor=
// Os produtos instalados agora nos ativos do tenant.
func (s *Server) listSoftware(w http.ResponseWriter, r *http.Request) {
	actor := actorFrom(r.Context())
	qs := r.URL.Query()
	resp := SoftwareList{Items: []SoftwareItem{}}
	var err error
	resp.AsOf, err = s.readQuery(r.Context(), actor.TenantID, func(q *store.Queries) error {
		units, err := loadUnits(r.Context(), q, actor.TenantID, qs)
		if err != nil {
			return err
		}
		p := store.ListTenantSoftwareParams{TenantID: actor.TenantID, UnitMode: units.mode, UnitIds: units.ids, Product: likePattern(qs, "product")}
		if p.PageSize, err = pageSize(qs); err != nil {
			return err
		}
		if p.AfterKey, p.AfterID, err = parseKeyCursor(qs); err != nil {
			return err
		}
		rows, err := q.ListTenantSoftware(r.Context(), p)
		if err != nil {
			return err
		}
		for _, row := range rows {
			resp.Items = append(resp.Items, SoftwareItem{
				SoftwareRef: SoftwareRef{ID: row.ID, Vendor: row.Vendor, Product: row.Product, Scope: scopeOf(row.SoftwareTenantID)},
				AssetCount:  row.AssetCount, VersionCount: row.VersionCount,
			})
		}
		if len(rows) == int(p.PageSize) {
			last := rows[len(rows)-1]
			resp.NextCursor = keyCursor(strings.ToLower(last.Product), last.ID)
		}
		return nil
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// softwareInstallations: GET /v1/software/{id}/installations?version_id=&version=&unit=&subtree=&limit=&cursor=
func (s *Server) softwareInstallations(w http.ResponseWriter, r *http.Request) {
	actor := actorFrom(r.Context())
	id, ok := pathID(w, r, "produto")
	if !ok {
		return
	}
	qs := r.URL.Query()
	resp := InstallationList{Versions: []VersionCount{}, Items: []InstallationItem{}}
	var err error
	resp.AsOf, err = s.readQuery(r.Context(), actor.TenantID, func(q *store.Queries) error {
		sw, err := q.GetVisibleSoftware(r.Context(), store.GetVisibleSoftwareParams{ID: id, TenantID: &actor.TenantID})
		if errors.Is(err, pgx.ErrNoRows) {
			return errorf(http.StatusNotFound, "produto não encontrado")
		}
		if err != nil {
			return err
		}
		resp.Software = SoftwareRef{ID: sw.ID, Vendor: sw.Vendor, Product: sw.Product, Scope: scopeOf(sw.SoftwareTenantID)}
		units, err := loadUnits(r.Context(), q, actor.TenantID, qs)
		if err != nil {
			return err
		}
		p := store.ListSoftwareInstallationsParams{
			SoftwareID: id, TenantID: actor.TenantID, UnitMode: units.mode, UnitIds: units.ids, Version: optional(qs, "version"),
		}
		if v := qs.Get("version_id"); v != "" {
			vid, err := uuid.Parse(v)
			if err != nil {
				return errorf(http.StatusBadRequest, "version_id inválido")
			}
			p.VersionID = &vid
		}
		if p.PageSize, err = pageSize(qs); err != nil {
			return err
		}
		if p.AfterKey, p.AfterID, err = parseKeyCursor(qs); err != nil {
			return err
		}
		versions, err := q.ListSoftwareVersionsInTenant(r.Context(), store.ListSoftwareVersionsInTenantParams{
			SoftwareID: id, TenantID: actor.TenantID, UnitMode: units.mode, UnitIds: units.ids,
		})
		if err != nil {
			return err
		}
		for _, v := range versions {
			resp.Versions = append(resp.Versions, VersionCount{ID: v.ID, Version: v.Version, PURL: v.Purl, AssetCount: v.AssetCount})
		}
		rows, err := q.ListSoftwareInstallations(r.Context(), p)
		if err != nil {
			return err
		}
		for _, row := range rows {
			it := InstallationItem{
				InstallationID: row.ID, AssetID: row.AssetID, Unit: units.ref(row.UnitID),
				VersionID: row.VersionID, PURL: row.Purl, InstalledSince: row.InstalledSince, LastSeenAt: row.LastSeenAt,
			}
			it.Collected.Hostname, it.Collected.Version, it.Collected.InstallPath = row.Hostname, row.Version, row.InstallPath
			resp.Items = append(resp.Items, it)
		}
		if len(rows) == int(p.PageSize) {
			last := rows[len(rows)-1]
			host := ""
			if last.Hostname != nil {
				host = strings.ToLower(*last.Hostname)
			}
			resp.NextCursor = keyCursor(host, last.ID)
		}
		return nil
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
