// SPDX-License-Identifier: AGPL-3.0-only

package snapshot

import (
	"bytes"
	"testing"
	"time"
)

func TestFingerprint(t *testing.T) {
	cases := []struct {
		host Host
		want string
	}{
		{Host{SMBIOSUUID: "4C4C4544-0042-3510-8052-B4C04F4D4E32"}, "smbios:4c4c4544-0042-3510-8052-b4c04f4d4e32"},
		{Host{SMBIOSUUID: "03000200-0400-0500-0006-000700080009", MachineID: "{ABC-123}"}, "machine:abc-123"},
		{Host{SMBIOSUUID: "não é uuid", MachineID: "0f1e2d"}, "machine:0f1e2d"},
		{Host{Hostname: "PMC-ARR-01"}, ""},
	}
	for _, c := range cases {
		if got := Fingerprint(c.host); got != c.want {
			t.Errorf("Fingerprint(%+v) = %q, quer %q", c.host, got, c.want)
		}
	}
}

func TestMarshal(t *testing.T) {
	s := New("test", time.Date(2026, 10, 21, 20, 0, 0, 0, time.UTC))
	s.Host = Host{Hostname: "h", MachineID: "abc"}
	s.Software = nil

	body, sum, err := Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if sum != Sum(body) || len(sum) != 64 {
		t.Fatalf("hash inconsistente: %s", sum)
	}
	if !bytes.Contains(body, []byte(`"software":[]`)) {
		t.Errorf("lista nula deveria virar []: %s", body)
	}

	s.Host = Host{Hostname: "h"}
	if _, _, err := Marshal(s); err == nil {
		t.Error("snapshot sem identidade deveria falhar")
	}
}
