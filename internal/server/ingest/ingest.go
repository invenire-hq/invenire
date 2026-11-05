// Package ingest aplica os snapshots recebidos ao estado do inventário.
//
// O snapshot é imutável e a ingestão só lê dele. Para cada snapshot pendente,
// numa transação:
//
//  1. cada software bruto entra em software_raw (sem produto, se ainda não
//     identificado) e passa pelas regras de normalização;
//  2. os atributos do ativo (hostname, SO, fabricante) são atualizados;
//  3. asset_software_raw passa a ter os nomes brutos deste snapshot;
//  4. as instalações abertas do ativo são comparadas com o software
//     identificado do snapshot: as novas são abertas, as presentes recebem
//     last_snapshot_id e as ausentes recebem removed_at;
//  5. cada mudança vai para o change_log, atribuída ao ator de sistema
//     "ingest" (ou "normalize", para mapeamentos feitos por regra).
//
// Software sem produto não vira installation. Quando um nome bruto é mapeado
// (por regra nova ou à mão), os ativos que o têm ficam com remap_pending e o
// worker refaz os passos 3 e 4 sobre o último snapshot aplicado de cada um.
package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/athosbes/invenire/internal/server/normalize"
	"github.com/athosbes/invenire/internal/server/store"
	"github.com/athosbes/invenire/pkg/snapshot"
)

// Normalizer reconhece produtos em nomes brutos. Só deve devolver ok para
// resultados confiáveis o bastante para mapear sem revisão humana.
type Normalizer interface {
	Normalize(source, rawName, rawVendor string) (normalize.Match, bool)
}

// Result resume o que a ingestão de um snapshot (ou a reaplicação de
// mapeamentos sobre ele) fez.
type Result struct {
	SnapshotID uuid.UUID
	AssetID    uuid.UUID
	Remap      bool // reaplicação sobre o último snapshot, não snapshot novo
	Software   int  // entradas de software no snapshot
	Unmapped   int  // dessas, quantas ainda sem produto
	Mapped     int  // nomes mapeados agora por regra
	Installed  int  // instalações abertas
	Kept       int  // instalações que continuam
	Removed    int  // instalações fechadas
	Partial    bool // coletor de software falhou: nada foi fechado
	OutOfOrder bool // coleta anterior à última aplicada: estado não mudou
	Error      string
}

// Worker processa os snapshots pendentes e as reaplicações.
type Worker struct {
	pool       *pgxpool.Pool
	log        *slog.Logger
	normalizer Normalizer
	wake       chan struct{}

	// Poll é a espera entre buscas quando ninguém chama Wake (padrão 30s).
	Poll time.Duration
}

// New cria o worker. normalizer pode ser nil: nenhum nome é mapeado por
// regra e só mapeamentos manuais viram instalação.
func New(pool *pgxpool.Pool, log *slog.Logger, normalizer Normalizer) *Worker {
	return &Worker{pool: pool, log: log, normalizer: normalizer, wake: make(chan struct{}, 1), Poll: 30 * time.Second}
}

// Wake avisa que há trabalho novo (snapshot ou mapeamento). Não bloqueia.
func (w *Worker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run processa o que está pendente, reaplica as regras a todos os nomes
// brutos (elas podem ter mudado desde a última subida) e depois processa o
// que chegar, até o contexto acabar. O pendente vem antes: ele preenche
// asset_software_raw, que diz em que tenants registrar cada mapeamento.
func (w *Worker) Run(ctx context.Context) error {
	if _, err := w.Drain(ctx); err != nil && ctx.Err() == nil {
		w.log.ErrorContext(ctx, "ingestão", "err", err)
	}
	if w.normalizer != nil {
		n, err := w.RemapAll(ctx)
		if err != nil && ctx.Err() == nil {
			w.log.ErrorContext(ctx, "reaplicar regras", "err", err)
		}
		w.log.InfoContext(ctx, "regras reaplicadas", "changed", n)
	}
	for {
		if _, err := w.Drain(ctx); err != nil && ctx.Err() == nil {
			w.log.ErrorContext(ctx, "ingestão", "err", err)
		}
		t := time.NewTimer(w.Poll)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-w.wake:
			t.Stop()
		case <-t.C:
		}
	}
}

// Drain processa os snapshots pendentes e depois as reaplicações pendentes.
// Snapshots primeiro: um snapshot novo já aplica os mapeamentos atuais e
// dispensa a reaplicação do ativo.
func (w *Worker) Drain(ctx context.Context) (int, error) {
	n := 0
	for _, next := range []func(context.Context) (Result, bool, error){w.ProcessNext, w.ProcessRemap} {
		for {
			res, ok, err := next(ctx)
			if err != nil || !ok {
				if err != nil {
					return n, err
				}
				break
			}
			n++
			w.log.InfoContext(ctx, "snapshot aplicado",
				"snapshot_id", res.SnapshotID, "asset_id", res.AssetID, "remap", res.Remap,
				"software", res.Software, "unmapped", res.Unmapped, "mapped", res.Mapped,
				"installed", res.Installed, "kept", res.Kept, "removed", res.Removed,
				"partial", res.Partial, "out_of_order", res.OutOfOrder, "error", res.Error)
		}
	}
	return n, nil
}

