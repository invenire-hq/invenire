// Comando invenire-server é a API central do Invenire.
//
//	invenire-server serve
//	invenire-server tenant create <nome>
//	invenire-server token create --tenant <nome> [--unit Saúde/UBS Centro]
//	invenire-server unit create --tenant <nome> [--parent Saúde] [--kind setor] <nome>
//
// A conexão com o PostgreSQL vem de INVENIRE_DATABASE_URL.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
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
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/athosbes/invenire/internal/server/api"
	"github.com/athosbes/invenire/internal/server/ingest"
	"github.com/athosbes/invenire/internal/server/normalize"
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
  token create --tenant <n> [--unit <caminho>]
                              cria um token de cadastro de agente, de uso único;
                              com --unit, a máquina já entra nessa unidade
  unit create --tenant <n> [--parent <caminho>] [--kind <tipo>] <nome>
                              cria uma unidade (secretaria, site, setor…)
  unit list --tenant <n>      mostra a árvore de unidades do tenant
  actor create --tenant <n> --name <nome> --scopes read,admin
                              cria um usuário ou pipeline e um token de API para ele
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
	case cmd == "unit create":
		err = runUnitCreate(ctx, os.Args[3:])
	case cmd == "unit list":
		err = runUnitList(ctx, os.Args[3:])
	case cmd == "actor create":
		err = runActorCreate(ctx, os.Args[3:])
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
	tlsCert := fs.String("tls-cert", "", "certificado público do servidor (PEM, com a cadeia); sem ele, a CA da instalação emite um")
	tlsKey := fs.String("tls-key", "", "chave do --tls-cert")
	minConf := fs.Float64("auto-map-confidence", normalize.DefaultMinConfidence,
		"confiança mínima para uma regra mapear software sem revisão (0 a 1)")
	fs.Parse(args)
	if *minConf <= 0 || *minConf > 1 {
		return errors.New("--auto-map-confidence precisa estar entre 0 (exclusive) e 1")
	}
	if (*tlsCert == "") != (*tlsKey == "") {
		return errors.New("--tls-cert e --tls-key vão juntos")
	}

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	if err := migrate(ctx); err != nil {
		return fmt.Errorf("migrações: %w", err)
	}
	pool, err := connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	master, err := pki.LoadOrCreateMasterKey(*dataDir)
	if err != nil {
		return err
	}
	// Duas confianças separadas: o agente confere o servidor pela CA da
	// instalação (com o pin) ou pelo certificado público; o servidor confere
	// o agente pela CA do tenant, no banco.
	caDir := filepath.Join(*dataDir, "ca")
	var serverCA, legacyCA *pki.CA
	var cert tls.Certificate
	mode := "internal"
	if *tlsCert != "" {
		mode = "public"
		if cert, err = tls.LoadX509KeyPair(*tlsCert, *tlsKey); err != nil {
			return fmt.Errorf("--tls-cert: %w", err)
		}
		if legacyCA, err = pki.Load(caDir); err != nil {
			return err
		}
	} else {
		if serverCA, err = pki.LoadOrCreate(caDir); err != nil {
			return err
		}
		if cert, err = serverCA.ServerCert(strings.Split(*hosts, ",")); err != nil {
			return err
		}
		legacyCA = serverCA
	}
	// Para o token create saber se mostra o pin.
	if err := os.WriteFile(filepath.Join(*dataDir, tlsModeFile), []byte(mode+"\n"), 0o644); err != nil {
		return err
	}
	if n, err := api.ImportLegacyCA(ctx, pool, legacyCA); err != nil {
		return fmt.Errorf("CA antiga da instalação: %w", err)
	} else if n > 0 {
		log.Info("agentes antigos ligados à CA da instalação", "agents", n)
	}
	srv, err := api.New(pool, api.Config{ServerCA: serverCA, MasterKey: master}, log)
	if err != nil {
		return err
	}
	rules, err := normalize.Default()
	if err != nil {
		return err
	}
	rules.MinConfidence = float32(*minConf)
	// A ingestão roda no mesmo processo. Ao subir, reaplica as regras (que
	// podem ter mudado com a versão); depois acorda a cada snapshot novo ou
	// mapeamento manual e, sem aviso, procura pendentes a cada 30s.
	worker := ingest.New(pool, log, rules)
	srv.Wake = worker.Wake
	srv.Suggest = rules.Evaluate
	ingestCtx, stopIngest := context.WithCancel(ctx)
	ingestDone := make(chan struct{})
	go func() {
		defer close(ingestDone)
		worker.Run(ingestCtx)
	}()
	// Antes de fechar o pool: a transação em curso termina ou é desfeita.
	defer func() { stopIngest(); <-ingestDone }()

	tlsCfg := srv.TLSConfig(cert)
	if *tlsCert != "" {
		// Certificado renovado em disco (certbot, por exemplo) entra sem reiniciar.
		r := &certReloader{certFile: *tlsCert, keyFile: *tlsKey, cert: &cert, log: log}
		r.mod, _ = modTime(*tlsCert)
		tlsCfg.Certificates, tlsCfg.GetCertificate = nil, r.get
	}
	hs := &http.Server{
		Addr:              *listen,
		Handler:           srv.Handler(),
		TLSConfig:         tlsCfg,
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

	if serverCA != nil {
		log.Info("invenire-server no ar", "version", version, "listen", *listen, "tls", mode, "ca_sha256", caPin(serverCA))
	} else {
		log.Info("invenire-server no ar", "version", version, "listen", *listen, "tls", mode)
	}
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
	unit := fs.String("unit", "", "caminho da unidade onde a máquina entra, ex.: Saúde/UBS Centro")
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
	var unitID *uuid.UUID
	if *unit != "" {
		u, err := api.ResolveUnitPath(ctx, q, t.ID, *unit)
		if err != nil {
			return fmt.Errorf("%w (veja: invenire-server unit list --tenant %q)", err, *tenant)
		}
		unitID = &u.ID
	}
	token, expires, err := api.NewEnrollmentToken(ctx, q, t.ID, unitID, *desc, *ttl)
	if err != nil {
		return err
	}
	fmt.Printf("token:   %s\nexpira:  %s\n", token, expires.Format(time.RFC3339))
	if unitID != nil {
		fmt.Printf("unidade: %s\n", strings.Trim(*unit, "/"))
	}
	if m, _ := os.ReadFile(filepath.Join(*dataDir, tlsModeFile)); strings.TrimSpace(string(m)) == "public" {
		fmt.Println("ca pin:  nenhum (o servidor usa certificado público: rode o enroll sem --ca-pin)")
		return nil
	}
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

func runUnitCreate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("unit create", flag.ExitOnError)
	tenant := fs.String("tenant", "", "nome do tenant (obrigatório)")
	parent := fs.String("parent", "", "caminho da unidade-mãe, ex.: Saúde/UBS Centro; vazio = raiz")
	kind := fs.String("kind", "", "tipo da camada, livre: secretaria, site, setor…")
	fs.Parse(args)
	if *tenant == "" || fs.NArg() != 1 {
		return errors.New("uso: invenire-server unit create --tenant <nome> [--parent <caminho>] [--kind <tipo>] <nome>")
	}
	if err := migrate(ctx); err != nil {
		return fmt.Errorf("migrações: %w", err)
	}
	pool, err := connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := store.New(tx)
	t, err := q.GetTenantByName(ctx, *tenant)
	if err != nil {
		return fmt.Errorf("tenant %q: %w", *tenant, err)
	}
	var parentID *uuid.UUID
	if *parent != "" {
		p, err := api.ResolveUnitPath(ctx, q, t.ID, *parent)
		if err != nil {
			return err
		}
		parentID = &p.ID
	}
	u, _, err := api.CreateUnit(ctx, q, t.ID, nil, parentID, *kind, fs.Arg(0))
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	path := fs.Arg(0)
	if *parent != "" {
		path = strings.Trim(*parent, "/") + "/" + path
	}
	fmt.Printf("unidade: %s\ncaminho: %s\n", u.ID, path)
	return nil
}

