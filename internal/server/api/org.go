package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/athosbes/invenire/internal/server/store"
)

// Unidades: uma árvore livre dentro do tenant (Saúde → UBS Centro →
// Recepção). Cada ativo está em no máximo uma unidade; mudar de unidade é um
// evento no change_log. Etiquetas são as dimensões que não são hierarquia.

// Unit é uma unidade como a API a devolve.
type Unit struct {
	ID       uuid.UUID  `json:"id"`
	ParentID *uuid.UUID `json:"parent_id"`
	Kind     string     `json:"kind"`
	Name     string     `json:"name"`
	// Path é o caminho desde a raiz, com "/" entre os níveis.
	Path       string    `json:"path"`
	Source     string    `json:"source"`
	AssetCount int32     `json:"asset_count"` // ativos ligados direto a ela
	CreatedAt  time.Time `json:"created_at"`
}

// UnitList é a resposta de GET /v1/units.
type UnitList struct {
	AsOf  int64  `json:"as_of"`
	Items []Unit `json:"items"`
}

// UnitCreate é o corpo de POST /v1/units. A unidade-mãe vem por parent_id
// ou parent_path; sem nenhum dos dois, a unidade é raiz.
type UnitCreate struct {
	ParentID   *uuid.UUID `json:"parent_id,omitempty"`
	ParentPath string     `json:"parent_path,omitempty"`
	Kind       string     `json:"kind"`
	Name       string     `json:"name"`
}

// UnitUpdate é o corpo de PATCH /v1/units/{id}. Campo ausente não muda;
// "parent_id": null move a unidade para a raiz.
type UnitUpdate struct {
	Name     *string         `json:"name,omitempty"`
	Kind     *string         `json:"kind,omitempty"`
	ParentID json.RawMessage `json:"parent_id,omitempty"`
}

// UnitResponse é a resposta de quem cria ou muda uma unidade.
type UnitResponse struct {
	AsOf int64 `json:"as_of"`
	Unit Unit  `json:"unit"`
}

// AssetUnitRequest é o corpo de PUT /v1/assets/{id}/unit; unit_id nulo
// tira o ativo de qualquer unidade.
type AssetUnitRequest struct {
	UnitID *uuid.UUID `json:"unit_id"`
}

// AssetUnitResponse é a resposta de PUT /v1/assets/{id}/unit.
type AssetUnitResponse struct {
	AsOf           int64      `json:"as_of"`
	AssetID        uuid.UUID  `json:"asset_id"`
	UnitID         *uuid.UUID `json:"unit_id"`
	PreviousUnitID *uuid.UUID `json:"previous_unit_id"`
}

// LabelRequest é o corpo de PUT /v1/assets/{id}/labels/{key}.
type LabelRequest struct {
	Value string `json:"value"`
}

// LabelResponse é a resposta de PUT e DELETE /v1/assets/{id}/labels/{key}.
type LabelResponse struct {
	AsOf          int64     `json:"as_of"`
	AssetID       uuid.UUID `json:"asset_id"`
	Key           string    `json:"key"`
	Value         *string   `json:"value"` // nulo depois do DELETE
	PreviousValue *string   `json:"previous_value"`
}

// AssetLabels é a resposta de GET /v1/assets/{id}/labels.
type AssetLabels struct {
	AssetID uuid.UUID         `json:"asset_id"`
	Labels  map[string]string `json:"labels"`
}

const maxOrgBody = 4 << 10

var labelKey = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)

// ValidUnitName confere o nome de uma unidade: sem "/" (separa os níveis do
// caminho), sem espaço nas pontas, até 120 caracteres.
func ValidUnitName(name string) error {
	switch {
	case name == "" || name != strings.TrimSpace(name):
		return errors.New("nome vazio ou com espaço nas pontas")
	case strings.Contains(name, "/"):
		return errors.New(`nome não pode ter "/": ele separa os níveis do caminho`)
	case utf8.RuneCountInString(name) > 120:
		return errors.New("nome com mais de 120 caracteres")
	}
	return nil
}

// ResolveUnitPath acha a unidade pelo caminho ("Saúde/UBS Centro").
func ResolveUnitPath(ctx context.Context, q *store.Queries, tenant uuid.UUID, path string) (store.OrgUnit, error) {
	var u store.OrgUnit
	var parent *uuid.UUID
	for _, name := range strings.Split(strings.Trim(path, "/"), "/") {
		var err error
		u, err = q.GetUnitByName(ctx, store.GetUnitByNameParams{TenantID: tenant, ParentID: parent, Name: strings.TrimSpace(name)})
		if errors.Is(err, pgx.ErrNoRows) {
			return u, errorf(http.StatusNotFound, "unidade %q não encontrada", path)
		}
		if err != nil {
			return u, err
		}
		parent = &u.ID
	}
	return u, nil
}

