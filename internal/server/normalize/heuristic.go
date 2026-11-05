package normalize

import (
	"regexp"
	"strings"
)

var (
	// Marcas entre parênteses no fim do nome: arquitetura, idioma, escopo da
	// instalação. "(64-bit x64)", "(x64 en-US)", "(User)", "(remove only)".
	trailingTag = regexp.MustCompile(`(?i)\s*\((?:x64|x86|arm64|amd64|64-?bit|32-?bit|64 bit|32 bit|user|current user|machine|remove only|preview)\b[^()]*\)$`)
	// Arquitetura solta no fim: "Postman Agent x64", "PowerShell 7-x64".
	trailingArch = regexp.MustCompile(`(?i)[\s-]+(?:x64|x86|arm64|amd64|64-?bit|32-?bit)$`)
	// Versão no fim: "7-Zip 26.04", "- 14.44.35211", "version 5.7.2.358",
	// "go1.26.1", "7.1.2-17 Q16-HDRI". Exige um dígito depois de um separador.
	trailingVersion = regexp.MustCompile(`(?i)(?:\s+-)?\s+(?:version\s+)?(?:v|go)?\d+(?:[.\-_]\w+)*(?:\s+Q\d+[\w-]*)?$`)
	// Data no fim, entre parênteses: "(2026-03-15)".
	trailingDate = regexp.MustCompile(`\s*\(\d{4}-\d{2}-\d{2}\)$`)

	// Sufixos societários do fabricante.
	legalSuffix = regexp.MustCompile(`(?i)[\s,]+(?:inc\.?|incorporated|llc|l\.l\.c\.|corp\.?|corporation|co\.|ltd\.?|limited|gmbh|ag|s\.a\.?|s\.r\.l\.|s\.a\.s\.|pte\.?\s*ltd\.?|bvba|b\.v\.|oy|ab)$`)
)

// windowsHeuristic propõe fabricante e produto para um nome do registro do
// Windows. Não conhece o produto: só tira do nome o que costuma variar entre
// instalações do mesmo produto.
func windowsHeuristic(name, vendor string) (Match, bool) {
	product := name
	for {
		before := product
		for _, re := range []*regexp.Regexp{trailingDate, trailingTag, trailingArch, trailingVersion} {
			product = strings.TrimSpace(re.ReplaceAllString(product, ""))
		}
		if product == before {
			break
		}
	}
	vendor = cleanVendor(vendor)
	// "Google Chrome" de "Google" → "Chrome", como no catálogo e no CPE. Vale
	// também a primeira palavra do fabricante: "Cisco Packet Tracer" de
	// "Cisco Systems".
	if vendor != "" {
		first, _, _ := strings.Cut(vendor, " ")
		for _, prefix := range []string{vendor + " ", first + " "} {
			if len(product) > len(prefix) && strings.EqualFold(product[:len(prefix)], prefix) {
				product = strings.TrimSpace(product[len(prefix):])
				break
			}
		}
	}
	if product == "" || !strings.ContainsFunc(product, isLetter) {
		return Match{}, false
	}
	m := Match{Vendor: vendor, Product: product, Confidence: confHeuristic, Rule: "heuristic:windows"}
	if vendor == "" {
		m.Confidence = confHeuristicAnon
	}
	return m, true
}

func cleanVendor(v string) string {
	v = strings.TrimSpace(v)
	for {
		next := strings.TrimSpace(legalSuffix.ReplaceAllString(v, ""))
		next = strings.TrimRight(next, ",")
		if next == v {
			return v
		}
		v = next
	}
}

func isLetter(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r > 0x7f
}
