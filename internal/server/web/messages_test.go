// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// Toda chave usada nos templates e no código existe no catálogo; e as chaves
// montadas na hora (estado, token) também.
func TestCatalogHasEveryKey(t *testing.T) {
	re := regexp.MustCompile(`(?:\{\{t |\(t |msg\()"([a-z0-9_.]+)"`)
	used := map[string]bool{}
	fs.WalkDir(templateFS, "templates", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := templateFS.ReadFile(p)
			for _, m := range re.FindAllSubmatch(b, -1) {
				used[string(m[1])] = true
			}
		}
		return nil
	})
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for _, m := range re.FindAllSubmatch(b, -1) {
			used[string(m[1])] = true
		}
	}
	for _, st := range stateOrder {
		used["state."+st+".label"], used["state."+st+".todo"] = true, true
	}
	for _, s := range []string{"valid", "revoked", "spent", "expired"} {
		used["token."+s] = true
	}
	if len(used) < 100 {
		t.Fatalf("só %d chaves achadas: a expressão está errada?", len(used))
	}
	for k := range used {
		if _, ok := ptBR[k]; !ok {
			t.Errorf("chave sem mensagem: %s", k)
		}
	}
}

func TestTemplatesParse(t *testing.T) {
	pages, err := parseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"login", "setup", "error", "overview", "enroll", "assets", "asset", "units", "unit", "software", "product", "unmapped", "access", "export", "account"} {
		if pages[p] == nil {
			t.Errorf("sem template %s", p)
		}
	}
}

func TestSafeReturn(t *testing.T) {
	for in, want := range map[string]string{
		"/assets?x=1": "/assets?x=1", "": "/", "//evil.org": "/", "/\\evil.org": "/",
		"https://evil.org": "/", "/v1/export": "/", "/a\r\nSet-Cookie: x": "/",
	} {
		if got := safeReturn(in); got != want {
			t.Errorf("safeReturn(%q) = %q, queria %q", in, got, want)
		}
	}
}
