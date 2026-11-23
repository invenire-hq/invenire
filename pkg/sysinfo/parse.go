// SPDX-License-Identifier: AGPL-3.0-only

package sysinfo

import (
	"bufio"
	"io"
	"strconv"
	"strings"
	"time"
)

// Parsers dos formatos Linux. Ficam fora dos arquivos por plataforma para
// poderem ser usados e testados em qualquer SO (por exemplo, sobre arquivos
// copiados de uma imagem de disco).

// ParseOSRelease lê um arquivo no formato os-release(5).
func ParseOSRelease(r io.Reader) map[string]string {
	m := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		if uq, err := strconv.Unquote(v); err == nil {
			v = uq
		} else {
			v = strings.Trim(v, `'"`)
		}
		m[k] = v
	}
	return m
}

// ParseDpkgStatus lê o banco do dpkg (/var/lib/dpkg/status) e devolve os
// pacotes instalados. location vai para Package.Location.
func ParseDpkgStatus(r io.Reader, location string) ([]Package, error) {
	var out []Package
	fields := map[string]string{}
	flush := func() {
		if strings.HasSuffix(fields["Status"], " installed") && fields["Package"] != "" {
			out = append(out, Package{
				Source:    SourceDpkg,
				ID:        fields["Package"],
				Name:      fields["Package"],
				Publisher: fields["Maintainer"],
				Version:   fields["Version"],
				Arch:      fields["Architecture"],
				Location:  location,
			})
		}
		clear(fields)
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			flush()
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			continue // continuação de campo multilinha (Description, Conffiles)
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			fields[k] = strings.TrimSpace(v)
		}
	}
	flush()
	return out, sc.Err()
}

// RpmQueryFormat é o --queryformat que ParseRpmQuery espera.
const RpmQueryFormat = `%{NAME}\t%{EPOCH}\t%{VERSION}-%{RELEASE}\t%{ARCH}\t%{VENDOR}\t%{INSTALLTIME}\n`

// ParseRpmQuery lê a saída de `rpm -qa --qf RpmQueryFormat`.
func ParseRpmQuery(b []byte, location string) []Package {
	var out []Package
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 6 || f[0] == "gpg-pubkey" {
			continue
		}
		version := f[2]
		if f[1] != "(none)" && f[1] != "" {
			version = f[1] + ":" + version
		}
		p := Package{
			Source:   SourceRpm,
			ID:       f[0],
			Name:     f[0],
			Version:  version,
			Arch:     f[3],
			Location: location,
		}
		if f[4] != "(none)" {
			p.Publisher = f[4]
		}
		if sec, err := strconv.ParseInt(f[5], 10, 64); err == nil {
			p.InstallDate = time.Unix(sec, 0).UTC().Format(time.RFC3339)
		}
		out = append(out, p)
	}
	return out
}
