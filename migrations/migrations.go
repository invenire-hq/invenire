// SPDX-License-Identifier: AGPL-3.0-only

// Package migrations embute as migrações goose, para o servidor aplicá-las
// na subida sem depender de arquivos no disco.
package migrations

import (
	"context"
	"database/sql"
	"embed"

	"github.com/pressly/goose/v3"
)

//go:embed *.sql
var FS embed.FS

func provider(db *sql.DB) (*goose.Provider, error) {
	return goose.NewProvider(goose.DialectPostgres, db, FS)
}

// Up aplica as migrações pendentes.
func Up(ctx context.Context, db *sql.DB) error {
	p, err := provider(db)
	if err != nil {
		return err
	}
	_, err = p.Up(ctx)
	return err
}

// Reset desfaz todas as migrações. Só para testes.
func Reset(ctx context.Context, db *sql.DB) error {
	p, err := provider(db)
	if err != nil {
		return err
	}
	_, err = p.DownTo(ctx, 0)
	return err
}
