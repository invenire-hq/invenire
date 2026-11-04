// Comando invenire-server é a API central do Invenire.
//
//	invenire-server serve
//	invenire-server tenant create <nome>
//	invenire-server token create --tenant <nome>
//
// A conexão com o PostgreSQL vem de INVENIRE_DATABASE_URL.
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/athosbes/invenire/internal/server/api"
	"github.com/athosbes/invenire/internal/server/ingest"
	"github.com/athosbes/invenire/internal/server/pki"
	"github.com/athosbes/invenire/internal/server/store"
	"github.com/athosbes/invenire/migrations"
)

// version é preenchida no build: -ldflags "-X main.version=0.1.0".
var version = "dev"

func usage() {
	fmt.Fprintf(os.Stderr, `uso: invenire-server <comando> [opções]

comandos:
  serve                       aplica as migrações e sobe a API (HTTPS com mTLS)
  tenant create <nome>        cria um tenant
  token create --tenant <n>   cria um token de cadastro de agente, de uso único
  version                     mostra a versão

A conexão com o PostgreSQL vem de INVENIRE_DATABASE_URL.
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var err error
	switch cmd := strings.Join(os.Args[1:min(3, len(os.Args))], " "); {
	case os.Args[1] == "serve":
		err = runServe(ctx, os.Args[2:])
	case cmd == "tenant create":
		err = runTenantCreate(ctx, os.Args[3:])
	case cmd == "token create":
		err = runTokenCreate(ctx, os.Args[3:])
	case os.Args[1] == "version" || os.Args[1] == "--version":
		fmt.Println(version)
	case os.Args[1] == "help" || os.Args[1] == "-h" || os.Args[1] == "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "invenire-server:", err)
		os.Exit(1)
	}
}

func databaseURL() (string, error) {
	url := os.Getenv("INVENIRE_DATABASE_URL")
	if url == "" {
		return "", errors.New("INVENIRE_DATABASE_URL não definida")
	}
	return url, nil
}

func connect(ctx context.Context) (*pgxpool.Pool, error) {
	url, err := databaseURL()
	if err != nil {
		return nil, err
	}
	return pgxpool.New(ctx, url)
}

func migrate(ctx context.Context) error {
	url, err := databaseURL()
	if err != nil {
		return err
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		return err
	}
	defer db.Close()
	return migrations.Up(ctx, db)
}

func runServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", ":8443", "endereço de escuta")
	dataDir := fs.String("data", "data", "diretório de dados (chave e certificado da CA)")
	hosts := fs.String("hosts", "localhost,127.0.0.1", "nomes e IPs do certificado TLS do servidor, separados por vírgula")
	fs.Parse(args)

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	if err := migrate(ctx); err != nil {
		return fmt.Errorf("migrações: %w", err)
	}
	pool, err := connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	ca, err := pki.LoadOrCreate(filepath.Join(*dataDir, "ca"))
	if err != nil {
		return err
	}
	cert, err := ca.ServerCert(strings.Split(*hosts, ","))
	if err != nil {
		return err
	}
	srv, err := api.New(pool, ca, log)
	if err != nil {
		return err
	}
	// A ingestão roda no mesmo processo: acorda a cada snapshot novo e, sem
	// aviso, procura pendentes a cada 30s (inclusive os de antes de subir).
	worker := ingest.New(pool, log, nil)
	srv.OnSnapshot = worker.Wake
	ingestCtx, stopIngest := context.WithCancel(ctx)
	ingestDone := make(chan struct{})
	go func() {
		defer close(ingestDone)
		worker.Run(ingestCtx)
	}()
	// Antes de fechar o pool: a transação em curso termina ou é desfeita.
	defer func() { stopIngest(); <-ingestDone }()

	hs := &http.Server{
		Addr:              *listen,
		Handler:           srv.Handler(),
		TLSConfig:         srv.TLSConfig(cert),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute, // snapshots grandes em links lentos
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		hs.Shutdown(shutdownCtx)
	}()

	log.Info("invenire-server no ar", "version", version, "listen", *listen, "ca_sha256", caPin(ca))
	if err := hs.ListenAndServeTLS("", ""); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// caPin é o SHA-256 do certificado da CA. Vai junto com o token de cadastro
// para o agente conferir o servidor antes de confiar nele.
func caPin(ca *pki.CA) string {
	h := sha256.Sum256(ca.Cert.Raw)
	return hex.EncodeToString(h[:])
}

func runTenantCreate(ctx context.Context, args []string) error {
	if len(args) != 1 || args[0] == "" {
		return errors.New("uso: invenire-server tenant create <nome>")
	}
	if err := migrate(ctx); err != nil {
		return fmt.Errorf("migrações: %w", err)
	}
	pool, err := connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	t, err := store.New(pool).CreateTenant(ctx, args[0])
	if err != nil {
		return err
	}
	fmt.Println(t.ID)
	return nil
}

func runTokenCreate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("token create", flag.ExitOnError)
	tenant := fs.String("tenant", "", "nome do tenant (obrigatório)")
	ttl := fs.Duration("ttl", 24*time.Hour, "validade do token")
	desc := fs.String("description", "", "para que ou para quem é o token")
	dataDir := fs.String("data", "data", "diretório de dados do servidor, para mostrar o pin da CA")
	fs.Parse(args)
	if *tenant == "" {
		return errors.New("--tenant é obrigatório")
	}

	pool, err := connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	q := store.New(pool)
	t, err := q.GetTenantByName(ctx, *tenant)
	if err != nil {
		return fmt.Errorf("tenant %q: %w", *tenant, err)
	}
	token, expires, err := api.NewEnrollmentToken(ctx, q, t.ID, *desc, *ttl)
	if err != nil {
		return err
	}
	fmt.Printf("token:   %s\nexpira:  %s\n", token, expires.Format(time.RFC3339))
	// Só lê o certificado: rodar no diretório errado não pode criar outra CA.
	if b, err := os.ReadFile(filepath.Join(*dataDir, "ca", "ca.crt")); err == nil {
		if blk, _ := pem.Decode(b); blk != nil {
			h := sha256.Sum256(blk.Bytes)
			fmt.Printf("ca pin:  sha256:%s\n", hex.EncodeToString(h[:]))
		}
	} else {
		fmt.Fprintf(os.Stderr, "aviso: sem pin da CA (%v); o agente terá de confiar no servidor no primeiro contato\n", err)
	}
	return nil
}
