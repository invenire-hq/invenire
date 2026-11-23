// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/invenire-hq/invenire/internal/server/api"
	"github.com/invenire-hq/invenire/internal/server/export"
	"github.com/invenire-hq/invenire/internal/server/password"
	"github.com/invenire-hq/invenire/internal/server/store"
)

func pathUUID(r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	return id, err == nil
}

// formUUID lê um id opcional do formulário; vazio = nulo.
func formUUID(r *http.Request, name string) (*uuid.UUID, error) {
	v := strings.TrimSpace(r.PostFormValue(name))
	if v == "" {
		return nil, nil
	}
	id, err := uuid.Parse(v)
	if err != nil {
		return nil, errors.New("id inválido")
	}
	return &id, nil
}

// ---------------------------------------------------------------- visão geral

type stateCount struct {
	State string
	N     int
}

type overviewView struct {
	P        api.ParkStatus
	States   []stateCount
	Problems int
	OS       []kv
	Versions []kv
}

var stateOrder = []string{api.StateNoAgent, api.StateFailed, api.StateStale, api.StateNoData, api.StatePending, api.StateOK}

func sortedCounts(m map[string]int) []kv {
	out := make([]kv, 0, len(m))
	for k, v := range m {
		out = append(out, kv{k, strconv.Itoa(v)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].K < out[j].K })
	return out
}

func (p *Panel) overview(w http.ResponseWriter, r *http.Request, s *session) {
	ps, err := p.api.TenantStatus(r.Context(), s.Actor.TenantID, p.stale, p.now())
	if err != nil {
		p.fail(w, r, s, err)
		return
	}
	v := overviewView{P: ps, Problems: ps.Problems(), OS: sortedCounts(ps.ByOS), Versions: sortedCounts(ps.ByVersion)}
	for _, st := range stateOrder {
		v.States = append(v.States, stateCount{st, ps.ByState[st]})
	}
	p.render(w, r, http.StatusOK, "overview", s, pageData{Title: msg("nav.overview"), Nav: "overview", Data: v})
}

// ---------------------------------------------------------- adicionar máquinas

type tokenRow struct {
	store.ListEnrollmentTokensRow
	State string
	Unit  string
}

type newToken struct {
	api.NewToken
	Uses   int
	Unit   string
	Server string
	Pin    string
	// As formas de instalar (install.go).
	Install installView
}

type enrollView struct {
	Units  []api.Unit
	Tokens []tokenRow
	All    bool
	First  bool
	New    *newToken
}

func (p *Panel) enrollData(r *http.Request, s *session, all bool) (enrollView, error) {
	v := enrollView{All: all}
	units, err := p.api.ListUnits(r.Context(), s.Actor.TenantID)
	if err != nil {
		return v, err
	}
	v.Units = units.Items
	paths := map[uuid.UUID]string{}
	for _, u := range units.Items {
		paths[u.ID] = u.Path
	}
	rows, err := p.q.ListEnrollmentTokens(r.Context(), store.ListEnrollmentTokensParams{TenantID: s.Actor.TenantID, IncludeAll: all})
	if err != nil {
		return v, err
	}
	now := p.now()
	for _, t := range rows {
		row := tokenRow{ListEnrollmentTokensRow: t, State: "valid"}
		switch {
		case t.RevokedAt != nil:
			row.State = "revoked"
		case t.Uses >= t.MaxUses:
			row.State = "spent"
		case !t.ExpiresAt.After(now):
			row.State = "expired"
		}
		if t.UnitID != nil {
			row.Unit = paths[*t.UnitID]
		}
		v.Tokens = append(v.Tokens, row)
	}
	return v, nil
}

func (p *Panel) enrollPage(w http.ResponseWriter, r *http.Request, s *session) {
	v, err := p.enrollData(r, s, r.URL.Query().Get("todos") == "1")
	if err != nil {
		p.fail(w, r, s, err)
		return
	}
	v.First = r.URL.Query().Get("primeira") == "1"
	p.render(w, r, http.StatusOK, "enroll", s, pageData{Title: msg("nav.enroll"), Nav: "enroll", Data: v})
}

