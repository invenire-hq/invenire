// Package migrations embute as migrações goose, para o servidor aplicá-las
// na subida sem depender de arquivos no disco.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
