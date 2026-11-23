// SPDX-License-Identifier: AGPL-3.0-only

package download_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/invenire-hq/invenire/internal/server/download"
)

// fakeAgents monta uma pasta como a do scripts/build.sh, com executáveis de
// mentira.
func fakeAgents(t *testing.T, platforms ...string) string {
	t.Helper()
	dir := t.TempDir()
	var sums strings.Builder
	for _, p := range platforms {
		name := "invenire-agent-" + p
		if strings.HasPrefix(p, "windows") {
			name += ".exe"
		}
		data := []byte("executável de mentira para " + p)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(data)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), name)
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const token = "inv_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func cfg(pin string, remaining int) download.Config {
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	return download.Config{Server: "https://invenire.exemplo.org:8443", Pin: pin, Token: token, ExpiresAt: now.Add(24 * time.Hour), Remaining: remaining, Generated: now}
}

func TestOpen(t *testing.T) {
	if a := download.Open(""); a.Problem() == "" {
		t.Fatal("sem pasta deveria haver um motivo para o painel")
	}
	if a := download.Open(t.TempDir()); a.Problem() == "" {
		t.Fatal("pasta sem SHA256SUMS deveria haver um motivo")
	}
	dir := fakeAgents(t, "windows-amd64", "linux-amd64")
	a := download.Open(dir)
	if a.Problem() != "" || !a.Has("windows-amd64") || !a.Has("linux-amd64") || a.Has("linux-arm64") {
		t.Fatalf("problema %q, faltam %v", a.Problem(), a.Missing())
	}
	// Executável que não confere com o SHA256SUMS fica de fora.
	os.WriteFile(filepath.Join(dir, "invenire-agent-linux-amd64"), []byte("alterado"), 0o755)
	if a := download.Open(dir); a.Has("linux-amd64") || !a.Has("windows-amd64") {
		t.Fatal("executável alterado não deveria ser distribuído")
	}
}

func TestServe(t *testing.T) {
	dir := fakeAgents(t, "windows-amd64", "linux-amd64", "linux-arm64")
	a := download.Open(dir)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /download/agent/{platform}", a.ServeAgent)
	mux.HandleFunc("GET /download/SHA256SUMS", a.ServeSums)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	get := func(path string) (int, []byte, http.Header) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b, resp.Header
	}
	st, body, h := get("/download/agent/linux-arm64")
	want, _ := os.ReadFile(filepath.Join(dir, "invenire-agent-linux-arm64"))
	if st != 200 || !bytes.Equal(body, want) || !strings.Contains(h.Get("Content-Disposition"), "invenire-agent-linux-arm64") {
		t.Fatalf("agente: %d %q", st, h.Get("Content-Disposition"))
	}
	if st, _, _ := get("/download/agent/darwin-amd64"); st != 404 {
		t.Fatalf("plataforma desconhecida: %d", st)
	}
	if st, _, _ := get("/download/agent/..%2fSHA256SUMS"); st != 404 {
		t.Fatalf("caminho estranho: %d", st)
	}
	sums, _ := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if st, body, _ := get("/download/SHA256SUMS"); st != 200 || !bytes.Equal(body, sums) {
		t.Fatalf("SHA256SUMS: %d", st)
	}
}

func readZip(t *testing.T, b []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	m := map[string][]byte{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		m[f.Name], _ = io.ReadAll(rc)
		rc.Close()
	}
	return m
}

func readTarGz(t *testing.T, b []byte) (map[string][]byte, map[string]int64) {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	files, modes := map[string][]byte{}, map[string]int64{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		files[h.Name], _ = io.ReadAll(tr)
		modes[h.Name] = h.Mode
	}
	return files, modes
}

func sumsOK(t *testing.T, files map[string][]byte, prefix string) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(string(files[prefix+"SHA256SUMS"])), "\n") {
		f := strings.Fields(line)
		h := sha256.Sum256(files[prefix+f[1]])
		if hex.EncodeToString(h[:]) != f[0] {
			t.Fatalf("SHA256SUMS do pacote não confere para %s", f[1])
		}
	}
}