// ProcessNext ingere o próximo snapshot pendente, se houver. Erros de banco
// desfazem tudo e o snapshot volta para a fila; um payload que não pode ser
// lido fica marcado em ingest_error e não é tentado de novo.
func (w *Worker) ProcessNext(ctx context.Context) (Result, bool, error) {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return Result{}, false, err
	}
	defer tx.Rollback(ctx)
	q := store.New(tx)

	row, err := q.ClaimNextSnapshot(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, err
	}
	asset, err := q.LockAsset(ctx, row.AssetID)
	if err != nil {
		return Result{}, false, err
	}
	res, err := w.apply(ctx, q, row, asset)
	if err != nil {
		return res, false, fmt.Errorf("snapshot %s: %w", row.ID, err)
	}
	return res, true, tx.Commit(ctx)
}

// ProcessRemap recalcula as instalações de um ativo com remap_pending a
// partir do último snapshot aplicado, com os mapeamentos de agora.
func (w *Worker) ProcessRemap(ctx context.Context) (Result, bool, error) {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return Result{}, false, err
	}
	defer tx.Rollback(ctx)
	q := store.New(tx)

	asset, err := q.ClaimRemapAsset(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, err
	}
	res := Result{AssetID: asset.ID, Remap: true}
	latest, err := q.LatestAppliedSnapshot(ctx, asset.ID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Nada aplicado ainda: o primeiro snapshot já usará os mapeamentos.
		return res, true, w.commitClear(ctx, tx, q, asset.ID)
	case err != nil:
		return res, false, err
	}
	res.SnapshotID = latest.ID

	var snap snapshot.Snapshot
	if err := json.Unmarshal(latest.Payload, &snap); err != nil {
		res.Error = "payload ilegível: " + err.Error()
		return res, true, w.commitClear(ctx, tx, q, asset.ID)
	}
	change, err := changeFunc(ctx, q, asset)
	if err != nil {
		return res, false, err
	}
	entries := entriesOf(snap.Software)
	res.Software = len(entries)
	raws, mapped, err := w.ensureRaws(ctx, q, asset.TenantID, entries)
	if err != nil {
		return res, false, err
	}
	res.Mapped = mapped
	if err := applyState(ctx, q, asset, latest.ID, &snap, entries, raws, &res, change); err != nil {
		return res, false, fmt.Errorf("reaplicar no ativo %s: %w", asset.ID, err)
	}
	return res, true, tx.Commit(ctx)
}

