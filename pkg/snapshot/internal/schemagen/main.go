// SPDX-License-Identifier: AGPL-3.0-only

// Comando schemagen gera o JSON Schema do snapshot a partir das structs Go.
// Rode com `go generate ./pkg/snapshot`.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"os"

	"github.com/invopop/jsonschema"

	"github.com/invenire-hq/invenire/pkg/snapshot"
)

func generate() ([]byte, error) {
	r := &jsonschema.Reflector{}
	s := r.Reflect(&snapshot.Snapshot{})
	s.ID = jsonschema.ID("urn:invenire:schema:snapshot:v" + snapshot.SchemaVersion)
	s.Title = "Invenire snapshot v" + snapshot.SchemaVersion
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func main() {
	out := flag.String("out", "schema/v1.json", "arquivo de saída")
	flag.Parse()
	b, err := generate()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		log.Fatal(err)
	}
}