// serverURL é o endereço que o navegador usou, que é o mesmo que as
// máquinas da rede devem usar. Atrás de proxy, o Host vem do proxy.
func serverURL(r *http.Request) string {
	return "https://" + r.Host
}

var ttls = map[string]time.Duration{"1h": time.Hour, "24h": 24 * time.Hour, "168h": 7 * 24 * time.Hour, "720h": 30 * 24 * time.Hour}

func (p *Panel) enrollCreate(w http.ResponseWriter, r *http.Request, s *session) {
	unit, err := formUUID(r, "unit")
	if err != nil {
		p.failed(w, r, s, "/enroll", errBadRequest(msg("error.bad_unit")))
		return
	}
	uses, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("uses")))
	if err != nil {
		p.failed(w, r, s, "/enroll", errBadRequest(msg("enroll.bad_uses")))
		return
	}
	ttl, ok := ttls[r.PostFormValue("ttl")]
	if !ok {
		p.failed(w, r, s, "/enroll", errBadRequest(msg("enroll.bad_ttl")))
		return
	}
	spec := api.TokenSpec{Tenant: s.Actor.TenantID, Unit: unit, Description: strings.TrimSpace(r.PostFormValue("description")), TTL: ttl, Uses: uses}
	var tok api.NewToken
	err = p.api.InTx(r.Context(), func(q *store.Queries) error {
		var err error
		tok, err = api.IssueEnrollmentToken(r.Context(), q, &s.Actor.ID, spec)
		return err
	})
	if err != nil {
		p.failed(w, r, s, "/enroll", err)
		return
	}
	v, err := p.enrollData(r, s, false)
	if err != nil {
		p.fail(w, r, s, err)
		return
	}
	v.New = &newToken{NewToken: tok, Uses: uses, Server: serverURL(r), Pin: p.api.CAPin()}
	v.New.Install = p.installView(v.New.Server, v.New.Pin)
	for _, u := range v.Units {
		if unit != nil && u.ID == *unit {
			v.New.Unit = u.Path
		}
	}
	p.render(w, r, http.StatusOK, "enroll", s, pageData{Title: msg("nav.enroll"), Nav: "enroll", Data: v})
}

func (p *Panel) enrollRevoke(w http.ResponseWriter, r *http.Request, s *session) {
	id, ok := pathUUID(r)
	if !ok {
		p.notFound(w, r, s)
		return
	}
	err := p.api.InTx(r.Context(), func(q *store.Queries) error {
		_, err := api.RevokeEnrollmentToken(r.Context(), q, s.Actor.TenantID, &s.Actor.ID, id)
		return err
	})
	if err != nil {
		p.failed(w, r, s, "/enroll", err)
		return
	}
	p.done(w, r, "/enroll", msg("enroll.revoked"))
}

// ------------------------------------------------------------------ máquinas

type assetFilter struct {
	Unit, OS, Host string
}

type assetsView struct {
	Units  []api.Unit
	Filter assetFilter
	List   api.AssetList
	State  map[uuid.UUID]string
	Next   string
	Paged  bool
}

// stateMap é o estado de cada ativo (o mesmo da visão geral).
func (p *Panel) stateMap(r *http.Request, s *session) (map[uuid.UUID]api.AssetStatus, error) {
	ps, err := p.api.TenantStatus(r.Context(), s.Actor.TenantID, p.stale, p.now())
	if err != nil {
		return nil, err
	}
	m := make(map[uuid.UUID]api.AssetStatus, len(ps.Assets))
	for _, a := range ps.Assets {
		m[a.ID] = a
	}
	return m, nil
}