func runUnitList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("unit list", flag.ExitOnError)
	tenant := fs.String("tenant", "", "nome do tenant (obrigatório)")
	fs.Parse(args)
	if *tenant == "" {
		return errors.New("--tenant é obrigatório")
	}
	if err := migrate(ctx); err != nil {
		return fmt.Errorf("migrações: %w", err)
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
	rows, err := q.ListUnits(ctx, t.ID)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintln(os.Stderr, "nenhuma unidade; crie com: invenire-server unit create --tenant", *tenant, "<nome>")
		return nil
	}
	for _, u := range rows {
		kind := ""
		if u.Kind != "" {
			kind = " (" + u.Kind + ")"
		}
		fmt.Printf("%s%s%s  %d ativo(s)  %s\n", strings.Repeat("  ", int(u.Depth)), u.Name, kind, u.AssetCount, u.ID)
	}
	return nil
}

func runActorCreate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("actor create", flag.ExitOnError)
	tenant := fs.String("tenant", "", "nome do tenant (obrigatório)")
	name := fs.String("name", "", "nome do usuário ou pipeline (obrigatório)")
	kind := fs.String("kind", "human", "human, ai_agent ou ci")
	scopes := fs.String("scopes", "read", "escopos separados por vírgula: read, admin")
	ttl := fs.Duration("ttl", 0, "validade do token (0 = não expira)")
	desc := fs.String("description", "", "para que é o token")
	fs.Parse(args)
	if *tenant == "" || *name == "" {
		return errors.New("--tenant e --name são obrigatórios")
	}
	switch *kind {
	case "human", "ai_agent", "ci":
	default:
		return fmt.Errorf("--kind %q: use human, ai_agent ou ci", *kind)
	}
	var sc []string
	for _, s := range strings.Split(*scopes, ",") {
		if s = strings.TrimSpace(s); s != "" {
			sc = append(sc, s)
		}
	}
	if err := migrate(ctx); err != nil {
		return fmt.Errorf("migrações: %w", err)
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
	a, err := q.CreateActor(ctx, store.CreateActorParams{TenantID: t.ID, Kind: *kind, Name: *name, Scopes: sc})
	if err != nil {
		return err
	}
	token, expires, err := api.NewAPIToken(ctx, q, a.ID, *desc, *ttl)
	if err != nil {
		return err
	}
	fmt.Printf("ator:    %s (%s, %s)\ntoken:   %s\n", a.ID, a.Kind, strings.Join(a.Scopes, ","), token)
	if expires != nil {
		fmt.Printf("expira:  %s\n", expires.Format(time.RFC3339))
	}
	return nil
}

// tlsModeFile guarda, no diretório de dados, se o serve usa o certificado
// da CA da instalação (internal) ou um público (public).
const tlsModeFile = "tls-mode"

// certReloader relê o certificado público quando o arquivo muda, conferindo
// no máximo uma vez por minuto.
type certReloader struct {
	certFile, keyFile string
	log               *slog.Logger

	mu      sync.Mutex
	cert    *tls.Certificate
	mod     time.Time
	checked time.Time
}

func (r *certReloader) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.checked) >= time.Minute {
		r.checked = time.Now()
		if mod, err := modTime(r.certFile); err == nil && !mod.Equal(r.mod) {
			c, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
			if err != nil {
				// Certificado e chave trocados em momentos diferentes: tenta de novo depois.
				r.log.Warn("certificado público novo ainda ilegível; mantendo o anterior", "err", err)
			} else {
				r.cert, r.mod = &c, mod
				r.log.Info("certificado público recarregado", "file", r.certFile)
			}
		}
	}
	return r.cert, nil
}

func modTime(path string) (time.Time, error) {
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return st.ModTime(), nil
}
