// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/invenire-hq/invenire/internal/server/pki"
)

// Report é o resultado da conferência de um pacote.
type Report struct {
	Manifest  Manifest
	Records   map[string]int
	Snapshots int
	// PayloadOnly é quantos snapshots vieram sem os bytes originais: o
	// sha256 deles não pode ser reconferido, só o encadeamento.
	PayloadOnly int
	// ChainBreaks são os snapshots cujo prev_sha256 não é o sha256 do
	// anterior do mesmo ativo. Não invalidam o pacote: o servidor aceita e
	// registra a divergência (snapshot_chain_broken), e o pacote a preserva.
	ChainBreaks []ChainBreak
	// CAKeys diz quantas chaves de CA foram abertas e conferidas com o
	// certificado (só com a chave de exportação).
	CAKeys int
	// Problems são as falhas de integridade: com qualquer uma, o pacote não
	// é confiável.
	Problems []string
}

// OK diz se o pacote passou na conferência.
func (r *Report) OK() bool { return len(r.Problems) == 0 }

// ChainBreak é um elo quebrado na corrente de um ativo.
type ChainBreak struct {
	AssetID    uuid.UUID `json:"asset_id"`
	SnapshotID uuid.UUID `json:"snapshot_id"`
	Want       string    `json:"want_prev_sha256"`
	Got        string    `json:"got_prev_sha256"`
}

func (r *Report) problem(format string, args ...any) {
	if len(r.Problems) < 1000 {
		r.Problems = append(r.Problems, fmt.Sprintf(format, args...))
	}
}

// Verify confere um pacote só com o que está nele: o SHA-256 e a contagem de
// cada arquivo contra o manifesto, os bytes de cada snapshot contra o nome e
// os metadados, a corrente de cada ativo e as referências entre registros.
// exportKey, se dada, abre as chaves das CAs e confere cada uma com o
// certificado. O erro só vem quando o pacote nem pode ser lido.
func Verify(r io.Reader, exportKey []byte) (*Report, error) {
	rep := &Report{Records: map[string]int{}}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("não é um .tar.gz: %w", err)
	}
	tr := tar.NewReader(gz)

	got := map[string]FileInfo{}
	bodies := map[string]bool{}
	payloads := map[string]bool{}
	var manifest []byte
	var (
		cas    []TenantCA
		units  []Unit
		assets []Asset
		labels []Label
		agents []Agent
		snaps  []Snapshot
		maps   []Mapping
		prods  []Product
	)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("pacote corrompido: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			rep.problem("entrada que não é arquivo: %s", h.Name)
			continue
		}
		switch name := h.Name; {
		case name == ManifestFile:
			if manifest, err = io.ReadAll(io.LimitReader(tr, 16<<20)); err != nil {
				return nil, err
			}
		case strings.HasPrefix(name, SnapshotDir) && strings.HasSuffix(name, PayloadSuffix):
			b, err := io.ReadAll(io.LimitReader(tr, 256<<20))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			if !json.Valid(b) {
				rep.problem("%s: não é JSON", name)
			}
			sum := strings.TrimSuffix(strings.TrimPrefix(name, SnapshotDir), PayloadSuffix)
			if payloads[sum] || bodies[sum] {
				rep.problem("%s repetido", name)
			}
			payloads[sum] = true
		case strings.HasPrefix(name, SnapshotDir):
			sum := strings.TrimSuffix(strings.TrimPrefix(name, SnapshotDir), ".json")
			hs := sha256.New()
			if _, err := io.Copy(hs, tr); err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			if hex.EncodeToString(hs.Sum(nil)) != sum {
				rep.problem("%s: o conteúdo não tem o sha256 do nome (alterado)", name)
			}
			if bodies[sum] {
				rep.problem("%s repetido", name)
			}
			bodies[sum] = true
		case slices.Contains(Files, name):
			if _, dup := got[name]; dup {
				rep.problem("%s repetido", name)
			}
			info, err := readRecords(tr, name, func(line []byte) error {
				var err error
				switch name {
				case FileTenantCAs:
					cas, err = appendJSON(cas, line)
				case FileUnits:
					units, err = appendJSON(units, line)
				case FileAssets:
					assets, err = appendJSON(assets, line)
				case FileLabels:
					labels, err = appendJSON(labels, line)
				case FileAgents:
					agents, err = appendJSON(agents, line)
				case FileSnapshots:
					snaps, err = appendJSON(snaps, line)
				case FileMappings:
					maps, err = appendJSON(maps, line)
				case FileProducts:
					prods, err = appendJSON(prods, line)
				default:
					if !json.Valid(line) {
						err = errors.New("linha que não é JSON")
					}
				}
				return err
			})
			if err != nil {
				rep.problem("%s: %v", name, err)
			}
			got[name] = info
			rep.Records[name] = info.Records
		default:
			rep.problem("arquivo desconhecido no pacote: %s", name)
		}
	}

	if manifest == nil {
		rep.problem("sem %s: pacote incompleto (cortado no meio?)", ManifestFile)
		return rep, nil
	}
	if err := json.Unmarshal(manifest, &rep.Manifest); err != nil {
		rep.problem("%s inválido: %v", ManifestFile, err)
		return rep, nil
	}
	m := rep.Manifest
	if m.Format != Format || m.FormatVersion != Version {
		rep.problem("formato %q versão %d; este programa lê %q versão %d", m.Format, m.FormatVersion, Format, Version)
		return rep, nil
	}
	for _, name := range Files {
		want, listed := m.Files[name]
		have, present := got[name]
		switch {
		case !listed:
			rep.problem("%s não está no manifesto", name)
		case !present:
			rep.problem("%s está no manifesto mas não no pacote", name)
		case want != have:
			rep.problem("%s não confere com o manifesto (sha256 %s, esperado %s; %d registros, esperado %d)",
				name, have.SHA256, want.SHA256, have.Records, want.Records)
		}
	}
	for name := range m.Files {
		if !slices.Contains(Files, name) {
			rep.problem("manifesto lista arquivo desconhecido: %s", name)
		}
	}
	rep.Snapshots = len(bodies) + len(payloads)
	rep.PayloadOnly = len(payloads)
	if m.SnapshotBodies != len(bodies) || m.SnapshotPayloads != len(payloads) {
		rep.problem("%d snapshots com bytes originais e %d só com payload no pacote; manifesto diz %d e %d",
			len(bodies), len(payloads), m.SnapshotBodies, m.SnapshotPayloads)
	}

	checkReferences(rep, units, assets, labels, agents, snaps, maps, prods, cas, bodies, payloads)
	checkChains(rep, snaps)
	checkCAs(rep, cas, exportKey)
	return rep, nil
}