func (p *Panel) assetsPage(w http.ResponseWriter, r *http.Request, s *session) {
	in := r.URL.Query()
	f := assetFilter{Unit: in.Get("unit"), OS: in.Get("os"), Host: strings.TrimSpace(in.Get("host"))}
	qs := url.Values{}
	if f.Unit != "" {
		qs.Set("unit", f.Unit)
	}
	if f.OS != "" {
		qs.Set("os_family", f.OS)
	}
	if f.Host != "" {
		qs.Set("hostname", f.Host)
	}
	if c := in.Get("cursor"); c != "" {
		qs.Set("cursor", c)
	}
	list, err := p.api.ListAssets(r.Context(), s.Actor.TenantID, qs)
	if err != nil {
		p.fail(w, r, s, err)
		return
	}
	units, err := p.api.ListUnits(r.Context(), s.Actor.TenantID)
	if err != nil {
		p.fail(w, r, s, err)
		return
	}
	states, err := p.stateMap(r, s)
	if err != nil {
		p.fail(w, r, s, err)
		return
	}
	v := assetsView{Units: units.Items, Filter: f, List: list, State: map[uuid.UUID]string{}, Paged: in.Get("cursor") != ""}
	for _, a := range list.Items {
		v.State[a.ID] = states[a.ID].State
	}
	if list.NextCursor != "" {
		next := url.Values{"unit": {f.Unit}, "os": {f.OS}, "host": {f.Host}, "cursor": {list.NextCursor}}
		v.Next = "/assets?" + next.Encode()
	}
	p.render(w, r, http.StatusOK, "assets", s, pageData{Title: msg("nav.assets"), Nav: "assets", Data: v})
}

type assetView struct {
	A        api.AssetItem
	Status   api.AssetStatus
	Units    []api.Unit
	Software api.AssetSoftwareList
	Product  string
	SwNext   string
	History  []api.Change
	HistNext string
	Kinds    string
}

// Filtros do histórico no painel.
var historyKinds = map[string][]string{
	"software": {"software_installed", "software_removed"},
	"org":      {"asset_moved", "label_set", "label_removed"},
	"agent":    {"asset_new", "asset_changed", "agent_enrolled", "agent_revoked", "agent_cert_renewed", "snapshot_chain_broken", "snapshot_out_of_order"},
}

func (p *Panel) assetPage(w http.ResponseWriter, r *http.Request, s *session) {
	id, ok := pathUUID(r)
	if !ok {
		p.notFound(w, r, s)
		return
	}
	in := r.URL.Query()
	asset, err := p.api.GetAsset(r.Context(), s.Actor.TenantID, id)
	if err != nil {
		if status, _, _ := api.ErrorMessage(err); status == http.StatusNotFound {
			p.notFound(w, r, s)
			return
		}
		p.fail(w, r, s, err)
		return
	}
	v := assetView{A: asset.Asset, Product: strings.TrimSpace(in.Get("product")), Kinds: in.Get("kinds")}
	units, err := p.api.ListUnits(r.Context(), s.Actor.TenantID)
	if err != nil {
		p.fail(w, r, s, err)
		return
	}
	v.Units = units.Items
	states, err := p.stateMap(r, s)
	if err != nil {
		p.fail(w, r, s, err)
		return
	}
	v.Status = states[id]
	swq := url.Values{"limit": {"100"}}
	if v.Product != "" {
		swq.Set("product", v.Product)
	}
	if c := in.Get("sw"); c != "" {
		swq.Set("cursor", c)
	}
	if v.Software, err = p.api.AssetSoftware(r.Context(), s.Actor.TenantID, id, swq); err != nil {
		p.failed(w, r, s, "/assets/"+id.String(), err)
		return
	}
	if v.Software.NextCursor != "" {
		v.SwNext = "/assets/" + id.String() + "?" + url.Values{"product": {v.Product}, "sw": {v.Software.NextCursor}, "kinds": {v.Kinds}}.Encode() + "#software"
	}
	before, _ := strconv.ParseInt(in.Get("antes"), 10, 64)
	const histPage = 50
	if v.History, err = p.api.RecentAssetHistory(r.Context(), s.Actor.TenantID, id, before, historyKinds[v.Kinds], histPage); err != nil {
		p.fail(w, r, s, err)
		return
	}
	if len(v.History) == histPage {
		v.HistNext = "/assets/" + id.String() + "?" + url.Values{"kinds": {v.Kinds}, "antes": {strconv.FormatInt(v.History[len(v.History)-1].ID, 10)}}.Encode() + "#historico"
	}
	p.render(w, r, http.StatusOK, "asset", s, pageData{Title: orDash(deref(asset.Asset.Collected.Hostname)), Nav: "assets", Data: v})
}

