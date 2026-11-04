package ingest

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limites, em runas, do texto vindo da máquina inventariada. Nome e
// fabricante formam a chave única de software_raw e o caminho entra na de
// installation: os limites mantêm as chaves abaixo do teto de 2704 bytes de
// um índice btree, mesmo com 4 bytes por runa.
const (
	maxName    = 256
	maxVendor  = 256
	maxVersion = 128
	maxPath    = 512
	maxAttr    = 256 // hostname, SO, fabricante e modelo do ativo
)

// clean prepara texto coletado para ir ao banco e, depois, a quem lê o
// inventário (inclusive agentes de IA): UTF-8 válido, sem caracteres de
// controle nem de formatação invisíveis (NUL, ESC, U+202E…), sem espaço nas
// pontas e truncado. Quem controla a máquina controla esse texto.
//
// O resultado precisa ser estável: o mesmo nome bruto sempre vira a mesma
// chave em software_raw.
func clean(s string, max int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) {
			if r == '\t' || r == '\n' || r == '\r' {
				r = ' '
			} else {
				continue
			}
		}
		if n == max {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// cleanPtr é clean para colunas que aceitam nulo: vazio vira nulo.
func cleanPtr(s string, max int) *string {
	s = clean(s, max)
	if s == "" {
		return nil
	}
	return &s
}
