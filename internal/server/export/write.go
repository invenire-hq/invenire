// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/invenire-hq/invenire/internal/server/pki"
	"github.com/invenire-hq/invenire/internal/server/store"
)

// Options controla a exportação.
type Options struct {
	ServerVersion string
	// WithCAKey inclui as chaves das CAs do tenant, cifradas com uma chave de
	// exportação nova (Result.ExportKey). Exige Master, que abre as chaves
	// guardadas no banco. Sem as chaves, quem importa confere os crachás
	// atuais mas precisa recadastrar os agentes quando eles vencerem.
	WithCAKey bool
	// ExportKey é a chave de exportação, se quem chama já a tem (a API a
	// manda no cabeçalho antes do corpo). Nula = gerar uma.
	ExportKey []byte
	Master    *pki.MasterKey
	// TempDir guarda os arquivos de registros enquanto o pacote é montado
	// (o tar precisa do tamanho antes do conteúdo). Vazio = o do sistema.
	TempDir string
}

// Result é o que a exportação devolve além do pacote.
type Result struct {
	Manifest Manifest
	// ExportKey é a chave que abre as chaves das CAs, só com WithCAKey. Não
	// vai no pacote: é mostrada uma vez a quem exportou.
	ExportKey []byte
	// CAKeys é quantas chaves de CA foram incluídas (só com WithCAKey). CAs
	// só de conferência, como a antiga da instalação, não têm chave.
	CAKeys int
}

const changePage = 1000

// Write escreve o pacote do tenant em w. Tudo é lido numa só transação de
// leitura repetível: o pacote é um retrato de um instante (Manifest.AsOf).
func Write(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, w io.Writer, opts Options) (Result, error) {
	var res Result
	if opts.WithCAKey && opts.Master == nil {
		return res, errors.New("export: chave-mestra necessária para exportar a chave da CA")
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)
	q := store.New(tx)

	t, err := q.GetTenant(ctx, tenantID)
	if err != nil {
		return res, fmt.Errorf("tenant: %w", err)
	}
	m := &res.Manifest
	m.Format, m.FormatVersion = Format, Version
	m.Tenant.ID, m.Tenant.Name = t.ID, t.Name
	m.ExportedAt = time.Now().UTC()
	m.ServerVersion = opts.ServerVersion
	m.Files = map[string]FileInfo{}
	if m.AsOf, err = q.LatestChangeID(ctx, tenantID); err != nil {
		return res, err
	}

	var sealer *pki.MasterKey
	if opts.WithCAKey {
		res.ExportKey = opts.ExportKey
		if res.ExportKey == nil {
			res.ExportKey = NewExportKey()
		}
		if sealer, err = pki.NewMasterKey(res.ExportKey); err != nil {
			return res, err
		}
		m.CAKey = &CAKeyInfo{Algorithm: "AES-256-GCM", AAD: "tenant_id||tenant_ca_id"}
	}

	dir, err := os.MkdirTemp(opts.TempDir, "invenire-export-*")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(dir)
	files := map[string]*jsonl{}
	for _, name := range Files {
		if files[name], err = newJSONL(filepath.Join(dir, name)); err != nil {
			return res, err
		}
	}
	defer func() {
		for _, f := range files {
			f.f.Close()
		}
	}()

	snaps, err := collect(ctx, q, t.ID, files, opts.Master, sealer, &res.CAKeys)
	if err != nil {
		return res, err
	}
	if res.CAKeys == 0 {
		m.CAKey = nil // nenhuma CA com chave: o manifesto não anuncia o que não tem
	}

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	for _, name := range Files {
		f := files[name]
		if err := f.finish(); err != nil {
			return res, err
		}
		m.Files[name] = FileInfo{SHA256: hex.EncodeToString(f.h.Sum(nil)), Bytes: f.n, Records: f.records}
		if err := addFile(tw, name, f.f, f.n, m.ExportedAt); err != nil {
			return res, err
		}
	}
	for _, s := range snaps {
		if s.Body == "" {
			payload, err := q.GetSnapshotPayload(ctx, s.ID)
			if err != nil {
				return res, fmt.Errorf("snapshot %s: %w", s.ID, err)
			}
			if err := addBytes(tw, s.Payload, payload, s.ReceivedAt); err != nil {
				return res, err
			}
			m.SnapshotPayloads++
			continue
		}
		body, err := q.GetSnapshotBody(ctx, s.ID)
		if err != nil {
			return res, fmt.Errorf("snapshot %s: %w", s.ID, err)
		}
		if sum := sha256.Sum256(body); hex.EncodeToString(sum[:]) != s.SHA256 {
			return res, fmt.Errorf("snapshot %s: os bytes guardados não conferem com o sha256", s.ID)
		}
		if err := addBytes(tw, s.Body, body, s.ReceivedAt); err != nil {
			return res, err
		}
		m.SnapshotBodies++
	}
	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return res, err
	}
	if err := addBytes(tw, ManifestFile, append(mb, '\n'), m.ExportedAt); err != nil {
		return res, err
	}
	if err := tw.Close(); err != nil {
		return res, err
	}
	return res, gz.Close()
}

