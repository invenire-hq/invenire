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
	AssetsToApply int64     `json:"assets_to_apply"` // ativos cujas instalações serão recalculadas
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

	resp := MapResponse{SoftwareRawID: rawID}
	err = s.inTx(r.Context(), func(q *store.Queries) error {
		if _, err := q.GetSoftwareRawForUpdate(r.Context(), rawID); errors.Is(err, pgx.ErrNoRows) {
			return errorf(http.StatusNotFound, "nome bruto não encontrado")
		} else if err != nil {
			return err
		}
		// O mapeamento é global, mas só quem vê o nome nos próprios ativos
		// pode mudá-lo.
		tenants, err := q.RawTenants(r.Context(), rawID)
		if err != nil {
			return err
		}
		if !slices.Contains(tenants, actor.TenantID) {
			return errorf(http.StatusNotFound, "nome bruto não encontrado")
		}
		swID, err := ingest.EnsureProduct(r.Context(), q, req.Vendor, req.Product)
		if err != nil {
			return err
		}
		conf := float32(1)
		if err := q.MapSoftwareRaw(r.Context(), store.MapSoftwareRawParams{
			ID: rawID, SoftwareID: &swID, Confidence: &conf, MappedBy: &actor.ID,
		}); err != nil {
			return err
		}
		if resp.AssetsToApply, err = q.MarkAssetsForRemap(r.Context(), rawID); err != nil {
			return err
		}
		resp.SoftwareID = swID
		data, _ := json.Marshal(map[string]any{
			"software_raw_id": rawID, "software_id": swID, "vendor": req.Vendor, "product": req.Product, "manual": true,
		})
		for _, t := range tenants {
			var by *uuid.UUID
			if t == actor.TenantID {
				by = &actor.ID
			}
			id, err := q.AppendChange(r.Context(), store.AppendChangeParams{TenantID: t, Kind: "software_mapped", ActorID: by, Data: data})
			if err != nil {
				return err
			}
			if t == actor.TenantID {
				resp.AsOf = id
			}
		}
		return nil
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