func (p *Panel) assetSetUnit(w http.ResponseWriter, r *http.Request, s *session) {
	id, ok := pathUUID(r)
	if !ok {
		p.notFound(w, r, s)
		return
	}
	back := "/assets/" + id.String()
	unit, err := formUUID(r, "unit")
	if err != nil {
		p.failed(w, r, s, back, errBadRequest(msg("error.bad_unit")))
		return
	}
	if _, err := p.api.SetAssetUnit(ctxWithActor(r, s), s.Actor, id, unit); err != nil {
		p.failed(w, r, s, back, err)
		return
	}
	p.done(w, r, back, msg("asset.unit_saved"))
}

func (p *Panel) assetSetLabel(w http.ResponseWriter, r *http.Request, s *session) {
	id, ok := pathUUID(r)
	if !ok {
		p.notFound(w, r, s)
		return
	}
	back := "/assets/" + id.String()
	value := r.PostFormValue("value")
	if _, err := p.api.ChangeLabel(ctxWithActor(r, s), s.Actor, id, strings.TrimSpace(r.PostFormValue("key")), &value); err != nil {
		p.failed(w, r, s, back, err)
		return
	}
	p.done(w, r, back, msg("asset.label_saved"))
}

func (p *Panel) assetRemoveLabel(w http.ResponseWriter, r *http.Request, s *session) {
	id, ok := pathUUID(r)
	if !ok {
		p.notFound(w, r, s)
		return
	}
	back := "/assets/" + id.String()
	if _, err := p.api.ChangeLabel(ctxWithActor(r, s), s.Actor, id, r.PostFormValue("key"), nil); err != nil {
		p.failed(w, r, s, back, err)
		return
	}
	p.done(w, r, back, msg("asset.label_removed"))
}

func (p *Panel) assetRevokeAgent(w http.ResponseWriter, r *http.Request, s *session) {
	id, ok := pathUUID(r)
	if !ok {
		p.notFound(w, r, s)
		return
	}
	back := "/assets/" + id.String()
	if err := p.api.RevokeAgent(ctxWithActor(r, s), s.Actor, id); err != nil {
		p.failed(w, r, s, back, err)
		return
	}
	p.done(w, r, back, msg("asset.agent_revoked"))
}

// ------------------------------------------------------------------ unidades

type unitsView struct {
	Units []api.Unit
	Unit  *api.Unit // na página de uma unidade
	// Parents são as unidades que podem ser mãe desta (todas menos ela e as de baixo).
	Parents []api.Unit
}

func (p *Panel) unitsPage(w http.ResponseWriter, r *http.Request, s *session) {
	units, err := p.api.ListUnits(r.Context(), s.Actor.TenantID)
	if err != nil {
		p.fail(w, r, s, err)
		return
	}
	p.render(w, r, http.StatusOK, "units", s, pageData{Title: msg("nav.units"), Nav: "units", Data: unitsView{Units: units.Items}})
}

func (p *Panel) unitPage(w http.ResponseWriter, r *http.Request, s *session) {
	id, ok := pathUUID(r)
	if !ok {
		p.notFound(w, r, s)
		return
	}
	units, err := p.api.ListUnits(r.Context(), s.Actor.TenantID)
	if err != nil {
		p.fail(w, r, s, err)
		return
	}
	v := unitsView{Units: units.Items}
	for i, u := range units.Items {
		if u.ID == id {
			v.Unit = &units.Items[i]
		}
	}
	if v.Unit == nil {
		p.notFound(w, r, s)
		return
	}
	for _, u := range units.Items {
		if u.ID != id && !strings.HasPrefix(u.Path, v.Unit.Path+"/") {
			v.Parents = append(v.Parents, u)
		}
	}
	p.render(w, r, http.StatusOK, "unit", s, pageData{Title: v.Unit.Path, Nav: "units", Data: v})
}

