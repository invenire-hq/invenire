// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"os"
	"testing"
)

// O schema versionado no repositório precisa bater com as structs.
func TestSchemaUpToDate(t *testing.T) {
	want, err := generate()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("../../schema/v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n")), want) {
		t.Fatal("schema/v1.json desatualizado: rode go generate ./pkg/snapshot")
	}
}
