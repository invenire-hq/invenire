package api

import (
	"encoding/json"
	"errors"
	"net/http"
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
	actor := actorFrom(r.Context())
	p := store.ListUnmappedForTenantParams{TenantID: actor.TenantID, PageSize: defaultPage}
	qs := r.URL.Query()
	if v := qs.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxPage {
			writeError(w, http.StatusBadRequest, "limit entre 1 e "+strconv.Itoa(maxPage))
			return
		}
		p.PageSize = int32(n)
	}
	if v := qs.Get("source"); v != "" {
		p.Source = &v
	}
	if v := qs.Get("cursor"); v != "" {
		count, id, err := parseCursor(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "cursor inválido")
			return
		}
		p.AfterCount, p.AfterID = &count, &id
	}

	resp := UnmappedList{Items: []UnmappedItem{}}
	// Leitura repetível: as_of e a lista enxergam o mesmo instante.
	tx, err := s.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	q := s.q.WithTx(tx)
	if resp.AsOf, err = q.LatestChangeID(r.Context(), actor.TenantID); err != nil {
		s.internalError(w, r, err)
		return
	}
	rows, err := q.ListUnmappedForTenant(r.Context(), p)
	if err != nil {
		s.internalError(w, r, err)
		return
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
	writeJSON(w, http.StatusOK, resp)
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

// mapRaw: PUT /v1/software/raw/{id} {"vendor": "...", "product": "..."}
// Mapeamento manual: vale para todos os ativos que têm o nome, as regras não
// o mudam depois, e as instalações são recalculadas em seguida.
// rawInTenant confere que o nome bruto aparece nos ativos do tenant: um nome
// que o tenant não tem é tratado como inexistente.
func rawInTenant(r *http.Request, q *store.Queries, rawID, tenant uuid.UUID) error {
	if _, err := q.GetSoftwareRawForUpdate(r.Context(), rawID); errors.Is(err, pgx.ErrNoRows) {
		return errorf(http.StatusNotFound, "nome bruto não encontrado")
	} else if err != nil {
		return err
	}
	tenants, err := q.RawTenants(r.Context(), rawID)
	if err != nil {
		return err
	}
	if !slices.Contains(tenants, tenant) {
		return errorf(http.StatusNotFound, "nome bruto não encontrado")
	}
	return nil
}

// mapRaw é o mapeamento manual. Vale só para o tenant de quem mapeia e vence
// a regra. Um produto que já está no catálogo comum é reaproveitado; um que
// não está é criado só para o tenant.
func (s *Server) mapRaw(w http.ResponseWriter, r *http.Request) {
	actor := actorFrom(r.Context())
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
	if strings.TrimSpace(req.Product) == "" {
		writeError(w, http.StatusBadRequest, "product é obrigatório")
		return
	}

	resp := MapResponse{SoftwareRawID: rawID, ProductScope: "catalog"}
	err = s.inTx(r.Context(), func(q *store.Queries) error {
		if err := rawInTenant(r, q, rawID, actor.TenantID); err != nil {
			return err
		}
		swID, private, err := ingest.EnsureProduct(r.Context(), q, actor.TenantID, req.Vendor, req.Product)
		if err != nil {
			return err
		}
		if private {
			resp.ProductScope = "tenant"
		}
		if err := q.SetTenantMapping(r.Context(), store.SetTenantMappingParams{
			TenantID: actor.TenantID, SoftwareRawID: rawID, SoftwareID: swID, MappedBy: &actor.ID,
		}); err != nil {
			return err
		}
		if resp.AssetsToApply, err = q.MarkTenantAssetsForRemap(r.Context(), store.MarkTenantAssetsForRemapParams{
			SoftwareRawID: rawID, TenantID: actor.TenantID,
		}); err != nil {
			return err
		}
		resp.SoftwareID = swID
		data, _ := json.Marshal(map[string]any{
			"software_raw_id": rawID, "software_id": swID, "vendor": req.Vendor, "product": req.Product,
			"manual": true, "product_scope": resp.ProductScope,
		})
		resp.AsOf, err = q.AppendChange(r.Context(), store.AppendChangeParams{
			TenantID: actor.TenantID, Kind: "software_mapped", ActorID: &actor.ID, Data: data,
		})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if s.Wake != nil {
		s.Wake()
	}
	s.log.InfoContext(r.Context(), "software mapeado à mão", "actor", actor.Name, "software_raw_id", rawID, "software_id", resp.SoftwareID)
	writeJSON(w, http.StatusOK, resp)
}

// unmapRaw desfaz o mapeamento manual do tenant: volta a valer a regra, se
// houver, ou o nome volta para a lista de não identificados.
func (s *Server) unmapRaw(w http.ResponseWriter, r *http.Request) {
	actor := actorFrom(r.Context())
	rawID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "nome bruto não encontrado")
		return
	}
	resp := UnmapResponse{SoftwareRawID: rawID}
	err = s.inTx(r.Context(), func(q *store.Queries) error {
		if err := rawInTenant(r, q, rawID, actor.TenantID); err != nil {
			return err
		}
		prev, err := q.DeleteTenantMapping(r.Context(), store.DeleteTenantMappingParams{TenantID: actor.TenantID, SoftwareRawID: rawID})
		if errors.Is(err, pgx.ErrNoRows) {
			return errorf(http.StatusNotFound, "o tenant não mapeou este nome à mão")
		}
		if err != nil {
			return err
		}
		if resp.AssetsToApply, err = q.MarkTenantAssetsForRemap(r.Context(), store.MarkTenantAssetsForRemapParams{
			SoftwareRawID: rawID, TenantID: actor.TenantID,
		}); err != nil {
			return err
		}
		data, _ := json.Marshal(map[string]any{"software_raw_id": rawID, "previous_software_id": prev, "manual": true})
		resp.AsOf, err = q.AppendChange(r.Context(), store.AppendChangeParams{
			TenantID: actor.TenantID, Kind: "software_unmapped", ActorID: &actor.ID, Data: data,
		})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if s.Wake != nil {
		s.Wake()
	}
	writeJSON(w, http.StatusOK, resp)
}