func (p *Panel) unitCreate(w http.ResponseWriter, r *http.Request, s *session) {
	back := safeReturn(r.PostFormValue("volta"))
	if back == "/" {
		back = "/units"
	}
	parent, err := formUUID(r, "parent")
	if err != nil {
		p.failed(w, r, s, back, errBadRequest(msg("error.bad_unit")))
		return
	}
	req := api.UnitCreate{ParentID: parent, Kind: strings.TrimSpace(r.PostFormValue("kind")), Name: strings.TrimSpace(r.PostFormValue("name"))}
	if _, err := p.api.AddUnit(ctxWithActor(r, s), s.Actor, req); err != nil {
		p.failed(w, r, s, back, err)
		return
	}
	p.done(w, r, back, msg("units.created", req.Name))
}

func (p *Panel) unitUpdate(w http.ResponseWriter, r *http.Request, s *session) {
	id, ok := pathUUID(r)
	if !ok {
		p.notFound(w, r, s)
		return
	}
	back := "/units/" + id.String()
	name := strings.TrimSpace(r.PostFormValue("name"))
	kind := strings.TrimSpace(r.PostFormValue("kind"))
	parent := []byte("null")
	if v := strings.TrimSpace(r.PostFormValue("parent")); v != "" {
		pid, err := uuid.Parse(v)
		if err != nil {
			p.failed(w, r, s, back, errBadRequest(msg("error.bad_unit")))
			return
		}
		parent = []byte(strconv.Quote(pid.String()))
	}
	req := api.UnitUpdate{Name: &name, Kind: &kind, ParentID: parent}
	if _, err := p.api.UpdateUnit(ctxWithActor(r, s), s.Actor, id, req); err != nil {
		if status, _, _ := api.ErrorMessage(err); status == http.StatusBadRequest && strings.Contains(err.Error(), "nada a mudar") {
			p.done(w, r, back, msg("units.nothing"))
			return
		}
		p.failed(w, r, s, back, err)
		return
	}
	p.done(w, r, back, msg("units.saved"))
}

func (p *Panel) unitDelete(w http.ResponseWriter, r *http.Request, s *session) {
	id, ok := pathUUID(r)
	if !ok {
		p.notFound(w, r, s)
		return
	}
	if _, err := p.api.DeleteUnit(ctxWithActor(r, s), s.Actor, id); err != nil {
		p.failed(w, r, s, "/units/"+id.String(), err)
		return
	}
	p.done(w, r, "/units", msg("units.deleted"))
}

// ------------------------------------------------------------------ software

type softwareView struct {
	Units   []api.Unit
	Product string
	Unit    string
	List    api.SoftwareList
	Next    string
	Paged   bool
}

func (p *Panel) softwarePage(w http.ResponseWriter, r *http.Request, s *session) {
	in := r.URL.Query()
	v := softwareView{Product: strings.TrimSpace(in.Get("product")), Unit: in.Get("unit"), Paged: in.Get("cursor") != ""}
	qs := url.Values{}
	for k, val := range map[string]string{"product": v.Product, "unit": v.Unit, "cursor": in.Get("cursor")} {
		if val != "" {
			qs.Set(k, val)
		}
	}
	var err error
	if v.List, err = p.api.ListSoftware(r.Context(), s.Actor.TenantID, qs); err != nil {
		p.fail(w, r, s, err)
		return
	}
	units, err := p.api.ListUnits(r.Context(), s.Actor.TenantID)
	if err != nil {
		p.fail(w, r, s, err)
		return
	}
	v.Units = units.Items
	if v.List.NextCursor != "" {
		v.Next = "/software?" + url.Values{"product": {v.Product}, "unit": {v.Unit}, "cursor": {v.List.NextCursor}}.Encode()
	}
	p.render(w, r, http.StatusOK, "software", s, pageData{Title: msg("nav.software"), Nav: "software", Data: v})
}