func (w *Worker) commitClear(ctx context.Context, tx pgx.Tx, q *store.Queries, assetID uuid.UUID) error {
	if err := q.ClearRemap(ctx, assetID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type rawKey struct{ source, name, vendor string }

type rawInfo struct {
	id       uuid.UUID
	software *uuid.UUID // nil: ainda sem produto
}

type entry struct {
	raw     rawKey
	version string
	path    string
}

type instKey struct {
	versionID uuid.UUID
	path      string
}

type changeLogger func(kind string, data any) error

// changeFunc registra mudanças do ativo em nome do ator "ingest" do tenant.
func changeFunc(ctx context.Context, q *store.Queries, asset store.Asset) (changeLogger, error) {
	actor, err := q.EnsureSystemActor(ctx, store.EnsureSystemActorParams{TenantID: asset.TenantID, Name: "ingest"})
	if err != nil {
		return nil, err
	}
	return func(kind string, data any) error {
		return appendChange(ctx, q, asset.TenantID, kind, &asset.ID, &actor.ID, data)
	}, nil
}

func (w *Worker) apply(ctx context.Context, q *store.Queries, row store.ClaimNextSnapshotRow, asset store.Asset) (Result, error) {
	res := Result{SnapshotID: row.ID, AssetID: asset.ID}
	change, err := changeFunc(ctx, q, asset)
	if err != nil {
		return res, err
	}

	var snap snapshot.Snapshot
	if err := json.Unmarshal(row.Payload, &snap); err != nil {
		res.Error = "payload ilegível: " + err.Error()
		if err := change("snapshot_ingest_failed", map[string]any{"snapshot_id": row.ID, "error": res.Error}); err != nil {
			return res, err
		}
		return res, q.FinishSnapshot(ctx, store.FinishSnapshotParams{ID: row.ID, IngestError: &res.Error})
	}

	// Nomes brutos, sempre: mesmo um snapshot fora de ordem alimenta a lista
	// de software não identificado.
	entries := entriesOf(snap.Software)
	res.Software = len(entries)
	raws, mapped, err := w.ensureRaws(ctx, q, asset.TenantID, entries)
	if err != nil {
		return res, err
	}
	res.Mapped = mapped

	last, err := q.LastIngestedCollectedAt(ctx, asset.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return res, err
	}
	if err == nil && row.CollectedAt.Before(last) {
		res.OutOfOrder = true
		if err := change("snapshot_out_of_order", map[string]any{
			"snapshot_id": row.ID, "collected_at": row.CollectedAt, "last_applied": last,
		}); err != nil {
			return res, err
		}
		return res, q.FinishSnapshot(ctx, store.FinishSnapshotParams{ID: row.ID})
	}

	if err := updateAsset(ctx, q, asset, &snap, row.CollectedAt, change); err != nil {
		return res, err
	}
	if err := applyState(ctx, q, asset, row.ID, &snap, entries, raws, &res, change); err != nil {
		return res, err
	}
	return res, q.FinishSnapshot(ctx, store.FinishSnapshotParams{ID: row.ID})
}

// applyState faz o estado atual do ativo (nomes brutos e instalações abertas)
// refletir o snapshot, com os mapeamentos de agora, e limpa remap_pending.
func applyState(ctx context.Context, q *store.Queries, asset store.Asset, snapshotID uuid.UUID, snap *snapshot.Snapshot,
	entries []entry, raws map[rawKey]rawInfo, res *Result, change changeLogger) error {
	res.Partial = softwareFailed(snap)
	if err := syncAssetRaws(ctx, q, asset.ID, raws, res.Partial); err != nil {
		return err
	}
	current, unmapped, err := currentInstallations(ctx, q, entries, raws, snap.OS.ID)
	if err != nil {
		return err
	}
	res.Unmapped = unmapped

	open, err := q.ListOpenInstallations(ctx, asset.ID)
	if err != nil {
		return err
	}
	openByKey := make(map[instKey]uuid.UUID, len(open))
	for _, o := range open {
		openByKey[instKey{o.SoftwareVersionID, o.InstallPath}] = o.ID
	}

	var keep, gone []uuid.UUID
	for k, e := range current {
		if id, ok := openByKey[k]; ok {
			keep = append(keep, id)
			continue
		}
		id, err := q.OpenInstallation(ctx, store.OpenInstallationParams{
			AssetID: asset.ID, SoftwareVersionID: k.versionID, InstallPath: k.path, FirstSnapshotID: snapshotID,
		})
		if err != nil {
			return err
		}
		res.Installed++
		if err := change("software_installed", map[string]any{
			"installation_id": id, "software_version_id": k.versionID, "install_path": k.path,
			"snapshot_id": snapshotID, "source": e.raw.source, "raw_name": e.raw.name, "version": e.version,
			"remap": res.Remap,
		}); err != nil {
			return err
		}
	}
	// Com o coletor de software em falha, a lista pode estar incompleta:
	// ausência não prova remoção, e a instalação fica aberta sem ser tocada.
	if !res.Partial {
		for k, id := range openByKey {
			if _, ok := current[k]; ok {
				continue
			}
			gone = append(gone, id)
			if err := change("software_removed", map[string]any{
				"installation_id": id, "software_version_id": k.versionID, "install_path": k.path,
				"snapshot_id": snapshotID, "remap": res.Remap,
			}); err != nil {
				return err
			}
		}
	}
	res.Kept, res.Removed = len(keep), len(gone)
	if len(keep) > 0 {
		if err := q.TouchInstallations(ctx, store.TouchInstallationsParams{LastSnapshotID: snapshotID, Ids: keep}); err != nil {
			return err
		}
	}
	if len(gone) > 0 {
		if err := q.CloseInstallations(ctx, store.CloseInstallationsParams{RemovedSnapshotID: &snapshotID, Ids: gone}); err != nil {
			return err
		}
	}
	return q.ClearRemap(ctx, asset.ID)
}

// syncAssetRaws deixa em asset_software_raw os nomes do snapshot. Com a lista
// parcial, só acrescenta.
func syncAssetRaws(ctx context.Context, q *store.Queries, assetID uuid.UUID, raws map[rawKey]rawInfo, partial bool) error {
	ids := make([]uuid.UUID, 0, len(raws))
	for _, r := range raws {
		ids = append(ids, r.id)
	}
	if !partial {
		if err := q.PruneAssetRaws(ctx, store.PruneAssetRawsParams{AssetID: assetID, RawIds: ids}); err != nil {
			return err
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return q.AddAssetRaws(ctx, store.AddAssetRawsParams{AssetID: assetID, RawIds: ids})
}
