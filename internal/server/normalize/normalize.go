// SPDX-License-Identifier: AGPL-3.0-only

// Package normalize reconhece produtos nos nomes brutos de software.
//
// Três regras, nesta ordem:
//
//  1. Catálogo (catalog.json): produtos conhecidos, com os nomes que cada
//     fonte usa para eles. "Google Chrome" no registro e google-chrome-stable
//     no dpkg são o mesmo produto. Confiança 1.
//  2. Pacote Linux: o nome do pacote dpkg ou rpm já é um identificador; vira o
//     produto. Confiança 1.
//  3. Heurística do Windows: tira do nome a versão, a arquitetura e marcas
//     como "(User)", e do fabricante o sufixo societário ("Inc.", "LLC").
//     Confiança 0,7 com fabricante e 0,5 sem: abaixo do limiar, é só uma
//     sugestão para quem mapeia à mão.
//
// Só um resultado com confiança a partir de MinConfidence é aplicado sem
// revisão humana (Normalize); Evaluate devolve o melhor palpite, para a lista
// de software não identificado.
package normalize

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Match é o produto reconhecido num nome bruto.
type Match struct {
	Vendor     string  `json:"vendor,omitempty"` // vazio quando o fabricante não é conhecido
	Product    string  `json:"product"`
	Confidence float32 `json:"confidence"`
	Rule       string  `json:"rule"` // gravado em software_raw.mapped_rule
}

// Fontes de software (as mesmas de pkg/snapshot).
const (
	sourceWinRegistry = "win_registry"
	sourceDpkg        = "dpkg"
	sourceRpm         = "rpm"
)

// DefaultMinConfidence é o limiar padrão para mapear sem revisão.
const DefaultMinConfidence = 0.9

const (
	confCatalog       = 1.0
	confLinuxPackage  = 1.0
	confHeuristic     = 0.7
	confHeuristicAnon = 0.5
)

//go:embed catalog.json
var catalogJSON []byte

type catalogEntry struct {
	ID      string         `json:"id"`
	Vendor  string         `json:"vendor"`
	Product string         `json:"product"` // aceita $1, $2… dos grupos de name
	Match   []catalogMatch `json:"match"`
}

type catalogMatch struct {
	Sources   []string `json:"sources"`
	Name      string   `json:"name"`
	Publisher string   `json:"publisher,omitempty"`
}

type compiled struct {
	entry     *catalogEntry
	sources   map[string]bool
	name      *regexp.Regexp
	publisher *regexp.Regexp
}

// Rules aplica o catálogo e as regras genéricas.
type Rules struct {
	catalog []compiled

	// MinConfidence é o limiar de Normalize. Padrão: DefaultMinConfidence.
	MinConfidence float32
}

// Default carrega o catálogo embutido.
func Default() (*Rules, error) {
	return New(catalogJSON)
}

// New carrega um catálogo em JSON. As expressões não diferenciam maiúsculas
// e são comparadas com o nome já limpo pela ingestão.
func New(catalog []byte) (*Rules, error) {
	var entries []catalogEntry
	if err := json.Unmarshal(catalog, &entries); err != nil {
		return nil, fmt.Errorf("catálogo: %w", err)
	}
	r := &Rules{MinConfidence: DefaultMinConfidence}
	ids := map[string]bool{}
	for i := range entries {
		e := &entries[i]
		if e.ID == "" || e.Product == "" || len(e.Match) == 0 {
			return nil, fmt.Errorf("catálogo: entrada %d sem id, product ou match", i)
		}
		if ids[e.ID] {
			return nil, fmt.Errorf("catálogo: id repetido %q", e.ID)
		}
		ids[e.ID] = true
		for _, m := range e.Match {
			c := compiled{entry: e, sources: map[string]bool{}}
			for _, s := range m.Sources {
				c.sources[s] = true
			}
			if len(c.sources) == 0 {
				return nil, fmt.Errorf("catálogo %s: match sem sources", e.ID)
			}
			var err error
			if c.name, err = regexp.Compile("(?i)" + m.Name); err != nil {
				return nil, fmt.Errorf("catálogo %s: %w", e.ID, err)
			}
			if m.Publisher != "" {
				if c.publisher, err = regexp.Compile("(?i)" + m.Publisher); err != nil {
					return nil, fmt.Errorf("catálogo %s: %w", e.ID, err)
				}
			}
			r.catalog = append(r.catalog, c)
		}
	}
	return r, nil
}

// Normalize devolve o produto só quando a confiança basta para mapear sem
// revisão. É o que a ingestão usa.
func (r *Rules) Normalize(source, rawName, rawVendor string) (Match, bool) {
	m, ok := r.Evaluate(source, rawName, rawVendor)
	return m, ok && m.Confidence >= r.MinConfidence
}

// Evaluate devolve o melhor palpite, com qualquer confiança.
func (r *Rules) Evaluate(source, rawName, rawVendor string) (Match, bool) {
	for _, c := range r.catalog {
		if !c.sources[source] {
			continue
		}
		idx := c.name.FindStringSubmatchIndex(rawName)
		if idx == nil || c.publisher != nil && !c.publisher.MatchString(rawVendor) {
			continue
		}
		product := string(c.name.ExpandString(nil, c.entry.Product, rawName, idx))
		return Match{Vendor: c.entry.Vendor, Product: product, Confidence: confCatalog, Rule: "catalog:" + c.entry.ID}, true
	}
	switch source {
	case sourceDpkg, sourceRpm:
		return Match{Product: strings.ToLower(rawName), Confidence: confLinuxPackage, Rule: "linux:package"}, true
	case sourceWinRegistry:
		return windowsHeuristic(rawName, rawVendor)
	}
	return Match{}, false
}
