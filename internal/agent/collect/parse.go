package collect

import (
	"bufio"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/turetos/invenire/pkg/snapshot"
)

// Parsers dos formatos Linux, separados para poderem ser testados em qualquer SO.

func parseOSRelease(r io.Reader) map[string]string {
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

// parseDpkgStatus lê o banco do dpkg direto, sem chamar dpkg-query.
func parseDpkgStatus(r io.Reader) ([]snapshot.Software, error) {
	var out []snapshot.Software
	fields := map[string]string{}
	flush := func() {
		if strings.HasSuffix(fields["Status"], " installed") && fields["Package"] != "" {
			out = append(out, snapshot.Software{
				Source:    snapshot.SourceDpkg,
				PackageID: fields["Package"],
				Name:      fields["Package"],
				Publisher: fields["Maintainer"],
				Version:   fields["Version"],
				Arch:      fields["Architecture"],
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

func parseRpmOutput(b []byte) []snapshot.Software {
	var out []snapshot.Software
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 6 || f[0] == "gpg-pubkey" {
			continue
		}
		version := f[2]
		if f[1] != "(none)" && f[1] != "" {
			version = f[1] + ":" + version
		}
		sw := snapshot.Software{
			Source:    snapshot.SourceRpm,
			PackageID: f[0],
			Name:      f[0],
			Version:   version,
			Arch:      f[3],
		}
		if f[4] != "(none)" {
			sw.Publisher = f[4]
		}
		if sec, err := strconv.ParseInt(f[5], 10, 64); err == nil {
			sw.InstallDate = time.Unix(sec, 0).UTC().Format(time.RFC3339)
		}
		out = append(out, sw)
	}
	return out
}
