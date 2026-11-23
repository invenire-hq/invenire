// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// pageData é o que todo template recebe.
type pageData struct {
	Title string
	Nav   string // item do menu em destaque
	S     *session
	Flash *flash
	Data  any
}

var funcs = template.FuncMap{
	"t":        msg,
	"ago":      ago,
	"when":     when,
	"deref":    deref,
	"join":     strings.Join,
	"has":      slices.Contains[[]string],
	"orDash":   orDash,
	"short":    shortID,
	"event":    eventLabel,
	"summary":  eventSummary,
	"plural":   plural,
	"sameUnit": sameUnit,
	"label":    label,
	"list":     func(v ...string) []string { return v },
	"mul100":   func(f float32) float64 { return float64(f) * 100 },
	"depth":    func(path string) int { return min(strings.Count(path, "/"), 6) },
}

// parseTemplates lê cada página com o layout. As páginas são os arquivos de
// templates/ que não começam com "_".
func parseTemplates() (map[string]*template.Template, error) {
	names, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	pages := map[string]*template.Template{}
	for _, n := range names {
		base := strings.TrimSuffix(path.Base(n), ".html")
		if strings.HasPrefix(base, "_") {
			continue
		}
		t, err := template.New(base).Funcs(funcs).ParseFS(templateFS, "templates/_layout.html", n)
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", base, err)
		}
		pages[base] = t
	}
	return pages, nil
}

// render escreve a página inteira, ou 500 se o template falhar (antes de
// mandar qualquer byte).
func (p *Panel) render(w http.ResponseWriter, r *http.Request, status int, page string, s *session, d pageData) {
	t, ok := p.pages[page]
	if !ok {
		panic("web: página sem template: " + page)
	}
	d.S = s
	if d.Flash == nil {
		d.Flash = takeFlash(w, r)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", d); err != nil {
		p.log.ErrorContext(r.Context(), "template do painel", "page", page, "err", err)
		http.Error(w, msg("error.internal"), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}

func shortID(id uuid.UUID) string { return id.String()[:8] }

func plural(n any, one, many string) string {
	if fmt.Sprint(n) == "1" {
		return msg(one)
	}
	return msg(many)
}

func sameUnit(a *uuid.UUID, b uuid.UUID) bool { return a != nil && *a == b }

// when é a data e hora no fuso do servidor (TZ).
func when(v any) string {
	switch t := v.(type) {
	case time.Time:
		return t.Local().Format("02/01/2006 15:04")
	case *time.Time:
		if t == nil {
			return msg("time.never")
		}
		return t.Local().Format("02/01/2006 15:04")
	}
	return ""
}

// ago é o tempo desde t, por extenso e curto.
func ago(v any) string {
	var t time.Time
	switch x := v.(type) {
	case time.Time:
		t = x
	case *time.Time:
		if x == nil {
			return msg("time.never")
		}
		t = *x
	default:
		return ""
	}
	d := time.Since(t)
	switch {
	case d < -time.Minute:
		return msg("time.future", when(t))
	case d < time.Minute:
		return msg("time.now")
	case d < time.Hour:
		return msg("time.minutes", int(d.Minutes()))
	case d < 48*time.Hour:
		return msg("time.hours", int(d.Hours()))
	}
	return msg("time.days", int(d.Hours()/24))
}

// eventLabel é o nome de um evento do change_log; um tipo sem tradução
// aparece como está.
func eventLabel(kind string) string {
	if m, ok := catalog["event."+kind]; ok {
		return m
	}
	return kind
}

type kv struct{ K, V string }

// eventSummary mostra os dados de um evento sem os identificadores internos.
// Os valores podem ter texto vindo da máquina: vão para o template como
// texto, e ele escapa.
func eventSummary(data json.RawMessage) []kv {
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		if k == "id" || strings.HasSuffix(k, "_id") || k == "remap" || k == "fingerprint" || k == "previous_fingerprint" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]kv, 0, len(keys))
	for _, k := range keys {
		var v string
		switch x := m[k].(type) {
		case nil:
			v = "–"
		case string:
			v = x
		case float64, bool:
			v = fmt.Sprint(x)
		default:
			b, _ := json.Marshal(x)
			v = string(b)
		}
		if utf8.RuneCountInString(v) > 160 {
			v = string([]rune(v)[:160]) + "…"
		}
		out = append(out, kv{k, v})
	}
	return out
}
