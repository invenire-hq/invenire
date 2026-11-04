// Package ingest aplica os snapshots recebidos ao estado do inventário.
//
// O snapshot é imutável e a ingestão só lê dele. Para cada snapshot pendente,
// numa transação:
//
//  1. cada software bruto entra em software_raw (sem produto, se ainda não
//     identificado) e passa pelas regras de normalização, se houver;
//  2. os atributos do ativo (hostname, SO, fabricante) são atualizados;
//  3. as instalações abertas do ativo são comparadas com o software
//     identificado do snapshot: as novas são abertas, as presentes recebem
//     last_snapshot_id e as ausentes recebem removed_at;
//  4. cada mudança vai para o change_log, atribuída ao ator de sistema
//     "ingest" (ou "normalize", para mapeamentos feitos por regra).
//
// Software sem produto não vira installation: ele fica na lista de não
// identificados e aparece nas instalações quando for mapeado e o próximo
// snapshot do ativo chegar.
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

	"github.com/athosbes/invenire/internal/server/store"
	"github.com/athosbes/invenire/pkg/snapshot"
)

// Match é o produto que uma regra de normalização reconheceu num nome bruto.
type Match struct {
	Vendor     string // vazio quando o produto não tem fabricante conhecido
	Product    string
	Confidence float32
	Rule       string // nome da regra, gravado em software_raw.mapped_rule
}

// Normalizer reconhece produtos em nomes brutos. As regras ficam em
// internal/server/normalize; a ingestão só as aplica.
type Normalizer interface {
	Normalize(source, rawName, rawVendor string) (Match, bool)
}

// Result resume o que a ingestão de um snapshot fez.
type Result struct {
	SnapshotID uuid.UUID
	AssetID    uuid.UUID
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

// Worker processa os snapshots pendentes.
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

// Wake avisa que chegou snapshot novo. Não bloqueia.
func (w *Worker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run processa snapshots até o contexto acabar.
func (w *Worker) Run(ctx context.Context) error {
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

// Drain processa os snapshots pendentes até não haver mais nenhum.
func (w *Worker) Drain(ctx context.Context) (int, error) {
	n := 0
	for {
		res, ok, err := w.ProcessNext(ctx)
		if err != nil || !ok {
			return n, err
		}
		n++
		w.log.InfoContext(ctx, "snapshot ingerido",
			"snapshot_id", res.SnapshotID, "asset_id", res.AssetID,
			"software", res.Software, "unmapped", res.Unmapped, "mapped", res.Mapped,
			"installed", res.Installed, "kept", res.Kept, "removed", res.Removed,
			"partial", res.Partial, "out_of_order", res.OutOfOrder, "error", res.Error)
	}
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

type rawKey struct{ source, name, vendor string }

type entry struct {
	raw     rawKey
	version string
	path    string
}

type instKey struct {
	versionID uuid.UUID
	path      string
}

func (w *Worker) apply(ctx context.Context, q *store.Queries, row store.ClaimNextSnapshotRow, asset store.Asset) (Result, error) {
	res := Result{SnapshotID: row.ID, AssetID: asset.ID}
	ingestActor, err := q.EnsureSystemActor(ctx, store.EnsureSystemActorParams{TenantID: asset.TenantID, Name: "ingest"})
	if err != nil {
		return res, err
	}
	change := func(kind string, data any) error {
		return appendChange(ctx, q, asset.TenantID, kind, &asset.ID, &ingestActor.ID, data)
	}

	var snap snapshot.Snapshot
	if err := json.Unmarshal(row.Payload, &snap); err != nil {
		res.Error = "payload ilegível: " + err.Error()
		if err := change("snapshot_ingest_failed", map[string]any{"snapshot_id": row.ID, "error": res.Error}); err != nil {
			return res, err
		}
		return res, q.FinishSnapshot(ctx, store.FinishSnapshotParams{ID: row.ID, IngestError: &res.Error})
	}

	// 1. Nomes brutos, sempre: mesmo um snapshot fora de ordem alimenta a
	// lista de software não identificado.
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

	// 2. Atributos do ativo.
	if err := updateAsset(ctx, q, asset, &snap, row.CollectedAt, change); err != nil {
		return res, err
	}

	// 3. Diff de instalações.
	current, unmapped, err := currentInstallations(ctx, q, entries, raws)
	if err != nil {
		return res, err
	}
	res.Unmapped = unmapped
	res.Partial = softwareFailed(&snap)

	open, err := q.ListOpenInstallations(ctx, asset.ID)
	if err != nil {
		return res, err
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
			AssetID: asset.ID, SoftwareVersionID: k.versionID, InstallPath: k.path, FirstSnapshotID: row.ID,
		})
		if err != nil {
			return res, err
		}
		res.Installed++
		if err := change("software_installed", map[string]any{
			"installation_id": id, "software_version_id": k.versionID, "install_path": k.path,
			"snapshot_id": row.ID, "source": e.raw.source, "raw_name": e.raw.name, "version": e.version,
		}); err != nil {
			return res, err
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
				"installation_id": id, "software_version_id": k.versionID, "install_path": k.path, "snapshot_id": row.ID,
			}); err != nil {
				return res, err
			}
		}
	}
	res.Kept, res.Removed = len(keep), len(gone)
	if len(keep) > 0 {
		if err := q.TouchInstallations(ctx, store.TouchInstallationsParams{LastSnapshotID: row.ID, Ids: keep}); err != nil {
			return res, err
		}
	}
	if len(gone) > 0 {
		if err := q.CloseInstallations(ctx, store.CloseInstallationsParams{RemovedSnapshotID: &row.ID, Ids: gone}); err != nil {
			return res, err
		}
	}
	return res, q.FinishSnapshot(ctx, store.FinishSnapshotParams{ID: row.ID})
}
