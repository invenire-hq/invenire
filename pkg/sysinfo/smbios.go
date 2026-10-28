package sysinfo

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// ParseSMBIOSSystem percorre uma tabela SMBIOS bruta e extrai a estrutura
// tipo 1 (System Information). major/minor são a versão do SMBIOS: a partir
// da 2.6 os três primeiros campos do UUID vêm em little-endian.
func ParseSMBIOSSystem(table []byte, major, minor byte) (System, error) {
	for off := 0; off+4 <= len(table); {
		typ, length := table[off], int(table[off+1])
		if length < 4 || off+length > len(table) {
			return System{}, errors.New("smbios: estrutura truncada")
		}
		formatted := table[off : off+length]

		// Strings ficam depois da área formatada e terminam com dois nulos.
		end := off + length
		for end+1 < len(table) && !(table[end] == 0 && table[end+1] == 0) {
			end++
		}
		if end+1 >= len(table) {
			return System{}, errors.New("smbios: área de strings sem terminador")
		}
		strs := strings.Split(string(table[off+length:end]), "\x00")
		str := func(i int) string {
			if i >= len(formatted) || formatted[i] == 0 || int(formatted[i]) > len(strs) {
				return ""
			}
			return strings.TrimSpace(strs[formatted[i]-1])
		}

		switch typ {
		case 1:
			s := System{Manufacturer: str(4), Product: str(5), Version: str(6), Serial: str(7)}
			if len(formatted) >= 0x18 {
				s.UUID = formatUUID(formatted[8:24], major > 2 || major == 2 && minor >= 6)
			}
			return s, nil
		case 127: // fim da tabela
			return System{}, errors.New("smbios: estrutura tipo 1 ausente")
		}
		off = end + 2
	}
	return System{}, errors.New("smbios: estrutura tipo 1 ausente")
}

func formatUUID(b []byte, littleEndian bool) string {
	var u [16]byte
	copy(u[:], b)
	if littleEndian {
		binary.BigEndian.PutUint32(u[0:], binary.LittleEndian.Uint32(b[0:]))
		binary.BigEndian.PutUint16(u[4:], binary.LittleEndian.Uint16(b[4:]))
		binary.BigEndian.PutUint16(u[6:], binary.LittleEndian.Uint16(b[6:]))
	}
	return fmt.Sprintf("%X-%X-%X-%X-%X", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}