func readRecords(r io.Reader, name string, each func([]byte) error) (FileInfo, error) {
	hs := sha256.New()
	var info FileInfo
	br := bufio.NewReaderSize(io.TeeReader(r, hs), 1<<16)
	var firstErr error
	for {
		line, err := br.ReadBytes('\n')
		info.Bytes += int64(len(line))
		if len(line) > 0 {
			if line[len(line)-1] != '\n' {
				firstErr = cmpErr(firstErr, errors.New("última linha sem \\n"))
			}
			info.Records++
			if e := each(bytes.TrimSuffix(line, []byte("\n"))); e != nil {
				firstErr = cmpErr(firstErr, fmt.Errorf("linha %d: %w", info.Records, e))
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return info, err
		}
	}
	info.SHA256 = hex.EncodeToString(hs.Sum(nil))
	return info, firstErr
}

func cmpErr(first, err error) error {
	if first != nil {
		return first
	}
	return err
}

func appendJSON[T any](list []T, line []byte) ([]T, error) {
	var v T
	dec := json.NewDecoder(bytes.NewReader(line))
	if err := dec.Decode(&v); err != nil {
		return list, err
	}
	return append(list, v), nil
}

func checkReferences(rep *Report, units []Unit, assets []Asset, labels []Label, agents []Agent,
	snaps []Snapshot, maps []Mapping, prods []Product, cas []TenantCA, bodies, payloads map[string]bool) {
	ids := func(n int, id func(int) uuid.UUID, what string) map[uuid.UUID]bool {
		set := make(map[uuid.UUID]bool, n)
		for i := 0; i < n; i++ {
			if set[id(i)] {
				rep.problem("%s %s repetido", what, id(i))
			}
			set[id(i)] = true
		}
		return set
	}
	unitSet := ids(len(units), func(i int) uuid.UUID { return units[i].ID }, "unidade")
	assetSet := ids(len(assets), func(i int) uuid.UUID { return assets[i].ID }, "ativo")
	agentSet := ids(len(agents), func(i int) uuid.UUID { return agents[i].ID }, "agente")
	caSet := ids(len(cas), func(i int) uuid.UUID { return cas[i].ID }, "CA")
	prodSet := ids(len(prods), func(i int) uuid.UUID { return prods[i].ID }, "produto")
	ids(len(snaps), func(i int) uuid.UUID { return snaps[i].ID }, "snapshot")

	for _, u := range units {
		if u.ParentID != nil && !unitSet[*u.ParentID] {
			rep.problem("unidade %s: mãe %s não está no pacote", u.ID, *u.ParentID)
		}
	}
	for _, a := range assets {
		if a.UnitID != nil && !unitSet[*a.UnitID] {
			rep.problem("ativo %s: unidade %s não está no pacote", a.ID, *a.UnitID)
		}
	}
	for _, l := range labels {
		if !assetSet[l.AssetID] {
			rep.problem("etiqueta %s: ativo %s não está no pacote", l.Key, l.AssetID)
		}
	}
	for _, g := range agents {
		if !assetSet[g.AssetID] {
			rep.problem("agente %s: ativo %s não está no pacote", g.ID, g.AssetID)
		}
		for _, ca := range []*uuid.UUID{g.TenantCAID, g.PrevTenantCAID} {
			if ca != nil && !caSet[*ca] {
				rep.problem("agente %s: CA %s não está no pacote", g.ID, *ca)
			}
		}
	}
	listed := map[string]bool{}
	for _, s := range snaps {
		if !assetSet[s.AssetID] {
			rep.problem("snapshot %s: ativo %s não está no pacote", s.ID, s.AssetID)
		}
		if !agentSet[s.AgentID] {
			rep.problem("snapshot %s: agente %s não está no pacote", s.ID, s.AgentID)
		}
		switch {
		case s.Body != "" && s.Payload == "":
			if s.Body != SnapshotDir+s.SHA256+".json" {
				rep.problem("snapshot %s: body %q não é snapshots/<sha256>.json", s.ID, s.Body)
			}
			if !bodies[s.SHA256] {
				rep.problem("snapshot %s: bytes ausentes (%s)", s.ID, s.Body)
			}
		case s.Body == "" && s.Payload != "":
			if s.Payload != SnapshotDir+s.SHA256+PayloadSuffix {
				rep.problem("snapshot %s: payload %q não é snapshots/<sha256>%s", s.ID, s.Payload, PayloadSuffix)
			}
			if !payloads[s.SHA256] {
				rep.problem("snapshot %s: payload ausente (%s)", s.ID, s.Payload)
			}
		default:
			rep.problem("snapshot %s: precisa de body ou de payload, um só", s.ID)
		}
		listed[s.SHA256] = true
	}
	for sum := range bodies {
		if !listed[sum] {
			rep.problem("%s%s.json não está em %s", SnapshotDir, sum, FileSnapshots)
		}
	}
	for sum := range payloads {
		if !listed[sum] {
			rep.problem("%s%s%s não está em %s", SnapshotDir, sum, PayloadSuffix, FileSnapshots)
		}
	}
	for _, mp := range maps {
		if mp.ProductScope == "tenant" && !prodSet[mp.SoftwareID] {
			rep.problem("mapeamento de %q: produto do tenant %s não está em %s", mp.RawName, mp.SoftwareID, FileProducts)
		}
	}
}

// checkChains refaz, por ativo e em ordem de coleta, a ligação de cada
// prev_sha256 com o sha256 do snapshot anterior.
func checkChains(rep *Report, snaps []Snapshot) {
	byAsset := map[uuid.UUID][]Snapshot{}
	for _, s := range snaps {
		byAsset[s.AssetID] = append(byAsset[s.AssetID], s)
	}
	assets := make([]uuid.UUID, 0, len(byAsset))
	for a := range byAsset {
		assets = append(assets, a)
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].String() < assets[j].String() })
	for _, a := range assets {
		list := byAsset[a]
		sort.SliceStable(list, func(i, j int) bool {
			if !list[i].CollectedAt.Equal(list[j].CollectedAt) {
				return list[i].CollectedAt.Before(list[j].CollectedAt)
			}
			return list[i].ReceivedAt.Before(list[j].ReceivedAt)
		})
		prev := ""
		for _, s := range list {
			if s.PrevSHA256 != prev {
				rep.ChainBreaks = append(rep.ChainBreaks, ChainBreak{AssetID: a, SnapshotID: s.ID, Want: prev, Got: s.PrevSHA256})
			}
			prev = s.SHA256
		}
	}
}