// collect grava os arquivos de registros e devolve os snapshots, cujos bytes
// entram no pacote depois.
func collect(ctx context.Context, q *store.Queries, tenant uuid.UUID, files map[string]*jsonl, master, sealer *pki.MasterKey, caKeys *int) ([]Snapshot, error) {
	cas, err := q.ExportTenantCAs(ctx, tenant)
	if err != nil {
		return nil, err
	}
	for _, c := range cas {
		r := TenantCA{
			ID: c.ID, Fingerprint: c.Fingerprint, CreatedAt: c.CreatedAt, RetiredAt: c.RetiredAt, RevokedAt: c.RevokedAt,
			CertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Cert})),
		}
		if sealer != nil && len(c.KeySealed) > 0 && c.RevokedAt == nil {
			keyDER, err := master.Open(c.KeySealed, tenant[:])
			if err != nil {
				return nil, fmt.Errorf("CA %s: %w", c.ID, err)
			}
			r.KeySealed = sealer.Seal(keyDER, CAKeyAAD(tenant, c.ID))
			*caKeys++
		}
		if err := files[FileTenantCAs].add(r); err != nil {
			return nil, err
		}
	}

	units, err := q.ExportUnits(ctx, tenant)
	if err != nil {
		return nil, err
	}
	for _, u := range units {
		if err := files[FileUnits].add(Unit{ID: u.ID, ParentID: u.ParentID, Kind: u.Kind, Name: u.Name, Source: u.Source, CreatedAt: u.CreatedAt}); err != nil {
			return nil, err
		}
	}

	assets, err := q.ExportAssets(ctx, tenant)
	if err != nil {
		return nil, err
	}
	for _, a := range assets {
		if err := files[FileAssets].add(Asset{
			ID: a.ID, Fingerprint: a.Fingerprint, Kind: a.Kind, UnitID: a.UnitID, Criticality: a.Criticality,
			FirstSeenAt: a.FirstSeenAt, LastSeenAt: a.LastSeenAt,
			Collected: AssetCollected{
				Hostname: a.Hostname, Serial: a.Serial, Manufacturer: a.Manufacturer, Model: a.Model,
				OSFamily: a.OsFamily, OSName: a.OsName, OSVersion: a.OsVersion,
			},
		}); err != nil {
			return nil, err
		}
	}

	labels, err := q.ExportLabels(ctx, tenant)
	if err != nil {
		return nil, err
	}
	for _, l := range labels {
		if err := files[FileLabels].add(Label{AssetID: l.AssetID, Key: l.Key, Value: l.Value, SetBy: l.SetBy, SetAt: l.SetAt}); err != nil {
			return nil, err
		}
	}

	agents, err := q.ExportAgents(ctx, tenant)
	if err != nil {
		return nil, err
	}
	for _, g := range agents {
		if err := files[FileAgents].add(Agent{
			ID: g.ID, AssetID: g.AssetID, CertFingerprint: g.CertFingerprint, TenantCAID: g.TenantCaID,
			PrevCertFingerprint: g.PrevCertFingerprint, PrevTenantCAID: g.PrevTenantCaID,
			AgentVersion: g.AgentVersion, EnrolledAt: g.EnrolledAt, LastSeenAt: g.LastSeenAt, RevokedAt: g.RevokedAt,
		}); err != nil {
			return nil, err
		}
	}

	actors, err := q.ExportActors(ctx, tenant)
	if err != nil {
		return nil, err
	}
	for _, a := range actors {
		if err := files[FileActors].add(Actor{ID: a.ID, Kind: a.Kind, Name: a.Name, Scopes: a.Scopes, CreatedAt: a.CreatedAt, DisabledAt: a.DisabledAt}); err != nil {
			return nil, err
		}
	}

	rows, err := q.ExportSnapshots(ctx, tenant)
	if err != nil {
		return nil, err
	}
	snaps := make([]Snapshot, 0, len(rows))
	for _, s := range rows {
		sum := hex.EncodeToString(s.Sha256)
		r := Snapshot{
			ID: s.ID, AssetID: s.AssetID, AgentID: s.AgentID, SchemaVersion: s.SchemaVersion,
			CollectedAt: s.CollectedAt, ReceivedAt: s.ReceivedAt, SHA256: sum,
			PrevSHA256: hex.EncodeToString(s.PrevSha256),
		}
		if s.HasBody {
			r.Body = SnapshotDir + sum + ".json"
		} else {
			r.Payload = SnapshotDir + sum + PayloadSuffix
		}
		if err := files[FileSnapshots].add(r); err != nil {
			return nil, err
		}
		snaps = append(snaps, r)
	}

	products, err := q.ExportPrivateSoftware(ctx, &tenant)
	if err != nil {
		return nil, err
	}
	for _, p := range products {
		r := Product{ID: p.ID, VendorID: p.VendorID, Vendor: p.Vendor, Product: p.Product, CPEVendor: p.CpeVendor, CPEProduct: p.CpeProduct}
		if p.VendorID != nil {
			r.VendorScope = scope(p.VendorTenantID)
		}
		if err := files[FileProducts].add(r); err != nil {
			return nil, err
		}
	}

	maps, err := q.ExportMappings(ctx, tenant)
	if err != nil {
		return nil, err
	}
	for _, mp := range maps {
		if err := files[FileMappings].add(Mapping{
			Source: mp.Source, RawName: mp.RawName, RawVendor: mp.RawVendor, SoftwareID: mp.SoftwareID,
			Vendor: mp.Vendor, Product: mp.Product, ProductScope: scope(mp.SoftwareTenantID),
			MappedBy: mp.MappedBy, MappedAt: mp.MappedAt,
		}); err != nil {
			return nil, err
		}
	}

	props, err := q.ExportProposals(ctx, tenant)
	if err != nil {
		return nil, err
	}
	for _, p := range props {
		if err := files[FileProposals].add(Proposal{
			ID: p.ID, ActorID: p.ActorID, Kind: p.Kind, TargetType: p.TargetType, TargetID: p.TargetID,
			Payload: p.Payload, Evidence: p.Evidence, Confidence: p.Confidence, Status: p.Status,
			ReviewedBy: p.ReviewedBy, ReviewedAt: p.ReviewedAt, ReviewNote: p.ReviewNote, CreatedAt: p.CreatedAt,
		}); err != nil {
			return nil, err
		}
	}

	for after := int64(0); ; {
		page, err := q.ListChanges(ctx, store.ListChangesParams{TenantID: tenant, AfterID: after, Kinds: []string{}, PageSize: changePage})
		if err != nil {
			return nil, err
		}
		for _, c := range page {
			if err := files[FileChanges].add(Change{ID: c.ID, OccurredAt: c.OccurredAt, Kind: c.Kind, AssetID: c.AssetID, ActorID: c.ActorID, Data: c.Data}); err != nil {
				return nil, err
			}
			after = c.ID
		}
		if len(page) < changePage {
			break
		}
	}
	return snaps, nil
}

