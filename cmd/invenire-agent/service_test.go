// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"strings"
	"testing"
	"time"
)

func TestSystemdUnit(t *testing.T) {
	unit, err := systemdUnit(serviceConfig{
		Bin:      "/opt/inv agent/invenire-agent",
		StateDir: "/var/lib/invenire",
		Interval: 90 * time.Minute,
		MaxQueue: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`ExecStart="/opt/inv agent/invenire-agent" run --interval 1h30m0s --state-dir /var/lib/invenire --max-queue 50` + "\n",
		"ReadWritePaths=-/var/lib/invenire\n",
		"RestartPreventExitStatus=2 3\n",
		"WantedBy=multi-user.target\n",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("falta %q em:\n%s", want, unit)
		}
	}
	if _, err := systemdUnit(serviceConfig{Bin: "invenire-agent", StateDir: "/var/lib/invenire"}); err == nil {
		t.Error("aceitou caminho relativo")
	}
}

func TestSystemdQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/local/bin/x": "/usr/local/bin/x",
		"/a b":             `"/a b"`,
		"/100%":            "/100%%",
		"/$HOME":           "/$$HOME",
		`/a"b`:             `"/a\"b"`,
		"/a;b":             `"/a;b"`,
	} {
		if got := systemdQuote(in); got != want {
			t.Errorf("systemdQuote(%q) = %q, quero %q", in, got, want)
		}
	}
}