func checkCAs(rep *Report, cas []TenantCA, exportKey []byte) {
	var opener *pki.MasterKey
	if exportKey != nil {
		var err error
		if opener, err = pki.NewMasterKey(exportKey); err != nil {
			rep.problem("chave de exportação: %v", err)
			return
		}
	}
	for _, c := range cas {
		blk, _ := pem.Decode([]byte(c.CertPEM))
		if blk == nil {
			rep.problem("CA %s: certificado não é PEM", c.ID)
			continue
		}
		cert, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			rep.problem("CA %s: certificado inválido: %v", c.ID, err)
			continue
		}
		if pki.Fingerprint(blk.Bytes) != c.Fingerprint {
			rep.problem("CA %s: fingerprint não confere com o certificado", c.ID)
		}
		if opener == nil || len(c.KeySealed) == 0 {
			continue
		}
		keyDER, err := opener.Open(c.KeySealed, CAKeyAAD(rep.Manifest.Tenant.ID, c.ID))
		if err != nil {
			rep.problem("CA %s: a chave de exportação não abre a chave da CA", c.ID)
			continue
		}
		if _, err := pki.ParseCA(cert.Raw, keyDER); err != nil {
			rep.problem("CA %s: chave não corresponde ao certificado: %v", c.ID, err)
			continue
		}
		rep.CAKeys++
	}
}