// CreateUnit cria a unidade e registra unit_created. actor pode ser nulo
// (linha de comando).
func CreateUnit(ctx context.Context, q *store.Queries, tenant uuid.UUID, actor *uuid.UUID, parent *uuid.UUID, kind, name string) (store.OrgUnit, int64, error) {
	if err := ValidUnitName(name); err != nil {
		return store.OrgUnit{}, 0, errorf(http.StatusBadRequest, "%s", err)
	}
	if utf8.RuneCountInString(kind) > 60 {
		return store.OrgUnit{}, 0, errorf(http.StatusBadRequest, "kind com mais de 60 caracteres")
	}
	if err := q.LockUnitTree(ctx, tenant); err != nil {
		return store.OrgUnit{}, 0, err
	}
	if parent != nil {
		if _, err := q.GetUnit(ctx, store.GetUnitParams{ID: *parent, TenantID: tenant}); errors.Is(err, pgx.ErrNoRows) {
			return store.OrgUnit{}, 0, errorf(http.StatusNotFound, "unidade-mãe não encontrada")
		} else if err != nil {
			return store.OrgUnit{}, 0, err
		}
	}
	if _, err := q.GetUnitByName(ctx, store.GetUnitByNameParams{TenantID: tenant, ParentID: parent, Name: name}); err == nil {
		return store.OrgUnit{}, 0, errorf(http.StatusConflict, "já existe a unidade %q nesse nível", name)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return store.OrgUnit{}, 0, err
	}
	u, err := q.CreateUnit(ctx, store.CreateUnitParams{TenantID: tenant, ParentID: parent, Kind: kind, Name: name})
	if err != nil {
		return u, 0, err
	}
	data, _ := json.Marshal(map[string]any{"unit_id": u.ID, "parent_id": parent, "kind": kind, "name": name})
	asOf, err := q.AppendChange(ctx, store.AppendChangeParams{TenantID: tenant, Kind: "unit_created", ActorID: actor, Data: data})
	return u, asOf, err
}

// unitView devolve a unidade com caminho e contagem, lendo a árvore.
func unitView(ctx context.Context, q *store.Queries, tenant, id uuid.UUID) (Unit, error) {
	rows, err := q.ListUnits(ctx, tenant)
	if err != nil {
		return Unit{}, err
	}
	for _, r := range rows {
		if r.ID == id {
			return unitFromRow(r), nil
		}
	}
	return Unit{}, errors.New("unidade sumiu da árvore")
}

func unitFromRow(r store.ListUnitsRow) Unit {
	return Unit{ID: r.ID, ParentID: r.ParentID, Kind: r.Kind, Name: r.Name, Path: r.Path,
		Source: r.Source, AssetCount: r.AssetCount, CreatedAt: r.CreatedAt}
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxOrgBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "corpo inválido: "+err.Error())
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request, what string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, what+" não encontrado")
	}
	return id, err == nil
}

