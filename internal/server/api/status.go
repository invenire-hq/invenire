// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/invenire-hq/invenire/internal/server/store"
)

// Situação de um ativo no status do parque, da pior para a melhor.
const (
	StateNoAgent = "sem-agente" // nenhum agente ativo (revogado ou nunca cadastrado)
	StateFailed  = "erro"       // algum snapshot não pôde ser ingerido
	StateStale   = "atrasado"   // o agente não aparece há mais que o limite
	StateNoData  = "sem-coleta" // cadastrado, mas nenhum snapshot chegou
	StatePending = "na-fila"    // snapshots esperando a ingestão
	StateOK      = "em-dia"
)

// AssetStatus é uma linha do status do parque.
type AssetStatus struct {
	ID            uuid.UUID
	Hostname      string
	OSFamily      string
	Unit          string
	State         string
	AgentVersion  string
	AgentLastSeen *time.Time
	LastCollected *time.Time
	Pending       int32
	Failed        int32
	ChainBreaks   int32
	Installed     int32
}

// ParkStatus é a situação do parque de um tenant.
type ParkStatus struct {
	Assets      []AssetStatus
	ByState     map[string]int
	ByOS        map[string]int
	ByVersion   map[string]int
	Unmapped    int32
	ValidTokens int
	Errors      []store.RecentIngestErrorsRow
}

// Problems é quantos ativos precisam de atenção (sem agente, com erro,
// atrasados ou sem coleta).
func (p ParkStatus) Problems() int {
	return p.ByState[StateNoAgent] + p.ByState[StateFailed] + p.ByState[StateStale] + p.ByState[StateNoData]
}

// TenantParkStatus lê a situação do parque. stale é quanto tempo sem notícia
// do agente o torna atrasado (com coleta a cada 4h, 12h são três coletas
// perdidas). now vem de quem chama, para os testes.
func TenantParkStatus(ctx context.Context, q *store.Queries, tenant uuid.UUID, stale time.Duration, now time.Time) (ParkStatus, error) {
	p := ParkStatus{ByState: map[string]int{}, ByOS: map[string]int{}, ByVersion: map[string]int{}}
	rows, err := q.TenantStatus(ctx, tenant)
	if err != nil {
		return p, err
	}
	units, err := q.ListUnits(ctx, tenant)
	if err != nil {
		return p, err
	}
	path := map[uuid.UUID]string{}
	for _, u := range units {
		path[u.ID] = u.Path
	}
	for _, r := range rows {
		a := AssetStatus{
			ID: r.ID, Hostname: deref(r.Hostname), OSFamily: deref(r.OsFamily), AgentVersion: deref(r.AgentVersion),
			AgentLastSeen: r.AgentLastSeen, Pending: r.Pending, Failed: r.Failed, ChainBreaks: r.ChainBreaks, Installed: r.Installed,
		}
		if t, ok := r.LastCollected.(time.Time); ok {
			a.LastCollected = &t
		}
		if r.UnitID != nil {
			a.Unit = path[*r.UnitID]
		}
		switch {
		case r.AgentID == nil:
			a.State = StateNoAgent
		case a.Failed > 0:
			a.State = StateFailed
		case a.AgentLastSeen == nil && a.LastCollected == nil:
			a.State = StateNoData
		case a.AgentLastSeen == nil || now.Sub(*a.AgentLastSeen) > stale:
			a.State = StateStale
		case a.LastCollected == nil:
			a.State = StateNoData
		case a.Pending > 0:
			a.State = StatePending
		default:
			a.State = StateOK
		}
		p.ByState[a.State]++
		p.ByOS[orDash(a.OSFamily)]++
		if r.AgentID != nil {
			p.ByVersion[orDash(a.AgentVersion)]++
		}
		p.Assets = append(p.Assets, a)
	}
	// Os que precisam de atenção primeiro, e por hostname dentro de cada grupo.
	rank := map[string]int{StateNoAgent: 0, StateFailed: 1, StateStale: 2, StateNoData: 3, StatePending: 4, StateOK: 5}
	sort.SliceStable(p.Assets, func(i, j int) bool { return rank[p.Assets[i].State] < rank[p.Assets[j].State] })

	if p.Unmapped, err = q.CountUnmappedForTenant(ctx, tenant); err != nil {
		return p, err
	}
	tokens, err := q.ListEnrollmentTokens(ctx, store.ListEnrollmentTokensParams{TenantID: tenant})
	if err != nil {
		return p, err
	}
	for _, t := range tokens {
		p.ValidTokens += int(t.MaxUses - t.Uses)
	}
	p.Errors, err = q.RecentIngestErrors(ctx, tenant)
	return p, err
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