type productView struct {
	Units   []api.Unit
	Unit    string
	Version string
	List    api.InstallationList
	Next    string
}

func (p *Panel) productPage(w http.ResponseWriter, r *http.Request, s *session) {
	id, ok := pathUUID(r)
	if !ok {
		p.notFound(w, r, s)
		return
	}
	in := r.URL.Query()
	v := productView{Unit: in.Get("unit"), Version: in.Get("version_id")}
	qs := url.Values{}
	for k, val := range map[string]string{"unit": v.Unit, "version_id": v.Version, "cursor": in.Get("cursor")} {
		if val != "" {
			qs.Set(k, val)
		}
	}
	var err error
	if v.List, err = p.api.SoftwareInstallations(r.Context(), s.Actor.TenantID, id, qs); err != nil {
		if status, _, _ := api.ErrorMessage(err); status == http.StatusNotFound {
			p.notFound(w, r, s)
			return
		}
		p.fail(w, r, s, err)
		return
	}
	units, err := p.api.ListUnits(r.Context(), s.Actor.TenantID)
	if err != nil {
		p.fail(w, r, s, err)
		return
	}
	v.Units = units.Items
	if v.List.NextCursor != "" {
		v.Next = "/software/" + id.String() + "?" + url.Values{"unit": {v.Unit}, "version_id": {v.Version}, "cursor": {v.List.NextCursor}}.Encode()
	}
	p.render(w, r, http.StatusOK, "product", s, pageData{Title: v.List.Software.Product, Nav: "software", Data: v})
}

type unmappedView struct {
	Back     string // esta página, para voltar a ela depois de identificar
	Source   string
	List     api.UnmappedList
	Next     string
	Paged    bool
	Mappings []store.ListTenantManualMappingsRow
}

func (p *Panel) unmappedPage(w http.ResponseWriter, r *http.Request, s *session) {
	in := r.URL.Query()
	v := unmappedView{Source: in.Get("source"), Paged: in.Get("cursor") != "", Back: r.URL.RequestURI()}
	qs := url.Values{}
	if v.Source != "" {
		qs.Set("source", v.Source)
	}
	if c := in.Get("cursor"); c != "" {
		qs.Set("cursor", c)
	}
	var err error
	if v.List, err = p.api.ListUnmapped(r.Context(), s.Actor.TenantID, qs); err != nil {
		p.fail(w, r, s, err)
		return
	}
	if v.List.NextCursor != "" {
		v.Next = "/software/unmapped?" + url.Values{"source": {v.Source}, "cursor": {v.List.NextCursor}}.Encode()
	}
	if v.Mappings, err = p.q.ListTenantManualMappings(r.Context(), s.Actor.TenantID); err != nil {
		p.fail(w, r, s, err)
		return
	}
	p.render(w, r, http.StatusOK, "unmapped", s, pageData{Title: msg("software.unmapped"), Nav: "software", Data: v})
}

func (p *Panel) rawMap(w http.ResponseWriter, r *http.Request, s *session) {
	id, ok := pathUUID(r)
	if !ok {
		p.notFound(w, r, s)
		return
	}
	back := safeReturn(r.PostFormValue("volta"))
	if !strings.HasPrefix(back, "/software/unmapped") {
		back = "/software/unmapped"
	}
	req := api.MapRequest{Vendor: strings.TrimSpace(r.PostFormValue("vendor")), Product: strings.TrimSpace(r.PostFormValue("product"))}
	if _, err := p.api.MapRaw(ctxWithActor(r, s), s.Actor, id, req); err != nil {
		p.failed(w, r, s, back, err)
		return
	}
	p.done(w, r, back, msg("software.mapped", req.Product))
}

func (p *Panel) rawUnmap(w http.ResponseWriter, r *http.Request, s *session) {
	id, ok := pathUUID(r)
	if !ok {
		p.notFound(w, r, s)
		return
	}
	if _, err := p.api.UnmapRaw(ctxWithActor(r, s), s.Actor, id); err != nil {
		p.failed(w, r, s, "/software/unmapped", err)
		return
	}
	p.done(w, r, "/software/unmapped", msg("software.unmapped_done"))
}

