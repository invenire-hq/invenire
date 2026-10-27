package snapshot

import "strings"

// UUIDs que fabricantes gravam no SMBIOS sem preencher de verdade. Usá-los
// como chave juntaria máquinas diferentes num mesmo ativo.
var junkUUIDs = map[string]bool{
	"00000000-0000-0000-0000-000000000000": true,
	"ffffffff-ffff-ffff-ffff-ffffffffffff": true,
	"03000200-0400-0500-0006-000700080009": true,
	"12345678-1234-5678-90ab-cddeefaabbcc": true,
}

// NormalizeUUID devolve o UUID em minúsculas, ou vazio se ele for inválido
// ou um dos valores-lixo conhecidos.
func NormalizeUUID(u string) string {
	u = strings.ToLower(strings.TrimSpace(u))
	if len(u) != 36 || junkUUIDs[u] {
		return ""
	}
	for i, c := range u {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return ""
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return ""
			}
		}
	}
	return u
}

// Fingerprint é a chave do ativo: o SMBIOS UUID, ou o machine-id/MachineGuid
// quando o UUID falta ou é lixo. Os prefixos evitam colisão entre os dois.
func Fingerprint(h Host) string {
	if u := NormalizeUUID(h.SMBIOSUUID); u != "" {
		return "smbios:" + u
	}
	if m := strings.ToLower(strings.Trim(strings.TrimSpace(h.MachineID), "{}")); m != "" {
		return "machine:" + m
	}
	return ""
}
