package collect

import (
	"strings"
	"testing"
)

func TestParseSMBIOSSystem(t *testing.T) {
	// Tipo 0 (BIOS) mínimo, depois tipo 1 com strings e UUID, depois fim (127).
	bios := []byte{0, 4, 0, 0, 'x', 0, 0}
	sys := []byte{1, 0x1b, 1, 0, 1, 2, 3, 4,
		0x44, 0x45, 0x4c, 0x4c, 0x42, 0x00, 0x10, 0x35, 0x80, 0x52, 0xb4, 0xc0, 0x4f, 0x4d, 0x4e, 0x32,
		6, 0, 0}
	sys = append(sys, "Dell Inc.\x00OptiPlex 7090\x001.0\x00ABC1234\x00\x00"...)
	end := []byte{127, 4, 0, 0, 0, 0}
	table := append(append(bios, sys...), end...)

	si, err := parseSMBIOSSystem(table, 3, 4)
	if err != nil {
		t.Fatal(err)
	}
	want := systemInfo{"Dell Inc.", "OptiPlex 7090", "ABC1234", "4C4C4544-0042-3510-8052-B4C04F4D4E32"}
	if si != want {
		t.Errorf("got %+v, want %+v", si, want)
	}

	// SMBIOS antigo (< 2.6): UUID inteiro em big-endian.
	si, _ = parseSMBIOSSystem(table, 2, 4)
	if si.UUID != "44454C4C-4200-1035-8052-B4C04F4D4E32" {
		t.Errorf("uuid big-endian: %s", si.UUID)
	}

	if _, err := parseSMBIOSSystem(append(bios, end...), 3, 4); err == nil {
		t.Error("tabela sem tipo 1 deveria falhar")
	}
	if _, err := parseSMBIOSSystem(table[:20], 3, 4); err == nil {
		t.Error("tabela truncada deveria falhar")
	}
}

func TestParseOSRelease(t *testing.T) {
	m := parseOSRelease(strings.NewReader(`# comentário
NAME="Debian GNU/Linux"
VERSION_ID="12"
ID=debian
PRETTY_NAME='Debian 12'
`))
	if m["NAME"] != "Debian GNU/Linux" || m["VERSION_ID"] != "12" || m["ID"] != "debian" || m["PRETTY_NAME"] != "Debian 12" {
		t.Errorf("%v", m)
	}
}

func TestParseDpkgStatus(t *testing.T) {
	list, err := parseDpkgStatus(strings.NewReader(`Package: openssl
Status: install ok installed
Architecture: amd64
Maintainer: Debian OpenSSL Team <pkg-openssl-devel@alioth-lists.debian.net>
Version: 3.0.11-1~deb12u2
Description: Secure Sockets Layer toolkit
 This package contains the openssl binary.

Package: removido
Status: deinstall ok config-files
Version: 1.0

Package: curl
Status: install ok installed
Version: 7.88.1-10
Architecture: amd64`))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("esperava 2 pacotes, veio %d: %+v", len(list), list)
	}
	if o := list[0]; o.Name != "openssl" || o.Version != "3.0.11-1~deb12u2" || o.Arch != "amd64" || o.Source != "dpkg" {
		t.Errorf("%+v", o)
	}
	if list[1].Name != "curl" {
		t.Errorf("%+v", list[1])
	}
}

func TestParseRpmOutput(t *testing.T) {
	list := parseRpmOutput([]byte("bash\t(none)\t5.1.8-9.el9\tx86_64\tRed Hat, Inc.\t1700000000\n" +
		"gpg-pubkey\t(none)\tfd431d51-4ae0493b\t(none)\t(none)\t1700000000\n" +
		"openssl\t1\t3.0.7-27.el9\tx86_64\t(none)\t1700000000\n"))
	if len(list) != 2 {
		t.Fatalf("esperava 2 pacotes: %+v", list)
	}
	if b := list[0]; b.Version != "5.1.8-9.el9" || b.Publisher != "Red Hat, Inc." || b.InstallDate != "2023-11-14T22:13:20Z" {
		t.Errorf("%+v", b)
	}
	if o := list[1]; o.Version != "1:3.0.7-27.el9" || o.Publisher != "" {
		t.Errorf("%+v", o)
	}
}
