// SPDX-License-Identifier: AGPL-3.0-only

package ingest

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/invenire-hq/invenire/internal/server/normalize"
	"github.com/invenire-hq/invenire/internal/server/store"
)

const remapPage = 500

// RemapAll passa as regras atuais sobre todos os nomes brutos. O mapeamento
// manual de cada tenant fica em tenant_software_map e não é tocado. Cada nome que muda marca seus ativos para reaplicação, que o
// worker faz em seguida a partir do último snapshot de cada um.
//
// É o que torna uma regra nova retroativa: os snapshots são imutáveis, então
// o estado atual pode ser recalculado com elas a qualquer momento.
func (w *Worker) RemapAll(ctx context.Context) (int, error) {
	if w.normalizer == nil {
		return 0, nil
	}
	changed := 0
	after := uuid.Nil
	for {
		rows, err := store.New(w.pool).ListRuleCandidates(ctx, store.ListRuleCandidatesParams{ID: after, Limit: remapPage})
		if err != nil {
			return changed, err
		}
		for _, r := range rows {
			after = r.ID
			ok, err := w.remapRaw(ctx, r)
			if err != nil {
				return changed, err
			}
			if ok {
				changed++
			}
		}
		if len(rows) < remapPage {
			if changed > 0 {
				w.Wake()
			}
			return changed, nil
		}
	}
}

// remapRaw aplica as regras a um nome. Devolve true se o mapeamento mudou.
func (w *Worker) remapRaw(ctx context.Context, r store.ListRuleCandidatesRow) (bool, error) {
	m, ok := w.normalizer.Normalize(r.Source, r.RawName, r.RawVendor)
	if !ok && r.SoftwareID == nil {
		return false, nil
	}
	if ok && r.SoftwareID != nil && deref(r.MappedRule) == m.Rule {
		// Mesma regra: só muda se ela agora aponta para outro produto.
		var vp *string
		if v := clean(m.Vendor, maxVendor); v != "" {
			vp = &v
		}
		found, err := store.New(w.pool).FindSoftware(ctx, store.FindSoftwareParams{Product: clean(m.Product, maxName), Vendor: vp})
		if err == nil && found.ID == *r.SoftwareID {
			return false, nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
	}

	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	q := store.New(tx)
	// Trava a linha: dois servidores reaplicando ao mesmo tempo não se cruzam.
	if _, err := q.GetSoftwareRawForUpdate(ctx, r.ID); err != nil {
		return false, err
	}

	kind, data := "software_unmapped", map[string]any{"software_raw_id": r.ID, "previous_rule": deref(r.MappedRule)}
	if ok {
		swID, err := mapRaw(ctx, q, r.ID, m, nil)
		if err != nil {
			return false, err
		}
		kind, data = "software_mapped", mappingData(r.ID, swID, m)
	} else {
		if err := q.UnmapSoftwareRaw(ctx, r.ID); err != nil {
			return false, err
		}
		if _, err := q.MarkAssetsForRemap(ctx, r.ID); err != nil {
			return false, err
		}
	}
	if err := logToTenants(ctx, q, r.ID, kind, data); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// logToTenants registra uma mudança de regra, que é global, no change_log
// de cada tenant em que ela vale (tem o nome e não o mapeou à mão), pelo
// ator "normalize".
func logToTenants(ctx context.Context, q *store.Queries, rawID uuid.UUID, kind string, data any) error {
	tenants, err := q.RuleTenants(ctx, rawID)
	if err != nil {
		return err
	}
	for _, t := range tenants {
		a, err := q.EnsureSystemActor(ctx, store.EnsureSystemActorParams{TenantID: t, Name: "normalize"})
		if err != nil {
			return err
		}
		if err := appendChange(ctx, q, t, kind, nil, &a.ID, data); err != nil {
			return err
		}
	}
	return nil
}

var _ Normalizer = (*normalize.Rules)(nil)