// -------------------------------------------------------------------- acesso

type accessView struct {
	Users  []store.ListPanelUsersRow
	Tokens []store.ListTenantAPITokensRow
	New    *api.CreatedAPIToken
	Now    time.Time
}

func (p *Panel) accessData(r *http.Request, s *session) (accessView, error) {
	v := accessView{Now: p.now()}
	var err error
	if v.Users, err = p.q.ListPanelUsers(r.Context(), s.Actor.TenantID); err != nil {
		return v, err
	}
	v.Tokens, err = p.q.ListTenantAPITokens(r.Context(), s.Actor.TenantID)
	return v, err
}

func (p *Panel) accessPage(w http.ResponseWriter, r *http.Request, s *session) {
	v, err := p.accessData(r, s)
	if err != nil {
		p.fail(w, r, s, err)
		return
	}
	p.render(w, r, http.StatusOK, "access", s, pageData{Title: msg("nav.access"), Nav: "access", Data: v})
}

func (p *Panel) userCreate(w http.ResponseWriter, r *http.Request, s *session) {
	if r.PostFormValue("password") != r.PostFormValue("password2") {
		p.failed(w, r, s, "/access", errBadRequest(msg("account.mismatch")))
		return
	}
	u := api.NewPanelUser{
		Name: r.PostFormValue("name"), Email: r.PostFormValue("email"), Password: r.PostFormValue("password"),
		Admin: r.PostFormValue("scope") == "admin",
	}
	if _, err := p.api.CreatePanelUser(ctxWithActor(r, s), s.Actor, u); err != nil {
		p.failed(w, r, s, "/access", err)
		return
	}
	p.done(w, r, "/access", msg("access.user_created", strings.TrimSpace(u.Name)))
}

func (p *Panel) actorDisable(w http.ResponseWriter, r *http.Request, s *session) {
	id, ok := pathUUID(r)
	if !ok {
		p.notFound(w, r, s)
		return
	}
	if err := p.api.DisableActor(ctxWithActor(r, s), s.Actor, id); err != nil {
		p.failed(w, r, s, "/access", err)
		return
	}
	p.done(w, r, "/access", msg("access.disabled"))
}

var apiTTLs = map[string]time.Duration{"": 0, "0": 0, "720h": 30 * 24 * time.Hour, "2160h": 90 * 24 * time.Hour, "8760h": 365 * 24 * time.Hour}

func (p *Panel) apiTokenCreate(w http.ResponseWriter, r *http.Request, s *session) {
	ttl, ok := apiTTLs[r.PostFormValue("ttl")]
	if !ok {
		p.failed(w, r, s, "/access", errBadRequest(msg("enroll.bad_ttl")))
		return
	}
	c := api.NewAPIClient{
		Name: r.PostFormValue("name"), Kind: r.PostFormValue("kind"), Admin: r.PostFormValue("scope") == "admin",
		Description: strings.TrimSpace(r.PostFormValue("description")), TTL: ttl,
	}
	tok, err := p.api.CreateAPIClient(ctxWithActor(r, s), s.Actor, c)
	if err != nil {
		p.failed(w, r, s, "/access", err)
		return
	}
	v, err := p.accessData(r, s)
	if err != nil {
		p.fail(w, r, s, err)
		return
	}
	v.New = &tok
	p.render(w, r, http.StatusOK, "access", s, pageData{Title: msg("nav.access"), Nav: "access", Data: v})
}

func (p *Panel) apiTokenRevoke(w http.ResponseWriter, r *http.Request, s *session) {
	id, ok := pathUUID(r)
	if !ok {
		p.notFound(w, r, s)
		return
	}
	if err := p.api.RevokeAPIToken(ctxWithActor(r, s), s.Actor, id); err != nil {
		p.failed(w, r, s, "/access", err)
		return
	}
	p.done(w, r, "/access", msg("access.token_revoked"))
}

// ------------------------------------------------------------------ exportar