func TestPackageWindows(t *testing.T) {
	a := download.Open(fakeAgents(t, "windows-amd64", "linux-amd64"))
	pin := "sha256:" + strings.Repeat("ab", 32)
	name, ctype, body, err := a.Package("windows", cfg(pin, 1))
	if err != nil {
		t.Fatal(err)
	}
	if name != "invenire-agente-windows.zip" || ctype != "application/zip" {
		t.Fatal(name, ctype)
	}
	files := readZip(t, body)
	const d = "invenire-agente/"
	for _, f := range []string{"Instalar.cmd", "instalar.ps1", "invenire-agent.exe", "SHA256SUMS", "instalar.conf", "LEIA-ME.txt"} {
		if _, ok := files[d+f]; !ok {
			t.Fatalf("falta %s no zip (tem %v)", f, keys(files))
		}
	}
	if len(files) != 6 {
		t.Fatalf("arquivos a mais no zip: %v", keys(files))
	}
	if string(files[d+"invenire-agent.exe"]) != "executável de mentira para windows-amd64" {
		t.Fatal("o agente do zip deveria ser o de Windows")
	}
	sumsOK(t, files, d)
	cmd := string(files[d+"Instalar.cmd"])
	if !strings.Contains(cmd, "\r\n") || strings.Contains(strings.ReplaceAll(cmd, "\r\n", ""), "\n") {
		t.Fatal("Instalar.cmd precisa de CRLF em todas as linhas")
	}
	if !strings.Contains(cmd, "-ExecutionPolicy Bypass") || !strings.Contains(cmd, "-Verb RunAs") {
		t.Fatal("Instalar.cmd deveria pedir elevação e chamar o instalar.ps1 com Bypass")
	}
	if ps1 := files[d+"instalar.ps1"]; !bytes.HasPrefix(ps1, []byte("\xef\xbb\xbf")) {
		t.Fatal("instalar.ps1 precisa de BOM para o Windows PowerShell 5.1")
	}
	conf := string(files[d+"instalar.conf"])
	for _, want := range []string{"servidor=https://invenire.exemplo.org:8443\r\n", "ca_pin=" + pin + "\r\n", "token=" + token + "\r\n", "apagar_depois=sim\r\n"} {
		if !strings.Contains(conf, want) {
			t.Fatalf("instalar.conf sem %q:\n%s", want, conf)
		}
	}
	// O token só vai no instalar.conf.
	for n, b := range files {
		if n != d+"instalar.conf" && bytes.Contains(b, []byte(token)) {
			t.Fatalf("o token apareceu em %s", n)
		}
	}
}

func TestPackageLinux(t *testing.T) {
	a := download.Open(fakeAgents(t, "windows-amd64", "linux-amd64", "linux-arm64"))
	name, ctype, body, err := a.Package("linux", cfg("", 5))
	if err != nil {
		t.Fatal(err)
	}
	if name != "invenire-agente-linux.tar.gz" || ctype != "application/gzip" {
		t.Fatal(name, ctype)
	}
	files, modes := readTarGz(t, body)
	const d = "invenire-agente/"
	for f, mode := range map[string]int64{"instalar.sh": 0o755, "invenire-agent-linux-amd64": 0o755, "invenire-agent-linux-arm64": 0o755, "instalar.conf": 0o600, "SHA256SUMS": 0o644, "LEIA-ME.txt": 0o644} {
		if _, ok := files[d+f]; !ok {
			t.Fatalf("falta %s no tar.gz (tem %v)", f, keys(files))
		}
		if modes[d+f] != mode {
			t.Fatalf("%s com modo %o, esperado %o", f, modes[d+f], mode)
		}
	}
	if _, ok := files[d+"invenire-agent.exe"]; ok {
		t.Fatal("o pacote Linux não leva o agente de Windows")
	}
	sumsOK(t, files, d)
	if bytes.Contains(files[d+"instalar.sh"], []byte("\r")) {
		t.Fatal("instalar.sh precisa de LF")
	}
	conf := string(files[d+"instalar.conf"])
	for _, want := range []string{"servidor=https://invenire.exemplo.org:8443\n", "ca_pin=\n", "token=" + token + "\n", "apagar_depois=nao\n"} {
		if !strings.Contains(conf, want) {
			t.Fatalf("instalar.conf sem %q:\n%s", want, conf)
		}
	}
	if strings.Contains(conf, "\r") {
		t.Fatal("instalar.conf do Linux com CRLF")
	}
}

func TestPackageRefuses(t *testing.T) {
	a := download.Open(fakeAgents(t, "linux-amd64"))
	if _, _, _, err := a.Package("windows", cfg("", 1)); err == nil {
		t.Fatal("sem agente de Windows não há pacote de Windows")
	}
	if _, _, _, err := a.Package("macos", cfg("", 1)); err == nil {
		t.Fatal("sistema desconhecido")
	}
	for _, bad := range []download.Config{
		{Server: "https://x.org'; rm -rf /", Token: token},
		{Server: "http://x.org", Token: token},
		{Server: "https://x.org", Token: "inv_a\nservidor=https://mal.org"},
		{Server: "https://x.org", Pin: "sha256:zz", Token: token},
	} {
		if _, _, _, err := a.Package("linux", bad); err == nil {
			t.Fatalf("configuração inválida aceita: %+v", bad)
		}
	}
}

func TestServeScript(t *testing.T) {
	for _, kind := range []string{"ps1", "sh"} {
		rec := httptest.NewRecorder()
		download.ServeScript(rec, kind, "https://invenire.exemplo.org", "")
		body := rec.Body.String()
		if rec.Code != 200 || !strings.Contains(body, "'https://invenire.exemplo.org'") {
			t.Fatalf("%s: %d, sem o endereço preenchido", kind, rec.Code)
		}
		if strings.HasPrefix(body, "\ufeff") {
			t.Fatalf("%s servido não leva BOM", kind)
		}
		rec = httptest.NewRecorder()
		download.ServeScript(rec, kind, "https://x.org'$(id)", "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: endereço com aspas aceito", kind)
		}
	}
}

func keys(m map[string][]byte) []string {
	var k []string
	for n := range m {
		k = append(k, n)
	}
	return k
}
