package api_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/athosbes/invenire/internal/server/api"
	"github.com/athosbes/invenire/internal/server/export"
	"github.com/athosbes/invenire/internal/server/ingest"
	"github.com/athosbes/invenire/internal/server/normalize"
	"github.com/athosbes/invenire/pkg/snapshot"
)

// download faz GET e devolve status, corpo e cabeçalhos.
func (e *env) download(path, token string) (int, []byte, http.Header) {
	e.t.Helper()
	req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := e.client(nil).Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp.StatusCode, b, resp.Header
}

// rewrite remonta o pacote trocando o conteúdo das entradas que change
// alterar (change devolve nil para manter).
func rewrite(t *testing.T, pkg []byte, change func(name string, b []byte) []byte) []byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(pkg))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var out bytes.Buffer
	gw := gzip.NewWriter(&out)
	tw := tar.NewWriter(gw)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		if nb := change(h.Name, b); nb != nil {
			b = nb
		}
		h.Size = int64(len(b))
		tw.WriteHeader(h)
		tw.Write(b)
	}
	tw.Close()
	gw.Close()
	return out.Bytes()
}

func verify(t *testing.T, pkg, key []byte) *export.Report {
	t.Helper()
	rep, err := export.Verify(bytes.NewReader(pkg), key)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestTenantExport(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	rules, _ := normalize.Default()
	worker := ingest.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)), rules)

	ubs, _, _ := api.CreateUnit(ctx, e.q, e.tenant.ID, nil, nil, "ubs", "UBS Centro")
	tok, _, _ := api.NewEnrollmentToken(ctx, e.q, e.tenant.ID, &ubs.ID, "", time.Hour)
	_, a, certA := e.enroll(tok, testHost)
	interno := snapshot.Software{Source: "win_registry", Name: "Sistema Interno 2.1", Version: "2.1"}
	t0 := time.Now().UTC().Truncate(time.Second).Add(-2 * time.Hour)
	sum1 := e.sendSoftware(certA, a.AgentID, testHost, "", t0, chrome130, interno)
	e.sendSoftware(certA, a.AgentID, testHost, sum1, t0.Add(time.Hour), chrome131, interno)

	// Outro tenant, com uma máquina que não pode aparecer no pacote.
	other, _ := e.q.CreateTenant(ctx, "outro")
	otherTok, _, _ := api.NewEnrollmentToken(ctx, e.q, other.ID, nil, "", time.Hour)
	_, b, certB := e.enroll(otherTok, secondHost)
	e.sendSoftware(certB, b.AgentID, secondHost, "", t0, chrome130)
	if _, err := worker.Drain(ctx); err != nil {
		t.Fatal(err)
	}

	admin := e.user(e.tenant, "admin", "admin")
	reader := e.user(e.tenant, "leitor", "read")
	e.call("PUT", "/v1/assets/"+a.AssetID.String()+"/labels/dados", admin, api.LabelRequest{Value: "pessoais"}, nil)
	var unmapped api.UnmappedList
	e.call("GET", "/v1/software/unmapped", reader, nil, &unmapped)
	if len(unmapped.Items) != 1 {
		t.Fatalf("não identificados: %+v", unmapped.Items)
	}
	var mapped api.MapResponse
	if status := e.call("PUT", "/v1/software/raw/"+unmapped.Items[0].ID.String(), admin,
		api.MapRequest{Vendor: "Minha Organização", Product: "Sistema Interno"}, &mapped); status != http.StatusOK || mapped.ProductScope != "tenant" {
		t.Fatalf("mapear: %d %+v", status, mapped)
	}

	if status, _, _ := e.download("/v1/export", reader); status != http.StatusForbidden {
		t.Fatalf("leitor exportou: %d", status)
	}
	status, pkg, hdr := e.download("/v1/export", admin)
	if status != http.StatusOK || hdr.Get("Content-Type") != "application/gzip" || hdr.Get(api.ExportKeyHeader) != "" ||
		!strings.Contains(hdr.Get("Content-Disposition"), "teste-") {
		t.Fatalf("exportar: %d %v", status, hdr)
	}
	rep := verify(t, pkg, nil)
	if !rep.OK() {
		t.Fatalf("pacote com problemas: %v", rep.Problems)
	}
	m := rep.Manifest
	if m.Tenant.ID != e.tenant.ID || m.AsOf == 0 || rep.Snapshots != 2 || len(rep.ChainBreaks) != 0 || m.CAKey != nil ||
		rep.Records[export.FileAssets] != 1 || rep.Records[export.FileAgents] != 1 || rep.Records[export.FileUnits] != 1 ||
		rep.Records[export.FileLabels] != 1 || rep.Records[export.FileMappings] != 1 || rep.Records[export.FileProducts] != 1 ||
		rep.Records[export.FileTenantCAs] != 1 || rep.Records[export.FileActors] < 2 || rep.Records[export.FileChanges] == 0 {
		t.Fatalf("conteúdo: %+v %v", m, rep.Records)
	}
	// Nada do outro tenant.
	raw := gunzip(t, pkg)
	if bytes.Contains(raw, []byte(secondHost.Hostname)) || bytes.Contains(raw, []byte(b.AssetID.String())) || bytes.Contains(raw, []byte(other.ID.String())) {
		t.Fatal("o pacote tem dados do outro tenant")
	}
	// Os bytes de cada snapshot são os enviados.
	if !bytes.Contains(raw, []byte(`"name":"Sistema Interno 2.1"`)) {
		t.Fatal("snapshot sem o conteúdo original")
	}

	// Com a chave da CA: abre só com a chave de exportação certa.
	status, withKey, hdr := e.download("/v1/export?with_ca_key=true", admin)
	key, _ := base64.StdEncoding.DecodeString(hdr.Get(api.ExportKeyHeader))
	if status != http.StatusOK || len(key) != 32 {
		t.Fatalf("exportar com chave: %d %v", status, hdr)
	}
	if rep := verify(t, withKey, key); !rep.OK() || rep.CAKeys != 1 || rep.Manifest.CAKey == nil {
		t.Fatalf("chave da CA: %v %d", rep.Problems, rep.CAKeys)
	}
	if rep := verify(t, withKey, export.NewExportKey()); rep.OK() {
		t.Fatal("chave de exportação errada abriu a chave da CA")
	}
	if rep := verify(t, withKey, nil); !rep.OK() || rep.CAKeys != 0 {
		t.Fatalf("sem chave: %v", rep.Problems)
	}

	// Adulterações.
	first := true
	tampered := rewrite(t, pkg, func(name string, b []byte) []byte {
		if strings.HasPrefix(name, export.SnapshotDir) && first {
			first = false
			return bytes.Replace(b, []byte("Sistema Interno"), []byte("Sistema Externo"), 1)
		}
		return nil
	})
	if rep := verify(t, tampered, nil); rep.OK() {
		t.Fatal("snapshot alterado passou")
	}
	tampered = rewrite(t, pkg, func(name string, b []byte) []byte {
		if name == export.FileUnits {
			return bytes.Replace(b, []byte("UBS Centro"), []byte("UBS Bairro"), 1)
		}
		return nil
	})
	if rep := verify(t, tampered, nil); rep.OK() || !strings.Contains(strings.Join(rep.Problems, ";"), export.FileUnits) {
		t.Fatalf("unidade alterada: %v", rep.Problems)
	}
	if rep, err := export.Verify(bytes.NewReader(pkg[:len(pkg)/2]), nil); err == nil && rep.OK() {
		t.Fatal("pacote cortado passou")
	}

	// Corrente quebrada: aceita na origem, preservada e apontada no pacote.
	e.sendSoftware(certA, a.AgentID, testHost, strings.Repeat("0", 64), t0.Add(90*time.Minute), chrome131)
	_, pkg, _ = e.download("/v1/export", admin)
	if rep := verify(t, pkg, nil); !rep.OK() || len(rep.ChainBreaks) != 1 || rep.Snapshots != 3 {
		t.Fatalf("corrente quebrada: %v %+v", rep.Problems, rep.ChainBreaks)
	}

	// Snapshot recebido antes de o servidor guardar os bytes: vai como
	// payload, e o pacote continua íntegro.
	if _, err := e.pool.Exec(ctx, "UPDATE snapshot SET body = NULL WHERE sha256 = decode($1, 'hex')", sum1); err != nil {
		t.Fatal(err)
	}
	_, pkg, _ = e.download("/v1/export", admin)
	if rep := verify(t, pkg, nil); !rep.OK() || rep.PayloadOnly != 1 || rep.Snapshots != 3 || rep.Manifest.SnapshotPayloads != 1 {
		t.Fatalf("snapshot sem bytes: %v %d", rep.Problems, rep.PayloadOnly)
	}

	if n := count(e.kinds(e.tenant.ID), "tenant_exported"); n != 4 {
		t.Fatalf("tenant_exported: %d", n)
	}
}

func gunzip(t *testing.T, b []byte) []byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(gz)
	return out
}