// exportKeys guarda, por alguns minutos, a chave de exportação mostrada na
// tela até o download que a usa: o navegador não lê o cabeçalho
// Invenire-Export-Key da API, então a chave aparece na página e o pacote
// vem num link de uso único, só para a mesma sessão.
type exportKeys struct {
	mu sync.Mutex
	m  map[string]exportKey
}

type exportKey struct {
	key     []byte
	actor   uuid.UUID
	expires time.Time
}

func (e *exportKeys) put(actor uuid.UUID, key []byte, now time.Time) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, k := range e.m {
		if now.After(k.expires) {
			delete(e.m, id)
		}
	}
	id := newSessionID()
	e.m[id] = exportKey{key: key, actor: actor, expires: now.Add(10 * time.Minute)}
	return id
}

func (e *exportKeys) take(id string, actor uuid.UUID, now time.Time) ([]byte, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	k, ok := e.m[id]
	if !ok || k.actor != actor || now.After(k.expires) {
		return nil, false
	}
	delete(e.m, id)
	return k.key, true
}

type exportView struct {
	Key      string
	Download string
}

func (p *Panel) exportPage(w http.ResponseWriter, r *http.Request, s *session) {
	p.render(w, r, http.StatusOK, "export", s, pageData{Title: msg("nav.export"), Nav: "export", Data: exportView{}})
}

func (p *Panel) exportStart(w http.ResponseWriter, r *http.Request, s *session) {
	if r.PostFormValue("with_ca_key") != "1" {
		p.api.ExportTenant(w, r, s.Actor, nil, "panel")
		return
	}
	key := export.NewExportKey()
	id := p.exports.put(s.Actor.ID, key, p.now())
	v := exportView{Key: base64.StdEncoding.EncodeToString(key), Download: "/export/download/" + id}
	p.render(w, r, http.StatusOK, "export", s, pageData{Title: msg("nav.export"), Nav: "export", Data: v})
}

func (p *Panel) exportDownload(w http.ResponseWriter, r *http.Request, s *session) {
	key, ok := p.exports.take(r.PathValue("id"), s.Actor.ID, p.now())
	if !ok {
		p.failed(w, r, s, "/export", errBadRequest(msg("export.link_used")))
		return
	}
	p.api.ExportTenant(w, r, s.Actor, key, "panel")
}

// ------------------------------------------------------------- minha conta

func (p *Panel) accountPage(w http.ResponseWriter, r *http.Request, s *session) {
	p.render(w, r, http.StatusOK, "account", s, pageData{Title: msg("nav.account"), Nav: "account", Data: password.MinLength})
}

func (p *Panel) accountPassword(w http.ResponseWriter, r *http.Request, s *session) {
	login, err := p.q.GetLoginByEmail(r.Context(), s.Email)
	if err != nil {
		p.fail(w, r, s, err)
		return
	}
	key := "email:" + s.Email
	if !p.loginLimit.allow(key) {
		p.failed(w, r, s, "/account", errBadRequest(msg("login.too_many")))
		return
	}
	if ok, _ := password.Verify(login.PasswordHash, r.PostFormValue("current")); !ok {
		p.loginLimit.fail(key)
		p.failed(w, r, s, "/account", errBadRequest(msg("account.wrong_current")))
		return
	}
	if r.PostFormValue("password") != r.PostFormValue("password2") {
		p.failed(w, r, s, "/account", errBadRequest(msg("account.mismatch")))
		return
	}
	if err := api.SetPassword(r.Context(), p.api.Pool(), s.Email, r.PostFormValue("password"), &s.Actor.ID); err != nil {
		p.failed(w, r, s, "/account", err)
		return
	}
	// As sessões do login caíram todas; esta continua, com identificador novo.
	if err := p.startSession(w, r, s.LoginID, s.Actor.ID); err != nil {
		p.fail(w, r, s, err)
		return
	}
	p.done(w, r, "/account", msg("account.changed"))
}

// errBadRequest é um erro de quem pediu, com a mensagem do catálogo.
func errBadRequest(text string) error { return api.BadRequest(text) }
