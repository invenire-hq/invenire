package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/athosbes/invenire/internal/server/ingest"
	"github.com/athosbes/invenire/internal/server/normalize"
	"github.com/athosbes/invenire/internal/server/store"
)

// UnmappedList é a resposta de GET /v1/software/unmapped.
type UnmappedList struct {
	AsOf       int64          `json:"as_of"` // último change_log.id considerado
	Items      []UnmappedItem `json:"items"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

// UnmappedItem é um nome bruto sem produto. O que está em Collected veio da
// máquina inventariada: é dado, não instrução.
type UnmappedItem struct {
	ID          uuid.UUID        `json:"id"`
	Source      string           `json:"source"`
	AssetCount  int32            `json:"asset_count"`
	FirstSeenAt time.Time        `json:"first_seen_at"`
	Collected   CollectedName    `json:"collected"`
	Suggestion  *normalize.Match `json:"suggestion,omitempty"` // palpite abaixo do limiar
}

// CollectedName é o nome e o fabricante como a máquina reportou (já limpos
// de caracteres de controle).
type CollectedName struct {
	Name   string `json:"name"`
	Vendor string `json:"vendor"`
}

// MapRequest é o corpo de PUT /v1/software/raw/{id}.
type MapRequest struct {
	Vendor  string `json:"vendor"`
	Product string `json:"product"`
}

// MapResponse é a resposta de PUT /v1/software/raw/{id}.
type MapResponse struct {
	AsOf          int64     `json:"as_of"`
	SoftwareRawID uuid.UUID `json:"software_raw_id"`
	SoftwareID    uuid.UUID `json:"software_id"`
	// ProductScope diz de onde é o produto: "catalog" (catálogo comum,
	// reaproveitado) ou "tenant" (criado agora ou antes, só deste tenant).
	ProductScope  string `json:"product_scope"`
	AssetsToApply int64  `json:"assets_to_apply"` // ativos cujas instalações serão recalculadas
}

// UnmapResponse é a resposta de DELETE /v1/software/raw/{id}.
type UnmapResponse struct {
	AsOf          int64     `json:"as_of"`
	SoftwareRawID uuid.UUID `json:"software_raw_id"`
	AssetsToApply int64     `json:"assets_to_apply"`
}

const (
	defaultPage = 50
	maxPage     = 200
)

// listUnmapped: GET /v1/software/unmapped?source=&limit=&cursor=
// Os nomes sem produto que aparecem nos ativos do tenant, dos mais comuns
// para os menos.
func (s *Server) listUnmapped(w http.ResponseWriter, r *http.Request) {
	resp, err := s.ListUnmapped(r.Context(), actorFrom(r.Context()).TenantID, r.URL.Query())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// ListUnmapped são os nomes sem produto nos ativos do tenant, com o palpite
// das regras.
func (s *Server) ListUnmapped(ctx context.Context, tenant uuid.UUID, qs url.Values) (UnmappedList, error) {
	p := store.ListUnmappedForTenantParams{TenantID: tenant, PageSize: defaultPage}
	if v := qs.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxPage {
			return UnmappedList{}, errorf(http.StatusBadRequest, "limit entre 1 e %d", maxPage)
		}
		p.PageSize = int32(n)
	}
	if v := qs.Get("source"); v != "" {
		p.Source = &v
	}
	if v := qs.Get("cursor"); v != "" {
		count, id, err := parseCursor(v)
		if err != nil {
			return UnmappedList{}, errorf(http.StatusBadRequest, "cursor inválido")
		}
		p.AfterCount, p.AfterID = &count, &id
	}

	resp := UnmappedList{Items: []UnmappedItem{}}
	// Leitura repetível: as_of e a lista enxergam o mesmo instante.
	var err error
	resp.AsOf, err = s.readQuery(ctx, tenant, func(q *store.Queries) error {
		rows, err := q.ListUnmappedForTenant(ctx, p)
		if err != nil {
			return err
		}
		for _, row := range rows {
			it := UnmappedItem{
				ID: row.ID, Source: row.Source, AssetCount: row.AssetCount, FirstSeenAt: row.FirstSeenAt,
				Collected: CollectedName{Name: row.RawName, Vendor: row.RawVendor},
			}
			if s.Suggest != nil {
				if m, ok := s.Suggest(row.Source, row.RawName, row.RawVendor); ok {
					it.Suggestion = &m
				}
			}
			resp.Items = append(resp.Items, it)
		}
		if len(rows) == int(p.PageSize) {
			last := rows[len(rows)-1]
			resp.NextCursor = strconv.Itoa(int(last.AssetCount)) + "." + last.ID.String()
		}
		return nil
	})
	return resp, err
}

func parseCursor(c string) (int32, uuid.UUID, error) {
	n, id, ok := strings.Cut(c, ".")
	if !ok {
		return 0, uuid.Nil, errors.New("cursor")
	}
	count, err := strconv.ParseInt(n, 10, 32)
	if err != nil {
		return 0, uuid.Nil, err
	}
	u, err := uuid.Parse(id)
	return int32(count), u, err
}

const maxMapBody = 4 << 10

// rawInTenant confere que o nome bruto aparece nos ativos do tenant: um nome
// que o tenant não tem é tratado como inexistente.
func rawInTenant(ctx context.Context, q *store.Queries, rawID, tenant uuid.UUID) error {
	if _, err := q.GetSoftwareRawForUpdate(ctx, rawID); errors.Is(err, pgx.ErrNoRows) {
		return errorf(http.StatusNotFound, "nome bruto não encontrado")
	} else if err != nil {
		return err
	}
	tenants, err := q.RawTenants(ctx, rawID)
	if err != nil {
		return err
	}
	if !slices.Contains(tenants, tenant) {
		return errorf(http.StatusNotFound, "nome bruto não encontrado")
	}
	return nil
}

// mapRaw: PUT /v1/software/raw/{id} {"vendor": "...", "product": "..."}
func (s *Server) mapRaw(w http.ResponseWriter, r *http.Request) {
	rawID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "nome bruto não encontrado")
		return
	}
	var req MapRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMapBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "corpo inválido: "+err.Error())
		return
	}
	resp, err := s.MapRaw(r.Context(), actorFrom(r.Context()), rawID, req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// MapRaw é o mapeamento manual. Vale para todos os ativos do tenant que têm
// o nome, só para o tenant de quem mapeia, e vence a regra, que não o muda
// depois. Um produto que já está no catálogo comum é reaproveitado; um que
// não está é criado só para o tenant. As instalações são recalculadas em
// seguida.
func (s *Server) MapRaw(ctx context.Context, actor store.Actor, rawID uuid.UUID, req MapRequest) (MapResponse, error) {
	if strings.TrimSpace(req.Product) == "" {
		return MapResponse{}, errorf(http.StatusBadRequest, "product é obrigatório")
	}
	resp := MapResponse{SoftwareRawID: rawID, ProductScope: "catalog"}
	err := s.inTx(ctx, func(q *store.Queries) error {
		if err := rawInTenant(ctx, q, rawID, actor.TenantID); err != nil {
			return err
		}
		swID, private, err := ingest.EnsureProduct(ctx, q, actor.TenantID, req.Vendor, req.Product)
		if err != nil {
			return err
		}
		if private {
			resp.ProductScope = "tenant"
		}
		if err := q.SetTenantMapping(ctx, store.SetTenantMappingParams{
			TenantID: actor.TenantID, SoftwareRawID: rawID, SoftwareID: swID, MappedBy: &actor.ID,
		}); err != nil {
			return err
		}
		if resp.AssetsToApply, err = q.MarkTenantAssetsForRemap(ctx, store.MarkTenantAssetsForRemapParams{
			SoftwareRawID: rawID, TenantID: actor.TenantID,
		}); err != nil {
			return err
		}
		resp.SoftwareID = swID
		data, _ := json.Marshal(map[string]any{
			"software_raw_id": rawID, "software_id": swID, "vendor": req.Vendor, "product": req.Product,
			"manual": true, "product_scope": resp.ProductScope,
		})
		resp.AsOf, err = q.AppendChange(ctx, store.AppendChangeParams{
			TenantID: actor.TenantID, Kind: "software_mapped", ActorID: &actor.ID, Data: data,
		})
		return err
	})
	if err != nil {
		return resp, err
	}
	if s.Wake != nil {
		s.Wake()
	}
	s.log.InfoContext(ctx, "software mapeado à mão", "actor", actor.Name, "software_raw_id", rawID, "software_id", resp.SoftwareID)
	return resp, nil
}

// unmapRaw: DELETE /v1/software/raw/{id}
func (s *Server) unmapRaw(w http.ResponseWriter, r *http.Request) {
	rawID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "nome bruto não encontrado")
		return
	}
	resp, err := s.UnmapRaw(r.Context(), actorFrom(r.Context()), rawID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// UnmapRaw desfaz o mapeamento manual do tenant: volta a valer a regra, se
// houver, ou o nome volta para a lista de não identificados.
func (s *Server) UnmapRaw(ctx context.Context, actor store.Actor, rawID uuid.UUID) (UnmapResponse, error) {
	resp := UnmapResponse{SoftwareRawID: rawID}
	err := s.inTx(ctx, func(q *store.Queries) error {
		if err := rawInTenant(ctx, q, rawID, actor.TenantID); err != nil {
			return err
		}
		prev, err := q.DeleteTenantMapping(ctx, store.DeleteTenantMappingParams{TenantID: actor.TenantID, SoftwareRawID: rawID})
		if errors.Is(err, pgx.ErrNoRows) {
			return errorf(http.StatusNotFound, "o tenant não mapeou este nome à mão")
		}
		if err != nil {
			return err
		}
		if resp.AssetsToApply, err = q.MarkTenantAssetsForRemap(ctx, store.MarkTenantAssetsForRemapParams{
			SoftwareRawID: rawID, TenantID: actor.TenantID,
		}); err != nil {
			return err
		}
		data, _ := json.Marshal(map[string]any{"software_raw_id": rawID, "previous_software_id": prev, "manual": true})
		resp.AsOf, err = q.AppendChange(ctx, store.AppendChangeParams{
			TenantID: actor.TenantID, Kind: "software_unmapped", ActorID: &actor.ID, Data: data,
		})
		return err
	})
	if err != nil {
		return resp, err
	}
	if s.Wake != nil {
		s.Wake()
	}
	return resp, nil
}