// CAKeyAAD são os dados adicionais da cifragem de uma chave de CA exportada.
func CAKeyAAD(tenant, ca uuid.UUID) []byte {
	return append(append([]byte{}, tenant[:]...), ca[:]...)
}

func scope(tenant *uuid.UUID) string {
	if tenant == nil {
		return "catalog"
	}
	return "tenant"
}

// jsonl grava um arquivo de registros num temporário, contando e somando.
type jsonl struct {
	f       *os.File
	buf     *bufio.Writer
	h       hash.Hash
	n       int64
	records int
}

func newJSONL(path string) (*jsonl, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &jsonl{f: f, buf: bufio.NewWriter(f), h: sha256.New()}, nil
}

func (j *jsonl) add(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	j.h.Write(b)
	j.n += int64(len(b))
	j.records++
	_, err = j.buf.Write(b)
	return err
}

// finish descarrega o buffer e volta ao início para copiar para o tar.
func (j *jsonl) finish() error {
	if err := j.buf.Flush(); err != nil {
		return err
	}
	_, err := j.f.Seek(0, io.SeekStart)
	return err
}

func addFile(tw *tar.Writer, name string, r io.Reader, size int64, mod time.Time) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: size, ModTime: mod, Format: tar.FormatPAX}); err != nil {
		return err
	}
	_, err := io.CopyN(tw, r, size)
	return err
}

func addBytes(tw *tar.Writer, name string, b []byte, mod time.Time) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(b)), ModTime: mod, Format: tar.FormatPAX}); err != nil {
		return err
	}
	_, err := tw.Write(b)
	return err
}

// Record registra a exportação no change_log (tenant_exported): tirar os
// dados do tenant é um evento de auditoria. actor nulo = linha de comando.
func Record(ctx context.Context, q *store.Queries, actor *uuid.UUID, m Manifest, withCAKey bool, via string) error {
	data, err := json.Marshal(map[string]any{
		"as_of": m.AsOf, "format_version": m.FormatVersion, "snapshot_bodies": m.SnapshotBodies,
		"with_ca_key": withCAKey, "via": via,
	})
	if err != nil {
		return err
	}
	_, err = q.AppendChange(ctx, store.AppendChangeParams{TenantID: m.Tenant.ID, Kind: "tenant_exported", ActorID: actor, Data: data})
	return err
}

// NewExportKey gera uma chave de exportação (32 bytes aleatórios).
func NewExportKey() []byte {
	k := make([]byte, 32)
	rand.Read(k)
	return k
}