// listUnits: GET /v1/units, a árvore inteira em ordem de caminho.
func (s *Server) listUnits(w http.ResponseWriter, r *http.Request) {
	actor := actorFrom(r.Context())
	tx, err := s.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	q := s.q.WithTx(tx)
	resp := UnitList{Items: []Unit{}}
	if resp.AsOf, err = q.LatestChangeID(r.Context(), actor.TenantID); err != nil {
		s.internalError(w, r, err)
		return
	}
	rows, err := q.ListUnits(r.Context(), actor.TenantID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	for _, row := range rows {
		resp.Items = append(resp.Items, unitFromRow(row))
	}
	writeJSON(w, http.StatusOK, resp)
}

// createUnit: POST /v1/units.
func (s *Server) createUnit(w http.ResponseWriter, r *http.Request) {
	actor := actorFrom(r.Context())
	var req UnitCreate
	if !decodeBody(w, r, &req) {
		return
	}
	if req.ParentID != nil && req.ParentPath != "" {
		writeError(w, http.StatusBadRequest, "use parent_id ou parent_path, não os dois")
		return
	}
	var resp UnitResponse
	err := s.inTx(r.Context(), func(q *store.Queries) error {
		parent := req.ParentID
		if req.ParentPath != "" {
			p, err := ResolveUnitPath(r.Context(), q, actor.TenantID, req.ParentPath)
			if err != nil {
				return err
			}
			parent = &p.ID
		}
		u, asOf, err := CreateUnit(r.Context(), q, actor.TenantID, &actor.ID, parent, req.Kind, req.Name)
		if err != nil {
			return err
		}
		resp.AsOf = asOf
		resp.Unit, err = unitView(r.Context(), q, actor.TenantID, u.ID)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, resp)
}

// updateUnit: PATCH /v1/units/{id}, renomeia, muda o tipo ou move a
// unidade (com tudo o que está abaixo dela).
func (s *Server) updateUnit(w http.ResponseWriter, r *http.Request) {
	actor := actorFrom(r.Context())
	id, ok := pathID(w, r, "unidade")
	if !ok {
		return
	}
	var req UnitUpdate
	if !decodeBody(w, r, &req) {
		return
	}
	var resp UnitResponse
	err := s.inTx(r.Context(), func(q *store.Queries) error {
		if err := q.LockUnitTree(r.Context(), actor.TenantID); err != nil {
			return err
		}
		u, err := q.GetUnitForUpdate(r.Context(), store.GetUnitForUpdateParams{ID: id, TenantID: actor.TenantID})
		if errors.Is(err, pgx.ErrNoRows) {
			return errorf(http.StatusNotFound, "unidade não encontrada")
		}
		if err != nil {
			return err
		}
		p := store.UpdateUnitParams{ID: id, TenantID: actor.TenantID, ParentID: u.ParentID, Kind: u.Kind, Name: u.Name}
		change := map[string]any{"unit_id": id}
		if req.Name != nil && *req.Name != u.Name {
			if err := ValidUnitName(*req.Name); err != nil {
				return errorf(http.StatusBadRequest, "%s", err)
			}
			p.Name = *req.Name
			change["name"], change["previous_name"] = p.Name, u.Name
		}
		if req.Kind != nil && *req.Kind != u.Kind {
			if utf8.RuneCountInString(*req.Kind) > 60 {
				return errorf(http.StatusBadRequest, "kind com mais de 60 caracteres")
			}
			p.Kind = *req.Kind
			change["kind"], change["previous_kind"] = p.Kind, u.Kind
		}
		if len(req.ParentID) > 0 {
			var parent *uuid.UUID
			if !bytes.Equal(req.ParentID, []byte("null")) {
				parent = new(uuid.UUID)
				if err := json.Unmarshal(req.ParentID, parent); err != nil {
					return errorf(http.StatusBadRequest, "parent_id inválido")
				}
			}
			if !sameID(parent, u.ParentID) {
				if parent != nil {
					if err := s.checkNoCycle(r.Context(), q, actor.TenantID, id, *parent); err != nil {
						return err
					}
				}
				p.ParentID = parent
				change["parent_id"], change["previous_parent_id"] = parent, u.ParentID
			}
		}
		if len(change) == 1 {
			return errorf(http.StatusBadRequest, "nada a mudar")
		}
		if p.Name != u.Name || !sameID(p.ParentID, u.ParentID) {
			if other, err := q.GetUnitByName(r.Context(), store.GetUnitByNameParams{TenantID: actor.TenantID, ParentID: p.ParentID, Name: p.Name}); err == nil && other.ID != id {
				return errorf(http.StatusConflict, "já existe a unidade %q nesse nível", p.Name)
			} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if _, err := q.UpdateUnit(r.Context(), p); err != nil {
			return err
		}
		data, _ := json.Marshal(change)
		if resp.AsOf, err = q.AppendChange(r.Context(), store.AppendChangeParams{
			TenantID: actor.TenantID, Kind: "unit_updated", ActorID: &actor.ID, Data: data,
		}); err != nil {
			return err
		}
		resp.Unit, err = unitView(r.Context(), q, actor.TenantID, id)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// checkNoCycle confere que a nova mãe existe no tenant e não está abaixo da
// unidade movida (nem é ela mesma). A árvore está travada por LockUnitTree.
func (s *Server) checkNoCycle(ctx context.Context, q *store.Queries, tenant, unit, parent uuid.UUID) error {
	cur := &parent
	for depth := 0; cur != nil; depth++ {
		if *cur == unit {
			return errorf(http.StatusConflict, "a unidade não pode ficar dentro dela mesma ou de uma subunidade")
		}
		if depth > 1000 {
			return errors.New("árvore de unidades com ciclo")
		}
		u, err := q.GetUnit(ctx, store.GetUnitParams{ID: *cur, TenantID: tenant})
		if errors.Is(err, pgx.ErrNoRows) {
			return errorf(http.StatusNotFound, "unidade-mãe não encontrada")
		}
		if err != nil {
			return err
		}
		cur = u.ParentID
	}
	return nil
}

func sameID(a, b *uuid.UUID) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// deleteUnit: DELETE /v1/units/{id}, só de unidade vazia: sem subunidades,
// sem ativos e sem token de cadastro ainda utilizável.
func (s *Server) deleteUnit(w http.ResponseWriter, r *http.Request) {
	actor := actorFrom(r.Context())
	id, ok := pathID(w, r, "unidade")
	if !ok {
		return
	}
	var asOf int64
	err := s.inTx(r.Context(), func(q *store.Queries) error {
		if err := q.LockUnitTree(r.Context(), actor.TenantID); err != nil {
			return err
		}
		u, err := q.GetUnitForUpdate(r.Context(), store.GetUnitForUpdateParams{ID: id, TenantID: actor.TenantID})
		if errors.Is(err, pgx.ErrNoRows) {
			return errorf(http.StatusNotFound, "unidade não encontrada")
		}
		if err != nil {
			return err
		}
		// setAssetUnit trava esta mesma linha antes de pôr um ativo aqui:
		// nenhum entra na unidade enquanto ela é contada e apagada.
		use, err := q.UnitUsage(r.Context(), &id)
		if err != nil {
			return err
		}
		if use.Children > 0 || use.Assets > 0 || use.PendingTokens > 0 {
			return errorf(http.StatusConflict, "unidade em uso: %d subunidade(s), %d ativo(s), %d token(s) de cadastro válidos",
				use.Children, use.Assets, use.PendingTokens)
		}
		if err := q.DetachUnitTokens(r.Context(), &id); err != nil {
			return err
		}
		if err := q.DeleteUnit(r.Context(), store.DeleteUnitParams{ID: id, TenantID: actor.TenantID}); err != nil {
			return err
		}
		data, _ := json.Marshal(map[string]any{"unit_id": id, "parent_id": u.ParentID, "kind": u.Kind, "name": u.Name})
		asOf, err = q.AppendChange(r.Context(), store.AppendChangeParams{
			TenantID: actor.TenantID, Kind: "unit_deleted", ActorID: &actor.ID, Data: data,
		})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"as_of": asOf, "unit_id": id})
}

// assetInTenant trava o ativo do tenant ou responde 404.
func assetInTenant(ctx context.Context, q *store.Queries, id, tenant uuid.UUID) (store.Asset, error) {
	a, err := q.GetAssetInTenantForUpdate(ctx, store.GetAssetInTenantForUpdateParams{ID: id, TenantID: tenant})
	if errors.Is(err, pgx.ErrNoRows) {
		return a, errorf(http.StatusNotFound, "ativo não encontrado")
	}
	return a, err
}

// setAssetUnit: PUT /v1/assets/{id}/unit {"unit_id": "…" | null}.
func (s *Server) setAssetUnit(w http.ResponseWriter, r *http.Request) {
	actor := actorFrom(r.Context())
	id, ok := pathID(w, r, "ativo")
	if !ok {
		return
	}
	var req AssetUnitRequest
	if !decodeBody(w, r, &req) {
		return
	}
	resp := AssetUnitResponse{AssetID: id, UnitID: req.UnitID}
	err := s.inTx(r.Context(), func(q *store.Queries) error {
		a, err := assetInTenant(r.Context(), q, id, actor.TenantID)
		if err != nil {
			return err
		}
		resp.PreviousUnitID = a.UnitID
		if req.UnitID != nil {
			// Trava a unidade: um DELETE simultâneo não a apaga com o ativo entrando.
			if _, err := q.GetUnitForUpdate(r.Context(), store.GetUnitForUpdateParams{ID: *req.UnitID, TenantID: actor.TenantID}); errors.Is(err, pgx.ErrNoRows) {
				return errorf(http.StatusNotFound, "unidade não encontrada")
			} else if err != nil {
				return err
			}
		}
		if sameID(req.UnitID, a.UnitID) {
			resp.AsOf, err = q.LatestChangeID(r.Context(), actor.TenantID)
			return err
		}
		if err := q.SetAssetUnit(r.Context(), store.SetAssetUnitParams{ID: id, UnitID: req.UnitID}); err != nil {
			return err
		}
		resp.AsOf, err = appendAssetMoved(r.Context(), q, actor.TenantID, id, &actor.ID, a.UnitID, req.UnitID)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func appendAssetMoved(ctx context.Context, q *store.Queries, tenant, asset uuid.UUID, actor, from, to *uuid.UUID) (int64, error) {
	data, _ := json.Marshal(map[string]any{"from_unit_id": from, "to_unit_id": to})
	return q.AppendChange(ctx, store.AppendChangeParams{TenantID: tenant, Kind: "asset_moved", AssetID: &asset, ActorID: actor, Data: data})
}

// listLabels: GET /v1/assets/{id}/labels.
func (s *Server) listLabels(w http.ResponseWriter, r *http.Request) {
	actor := actorFrom(r.Context())
	id, ok := pathID(w, r, "ativo")
	if !ok {
		return
	}
	if a, err := s.q.GetAsset(r.Context(), id); errors.Is(err, pgx.ErrNoRows) || (err == nil && a.TenantID != actor.TenantID) {
		writeError(w, http.StatusNotFound, "ativo não encontrado")
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}
	rows, err := s.q.ListAssetLabels(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	resp := AssetLabels{AssetID: id, Labels: map[string]string{}}
	for _, l := range rows {
		resp.Labels[l.Key] = l.Value
	}
	writeJSON(w, http.StatusOK, resp)
}

// setLabel: PUT /v1/assets/{id}/labels/{key} {"value": "..."}.
// deleteLabel: DELETE /v1/assets/{id}/labels/{key}.
func (s *Server) setLabel(w http.ResponseWriter, r *http.Request)    { s.changeLabel(w, r, true) }
func (s *Server) deleteLabel(w http.ResponseWriter, r *http.Request) { s.changeLabel(w, r, false) }

func (s *Server) changeLabel(w http.ResponseWriter, r *http.Request, set bool) {
	actor := actorFrom(r.Context())
	id, ok := pathID(w, r, "ativo")
	if !ok {
		return
	}
	key := r.PathValue("key")
	if !labelKey.MatchString(key) {
		writeError(w, http.StatusBadRequest, "chave de etiqueta: minúsculas, dígitos, _ . -, até 63 caracteres, começando por letra ou dígito")
		return
	}
	var req LabelRequest
	if set {
		if !decodeBody(w, r, &req) {
			return
		}
		if utf8.RuneCountInString(req.Value) > 256 || !utf8.ValidString(req.Value) || strings.ContainsRune(req.Value, 0) {
			writeError(w, http.StatusBadRequest, "valor com mais de 256 caracteres ou inválido")
			return
		}
	}
	resp := LabelResponse{AssetID: id, Key: key}
	err := s.inTx(r.Context(), func(q *store.Queries) error {
		if _, err := assetInTenant(r.Context(), q, id, actor.TenantID); err != nil {
			return err
		}
		prev, err := q.GetAssetLabel(r.Context(), store.GetAssetLabelParams{AssetID: id, Key: key})
		if err == nil {
			resp.PreviousValue = &prev.Value
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		kind := "label_set"
		if set {
			resp.Value = &req.Value
			if resp.PreviousValue != nil && *resp.PreviousValue == req.Value {
				resp.AsOf, err = q.LatestChangeID(r.Context(), actor.TenantID)
				return err
			}
			if err := q.SetAssetLabel(r.Context(), store.SetAssetLabelParams{AssetID: id, Key: key, Value: req.Value, SetBy: &actor.ID}); err != nil {
				return err
			}
		} else {
			if resp.PreviousValue == nil {
				return errorf(http.StatusNotFound, "o ativo não tem a etiqueta %q", key)
			}
			if _, err := q.DeleteAssetLabel(r.Context(), store.DeleteAssetLabelParams{AssetID: id, Key: key}); err != nil {
				return err
			}
			kind = "label_removed"
		}
		data, _ := json.Marshal(map[string]any{"key": key, "value": resp.Value, "previous_value": resp.PreviousValue})
		resp.AsOf, err = q.AppendChange(r.Context(), store.AppendChangeParams{
			TenantID: actor.TenantID, Kind: kind, AssetID: &id, ActorID: &actor.ID, Data: data,
		})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
