// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/invenire-hq/invenire/internal/server/pki"
	"github.com/invenire-hq/invenire/internal/server/store"
)

// signingCA devolve a CA que assina os crachás do tenant, criando-a no
// primeiro cadastro. A chave é aberta a cada uso; nada fica em memória.
func (s *Server) signingCA(ctx context.Context, q *store.Queries, tenantID uuid.UUID) (*pki.CA, uuid.UUID, error) {
	row, err := q.GetSigningCA(ctx, tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		row, err = s.createTenantCA(ctx, q, tenantID)
	}
	if err != nil {
		return nil, uuid.Nil, err
	}
	keyDER, err := s.master.Open(row.KeySealed, tenantID[:])
	if err != nil {
		return nil, uuid.Nil, err
	}
	ca, err := pki.ParseCA(row.Cert, keyDER)
	return ca, row.ID, err
}

func (s *Server) createTenantCA(ctx context.Context, q *store.Queries, tenantID uuid.UUID) (store.TenantCa, error) {
	t, err := q.GetTenant(ctx, tenantID)
	if err != nil {
		return store.TenantCa{}, err
	}
	certDER, keyDER, err := pki.NewTenantCA(t.Name)
	if err != nil {
		return store.TenantCa{}, err
	}
	row, err := q.InsertSigningCA(ctx, store.InsertSigningCAParams{
		TenantID:    tenantID,
		Cert:        certDER,
		Fingerprint: pki.Fingerprint(certDER),
		KeySealed:   s.master.Seal(keyDER, tenantID[:]),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Outro cadastro criou ao mesmo tempo; vale a dele.
		return q.GetSigningCA(ctx, tenantID)
	}
	if err != nil {
		return store.TenantCa{}, err
	}
	actor, err := q.EnsureSystemActor(ctx, store.EnsureSystemActorParams{TenantID: tenantID, Name: "pki"})
	if err != nil {
		return store.TenantCa{}, err
	}
	return row, appendChange(ctx, q, tenantID, "tenant_ca_created", nil, &actor.ID,
		map[string]any{"tenant_ca_id": row.ID, "fingerprint": row.Fingerprint})
}

// ImportLegacyCA liga à CA antiga da instalação os agentes cadastrados antes
// das CAs por tenant. Ela entra em cada tenant que tem esses agentes, só para
// conferir: os crachás já emitidos continuam valendo e os novos saem da CA
// do tenant. Sem agentes antigos, não faz nada.
func ImportLegacyCA(ctx context.Context, pool *pgxpool.Pool, legacy *pki.CA) (agents int64, err error) {
	q := store.New(pool)
	tenants, err := q.TenantsWithLegacyAgents(ctx)
	if err != nil || len(tenants) == 0 {
		return 0, err
	}
	if legacy == nil {
		return 0, fmt.Errorf("%d tenant(s) têm agentes da CA antiga da instalação, mas data/ca não foi encontrado", len(tenants))
	}
	for _, t := range tenants {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return agents, err
		}
		qt := q.WithTx(tx)
		row, err := qt.ImportVerifyOnlyCA(ctx, store.ImportVerifyOnlyCAParams{
			TenantID:    t,
			Cert:        legacy.Cert.Raw,
			Fingerprint: pki.Fingerprint(legacy.Cert.Raw),
		})
		if err == nil {
			var n int64
			n, err = qt.AssignLegacyAgents(ctx, store.AssignLegacyAgentsParams{TenantID: t, TenantCaID: &row.ID})
			agents += n
		}
		if err == nil {
			err = tx.Commit(ctx)
		}
		tx.Rollback(ctx)
		if err != nil {
			return agents, err
		}
	}
	return agents, nil
}
