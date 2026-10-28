// Package testdb dá aos testes um PostgreSQL de verdade com as migrações
// aplicadas, num schema próprio que é apagado no fim. Sem a variável
// INVENIRE_TEST_DATABASE_URL os testes que o usam são pulados.
package testdb

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/athosbes/invenire/migrations"
)

// New devolve um pool ligado a um schema novo, já migrado.
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("INVENIRE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("INVENIRE_TEST_DATABASE_URL não definida")
	}
	ctx := context.Background()

	schema := "test_" + uuid.NewString()[:8]
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close(context.Background())
	})

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	db, err := sql.Open("pgx", cfg.ConnString()+"&search_path="+schema)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Sobe, desce e sobe de novo: o Down também precisa funcionar.
	for _, step := range []func(context.Context, *sql.DB) error{migrations.Up, migrations.Reset, migrations.Up} {
		if err := step(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	return pool
}
