// Comando invenire-server é a API central do Invenire.
//
//	invenire-server serve
//	invenire-server tenant create [--admin-email <e-mail> --admin-name <nome>] <nome>
//	invenire-server user set-password --email <e-mail>
//	invenire-server tenant export --tenant <nome> --out <arquivo>
//	invenire-server tenant verify <arquivo>
//	invenire-server token create --tenant <nome> [--unit Sede/Financeiro]
//	invenire-server unit create --tenant <nome> [--parent Sede] [--kind setor] <nome>
//
// A conexão com o PostgreSQL vem de INVENIRE_DATABASE_URL.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/athosbes/invenire/internal/server/api"
	"github.com/athosbes/invenire/internal/server/download"
	"github.com/athosbes/invenire/internal/server/export"
	"github.com/athosbes/invenire/internal/server/ingest"
	"github.com/athosbes/invenire/internal/server/normalize"
	"github.com/athosbes/invenire/internal/server/pki"
	"github.com/athosbes/invenire/internal/server/store"
	"github.com/athosbes/invenire/internal/server/web"
	"github.com/athosbes/invenire/migrations"
)

// version é preenchida no build: -ldflags "-X main.version=0.1.0".
var version = "dev"

func usage() {
	fmt.Fprintf(os.Stderr, `uso: invenire-server <comando> [opções]

comandos:
  serve                       aplica as migrações e sobe a API e o painel web
                              (HTTPS); numa instalação vazia, mostra no log o
                              código da primeira configuração (/setup)
  serve --behind-proxy --trusted-proxies <CIDRs>
                              sobe a API em HTTP puro, atrás de um proxy
                              reverso que termina o TLS (Nginx, Caddy…)
  serve --agent-dir <pasta>   pasta com os executáveis do agente e o SHA256SUMS
                              (padrão: INVENIRE_AGENT_DIR, ou a da imagem
                              Docker); o painel monta deles o pacote do
                              instalador, e /download/ os entrega
  tenant create [--admin-email <e-mail> --admin-name <nome>] <nome>
                              cria um tenant; com --admin-email, também o
                              primeiro administrador do painel
  user set-password --email <e-mail>
                              troca a senha de login do painel (recuperação de
                              acesso) e encerra as sessões abertas
  tenant export --tenant <n> --out <arquivo> [--with-ca-key]
                              exporta o tenant inteiro num pacote .tar.gz
  tenant verify <arquivo>     confere um pacote exportado, sem banco
  token create --tenant <n> [--unit <caminho>] [--uses N]
                              cria um token de cadastro de agente (um uso, ou N
                              máquinas com --uses); com --unit, a máquina já
                              entra nessa unidade
  token list --tenant <n> [--all]
                              mostra os tokens de cadastro e quantos usos restam
  token revoke --tenant <n> <id>
                              revoga um token de cadastro
  status --tenant <n> [--stale 12h]
                              situação do parque: agentes em dia, atrasados,
                              snapshots com erro, corrente quebrada
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
	case cmd == "tenant export":
		err = runTenantExport(ctx, os.Args[3:])
	case cmd == "tenant verify":
		err = runTenantVerify(os.Args[3:])
	case cmd == "token create":
		err = runTokenCreate(ctx, os.Args[3:])
	case os.Args[1] == "health":
		err = runHealth(ctx, os.Args[2:])
	case os.Args[1] == "status":
		err = runStatus(ctx, os.Args[2:])
	case cmd == "token list":
		err = runTokenList(ctx, os.Args[3:])
	case cmd == "token revoke":
		err = runTokenRevoke(ctx, os.Args[3:])
	case cmd == "unit create":
		err = runUnitCreate(ctx, os.Args[3:])
	case cmd == "unit list":
		err = runUnitList(ctx, os.Args[3:])
	case cmd == "actor create":
		err = runActorCreate(ctx, os.Args[3:])
	case cmd == "user set-password":
		err = runUserSetPassword(ctx, os.Args[3:])
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
	behindProxy := fs.Bool("behind-proxy", false, "HTTP puro atrás de um proxy reverso que termina o TLS; o agente se autentica por assinatura")
	trustedProxies := fs.String("trusted-proxies", "", "com --behind-proxy: IPs ou CIDRs de onde o proxy conecta, separados por vírgula; só deles se leem os X-Forwarded-*")
	minConf := fs.Float64("auto-map-confidence", normalize.DefaultMinConfidence,
		"confiança mínima para uma regra mapear software sem revisão (0 a 1)")
	stale := fs.Duration("stale", 12*time.Hour, "no painel, sem notícia do agente por mais que isto = atrasado")
	agentDir := fs.String("agent-dir", envOr(agentDirEnv, download.DefaultDir),
		"pasta com os executáveis do agente e o SHA256SUMS (a de scripts/build.sh serve), distribuídos em /download/ e no pacote do instalador")
	fs.Parse(args)
	if *minConf <= 0 || *minConf > 1 {
		return errors.New("--auto-map-confidence precisa estar entre 0 (exclusive) e 1")
	}
	if (*tlsCert == "") != (*tlsKey == "") {
		return errors.New("--tls-cert e --tls-key vão juntos")
	}
	var trusted []netip.Prefix
	if *behindProxy {
		if *tlsCert != "" {
			return errors.New("--behind-proxy e --tls-cert não vão juntos: o certificado público fica no proxy")
		}
		var err error
		if trusted, err = api.ParseTrustedProxies(*trustedProxies); err != nil {
			return err
		}
	} else if *trustedProxies != "" {
		return errors.New("--trusted-proxies só vale com --behind-proxy")
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
	switch {
	case *behindProxy:
		// O certificado (público) é o do proxy: a CA da instalação não emite
		// nada. A antiga, se houver, continua conferindo crachás antigos.
		mode = "proxy"
		if legacyCA, err = pki.Load(caDir); err != nil {
			return err
		}
	case *tlsCert != "":
		mode = "public"
		if cert, err = tls.LoadX509KeyPair(*tlsCert, *tlsKey); err != nil {
			return fmt.Errorf("--tls-cert: %w", err)
		}
		if legacyCA, err = pki.Load(caDir); err != nil {
			return err
		}
	default:
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
	srv, err := api.New(pool, api.Config{ServerCA: serverCA, MasterKey: master, BehindProxy: *behindProxy}, log)
	if err != nil {
		return err
	}
	srv.Version = version
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

	// A primeira configuração pelo painel exige um código, mostrado aqui uma
	// vez (ou dado em INVENIRE_SETUP_CODE): quem achar primeiro uma
	// instalação nova exposta na internet não toma posse dela.
	var setupCode string
	if n, err := store.New(pool).CountTenants(ctx); err != nil {
		return err
	} else if n == 0 {
		setupCode = strings.TrimSpace(os.Getenv(setupCodeEnv))
		if setupCode == "" {
			setupCode = web.NewSetupCode()
		}
		log.Warn("primeira configuração: abra o painel no navegador (/setup) e use este código", "codigo", setupCode)
	}
	// Os executáveis do agente que o servidor distribui. Sem eles, o servidor
	// funciona; o painel explica o que falta.
	agents := download.Open(*agentDir)
	if p := agents.Problem(); p != "" {
		log.Warn("sem executáveis do agente para distribuir: o painel não oferece o pacote do instalador", "agent_dir", *agentDir, "motivo", p)
	} else if m := agents.Missing(); len(m) > 0 {
		log.Warn("executáveis do agente faltando", "agent_dir", *agentDir, "faltam", m)
	}
	panel, err := web.New(web.Config{API: srv, Log: log, SetupCode: setupCode, Stale: *stale, Agents: agents})
	if err != nil {
		return err
	}
	// Mesma porta: a API em /v1/ e /healthz, o painel no resto.
	root := http.NewServeMux()
	apiHandler := srv.Handler()
	root.Handle("/v1/", apiHandler)
	root.Handle("/healthz", apiHandler)
	root.Handle("/", panel.Handler())

	var tlsCfg *tls.Config
	handler := http.Handler(root)
	if *behindProxy {
		handler = api.ProxyHeaders(trusted, handler)
	} else {
		tlsCfg = srv.TLSConfig(cert)
		if *tlsCert != "" {
			// Certificado renovado em disco (certbot, por exemplo) entra sem reiniciar.
			r := &certReloader{certFile: *tlsCert, keyFile: *tlsKey, cert: &cert, log: log}
			r.mod, _ = modTime(*tlsCert)
			tlsCfg.Certificates, tlsCfg.GetCertificate = nil, r.get
		}
	}
	hs := &http.Server{
		Addr:              *listen,
		Handler:           handler,
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

	switch {
	case *behindProxy:
		log.Info("invenire-server no ar, em HTTP atrás de proxy", "version", version, "listen", *listen, "tls", mode, "trusted_proxies", *trustedProxies)
		err = hs.ListenAndServe()
	case serverCA != nil:
		log.Info("invenire-server no ar", "version", version, "listen", *listen, "tls", mode, "ca_sha256", caPin(serverCA))
		err = hs.ListenAndServeTLS("", "")
	default:
		log.Info("invenire-server no ar", "version", version, "listen", *listen, "tls", mode)
		err = hs.ListenAndServeTLS("", "")
	}
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// agentDirEnv é o padrão do --agent-dir.
const agentDirEnv = "INVENIRE_AGENT_DIR"

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// setupCodeEnv dá o código da primeira configuração, em vez de um gerado.
const setupCodeEnv = "INVENIRE_SETUP_CODE"

// caPin é o SHA-256 do certificado da CA. Vai junto com o token de cadastro
// para o agente conferir o servidor antes de confiar nele.
func caPin(ca *pki.CA) string {
	h := sha256.Sum256(ca.Cert.Raw)
	return hex.EncodeToString(h[:])
}

// exportKeyEnv traz a chave de exportação para o tenant verify sem que ela
// fique no histórico do terminal.
const exportKeyEnv = "INVENIRE_EXPORT_KEY"

func runTenantExport(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("tenant export", flag.ExitOnError)
	tenant := fs.String("tenant", "", "nome do tenant (obrigatório)")
	out := fs.String("out", "", "arquivo do pacote, ex.: minha-organizacao.invenire.tar.gz (obrigatório)")
	withKey := fs.Bool("with-ca-key", false, "inclui a chave da CA do tenant, cifrada com uma chave de exportação mostrada uma vez")
	dataDir := fs.String("data", "data", "diretório de dados do servidor (chave-mestra, só com --with-ca-key)")
	fs.Parse(args)
	if *tenant == "" || *out == "" {
		return errors.New("uso: invenire-server tenant export --tenant <nome> --out <arquivo> [--with-ca-key]")
	}
	if _, err := os.Stat(*out); err == nil {
		return fmt.Errorf("%s já existe; escolha outro nome", *out)
	}
	opts := export.Options{ServerVersion: version, WithCAKey: *withKey}
	if *withKey {
		var err error
		if opts.Master, err = pki.LoadMasterKey(*dataDir); err != nil {
			return err
		}
	}
	pool, err := connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	t, err := store.New(pool).GetTenantByName(ctx, *tenant)
	if err != nil {
		return fmt.Errorf("tenant %q: %w", *tenant, err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(*out), ".export-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	res, err := export.Write(ctx, pool, t.ID, io.MultiWriter(tmp, h), opts)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), *out); err != nil {
		return err
	}
	m := res.Manifest
	if err := export.Record(ctx, store.New(pool), nil, m, *withKey, "cli"); err != nil {
		return fmt.Errorf("pacote gravado, mas o registro no change_log falhou: %w", err)
	}
	fmt.Printf("pacote:     %s\nsha256:     %s\ntenant:     %s (%s)\nas_of:      %d\n", *out, hex.EncodeToString(h.Sum(nil)), m.Tenant.Name, m.Tenant.ID, m.AsOf)
	fmt.Printf("registros:  %d ativos, %d agentes, %d snapshots, %d unidades, %d mapeamentos, %d eventos\n",
		m.Files[export.FileAssets].Records, m.Files[export.FileAgents].Records, m.SnapshotBodies+m.SnapshotPayloads,
		m.Files[export.FileUnits].Records, m.Files[export.FileMappings].Records, m.Files[export.FileChanges].Records)
	if res.ExportKey != nil && res.CAKeys == 0 {
		fmt.Fprintln(os.Stderr, "aviso: o tenant não tem CA com chave (só a CA antiga da instalação, de conferência): o pacote vai sem chave de CA")
	} else if res.ExportKey != nil {
		fmt.Printf("chave:      %s (abre %d chave(s) de CA)\n", base64.StdEncoding.EncodeToString(res.ExportKey), res.CAKeys)
		fmt.Fprintln(os.Stderr, "a chave acima abre a chave da CA do tenant e só aparece agora: guarde-a separada do pacote")
	}
	return nil
}

func runTenantVerify(args []string) error {
	fs := flag.NewFlagSet("tenant verify", flag.ExitOnError)
	keyFlag := fs.String("export-key", "", "chave de exportação, para conferir a chave da CA (ou "+exportKeyEnv+")")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("uso: invenire-server tenant verify [--export-key <chave>] <arquivo>")
	}
	var key []byte
	if *keyFlag == "" {
		*keyFlag = os.Getenv(exportKeyEnv)
	}
	if *keyFlag != "" {
		var err error
		if key, err = base64.StdEncoding.DecodeString(strings.TrimSpace(*keyFlag)); err != nil || len(key) != 32 {
			return errors.New("chave de exportação inválida (32 bytes em base64)")
		}
	}
	f, err := os.Open(fs.Arg(0))
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	rep, err := export.Verify(io.TeeReader(f, h), key)
	if err != nil {
		return err
	}
	io.Copy(h, f) // o que vier depois do fim do gzip também entra no sha256 do arquivo
	m := rep.Manifest
	fmt.Printf("sha256:     %s\ntenant:     %s (%s)\nexportado:  %s, as_of %d, servidor %s\n",
		hex.EncodeToString(h.Sum(nil)), m.Tenant.Name, m.Tenant.ID, m.ExportedAt.Format(time.RFC3339), m.AsOf, m.ServerVersion)
	fmt.Printf("registros:  %d ativos, %d agentes, %d snapshots, %d unidades, %d mapeamentos, %d eventos\n",
		rep.Records[export.FileAssets], rep.Records[export.FileAgents], rep.Snapshots,
		rep.Records[export.FileUnits], rep.Records[export.FileMappings], rep.Records[export.FileChanges])
	if rep.PayloadOnly > 0 {
		fmt.Printf("sem bytes:  %d snapshot(s) recebido(s) antes de o servidor guardar os bytes originais: vão só como payload, sem sha256 reconferível\n", rep.PayloadOnly)
	}
	if len(rep.ChainBreaks) > 0 {
		fmt.Printf("corrente:   %d elo(s) com prev_sha256 divergente (registrados como alerta na origem):\n", len(rep.ChainBreaks))
		for _, b := range rep.ChainBreaks[:min(len(rep.ChainBreaks), 20)] {
			fmt.Printf("            ativo %s, snapshot %s\n", b.AssetID, b.SnapshotID)
		}
	} else {
		fmt.Println("corrente:   íntegra em todos os ativos")
	}
	switch {
	case key != nil:
		fmt.Printf("chave CA:   %d chave(s) aberta(s) e conferida(s)\n", rep.CAKeys)
	case m.CAKey != nil:
		fmt.Println("chave CA:   incluída, não conferida (passe --export-key)")
	default:
		fmt.Println("chave CA:   não incluída")
	}
	if !rep.OK() {
		for _, p := range rep.Problems {
			fmt.Fprintln(os.Stderr, "problema:", p)
		}
		return fmt.Errorf("pacote com %d problema(s) de integridade", len(rep.Problems))
	}
	fmt.Println("resultado:  íntegro")
	return nil
}

// passwordEnv traz a senha para tenant create --admin-email e user
// set-password sem que ela fique no histórico do terminal; sem ela, o
// comando gera uma e a mostra.
const passwordEnv = "INVENIRE_PASSWORD"

// newPassword é a senha de INVENIRE_PASSWORD ou uma gerada agora.
func newPassword() (pw string, generated bool) {
	if pw = os.Getenv(passwordEnv); pw != "" {
		return pw, false
	}
	b := make([]byte, 15)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b), true
}

func runTenantCreate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("tenant create", flag.ExitOnError)
	email := fs.String("admin-email", "", "e-mail do primeiro administrador do painel (a senha vem de "+passwordEnv+" ou é gerada)")
	adminName := fs.String("admin-name", "", "nome do primeiro administrador (com --admin-email)")
	fs.Parse(args)
	if fs.NArg() != 1 || fs.Arg(0) == "" {
		return errors.New("uso: invenire-server tenant create [--admin-email <e-mail> --admin-name <nome>] <nome>")
	}
	if (*email == "") != (*adminName == "") {
		return errors.New("--admin-email e --admin-name vão juntos")
	}
	if err := migrate(ctx); err != nil {
		return fmt.Errorf("migrações: %w", err)
	}
	pool, err := connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	if *email == "" {
		t, err := store.New(pool).CreateTenant(ctx, fs.Arg(0))
		if err != nil {
			return err
		}
		fmt.Println(t.ID)
		return nil
	}
	// A mesma função do /setup do painel.
	pw, generated := newPassword()
	org, err := api.CreateOrganization(ctx, pool, api.OrgSpec{
		Name: fs.Arg(0), AdminName: *adminName, AdminEmail: *email, AdminPassword: pw, LinkExistingLogin: true,
	})
	if err != nil {
		return err
	}
	fmt.Printf("tenant:  %s\nadmin:   %s (%s)\n", org.Tenant.ID, org.Admin.ID, strings.ToLower(strings.TrimSpace(*email)))
	switch {
	case org.LinkedLogin:
		fmt.Println("senha:   a que o e-mail já tinha nesta instalação")
	case generated:
		fmt.Printf("senha:   %s\n", pw)
		fmt.Fprintln(os.Stderr, "a senha acima só aparece agora; a pessoa pode trocá-la no painel, em Minha conta")
	}
	return nil
}

func runUserSetPassword(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("user set-password", flag.ExitOnError)
	email := fs.String("email", "", "e-mail de login no painel (obrigatório)")
	fs.Parse(args)
	if *email == "" || fs.NArg() != 0 {
		return errors.New("uso: invenire-server user set-password --email <e-mail>   (a senha vem de " + passwordEnv + " ou é gerada)")
	}
	pool, err := connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	pw, generated := newPassword()
	if err := api.SetPassword(ctx, pool, *email, pw, nil); err != nil {
		return err
	}
	if generated {
		fmt.Printf("senha:   %s\n", pw)
		fmt.Fprintln(os.Stderr, "a senha acima só aparece agora; a pessoa pode trocá-la no painel, em Minha conta")
	}
	fmt.Fprintln(os.Stderr, "senha trocada; as sessões abertas no painel foram encerradas")
	return nil
}

func runTokenCreate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("token create", flag.ExitOnError)
	tenant := fs.String("tenant", "", "nome do tenant (obrigatório)")
	ttl := fs.Duration("ttl", 24*time.Hour, "validade do token")
	desc := fs.String("description", "", "para que ou para quem é o token")
	unit := fs.String("unit", "", "caminho da unidade onde a máquina entra, ex.: Sede/Financeiro")
	uses := fs.Int("uses", 1, "quantas máquinas o token cadastra (um lote, como o piloto); revogue com token revoke")
	dataDir := fs.String("data", "data", "diretório de dados do servidor, para mostrar o pin da CA")
	fs.Parse(args)
	if *tenant == "" {
		return errors.New("--tenant é obrigatório")
	}
	if *uses < 1 || *uses > 10000 {
		return errors.New("--uses de 1 a 10000")
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
	tok, err := api.IssueEnrollmentToken(ctx, q, nil, api.TokenSpec{Tenant: t.ID, Unit: unitID, Description: *desc, TTL: *ttl, Uses: *uses})
	if err != nil {
		return err
	}
	fmt.Printf("token:   %s\nid:      %s\nexpira:  %s\nusos:    %d\n", tok.Token, tok.ID, tok.ExpiresAt.Format(time.RFC3339), *uses)
	if *uses > 1 {
		fmt.Fprintln(os.Stderr, "aviso: este token cadastra várias máquinas; se vazar, corte com: invenire-server token revoke --tenant", strconvQuote(*tenant), tok.ID)
	}
	if unitID != nil {
		fmt.Printf("unidade: %s\n", strings.Trim(*unit, "/"))
	}
	if m, _ := os.ReadFile(filepath.Join(*dataDir, tlsModeFile)); strings.TrimSpace(string(m)) == "public" || strings.TrimSpace(string(m)) == "proxy" {
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

func strconvQuote(s string) string { return fmt.Sprintf("%q", s) }

// errProblems faz o status --check sair com 1 quando há ativos que precisam
// de atenção, para um cron ou monitor avisar.
var errProblems = errors.New("há ativos que precisam de atenção")

func runStatus(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	tenant := fs.String("tenant", "", "nome do tenant (obrigatório)")
	stale := fs.Duration("stale", 12*time.Hour, "sem notícia do agente por mais que isto = atrasado (coleta padrão a cada 4h)")
	check := fs.Bool("check", false, "sair com código 1 se algum ativo precisar de atenção (para cron ou monitor)")
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
	now := time.Now()
	p, err := api.TenantParkStatus(ctx, q, t.ID, *stale, now)
	if err != nil {
		return err
	}
	ago := func(at *time.Time) string {
		if at == nil {
			return "nunca"
		}
		d := now.Sub(*at)
		switch {
		case d < time.Minute:
			return "agora"
		case d < time.Hour:
			return fmt.Sprintf("há %dmin", int(d.Minutes()))
		case d < 48*time.Hour:
			return fmt.Sprintf("há %dh", int(d.Hours()))
		}
		return fmt.Sprintf("há %dd", int(d.Hours()/24))
	}
	fmt.Printf("%-11s %-24s %-8s %-9s %-10s %-10s %6s  %s\n", "ESTADO", "HOSTNAME", "SO", "AGENTE", "VISTO", "COLETA", "PROGS", "UNIDADE")
	for _, a := range p.Assets {
		extra := ""
		if a.Pending > 0 {
			extra += fmt.Sprintf("  %d na fila", a.Pending)
		}
		if a.Failed > 0 {
			extra += fmt.Sprintf("  %d com erro", a.Failed)
		}
		if a.ChainBreaks > 0 {
			extra += fmt.Sprintf("  corrente quebrada %dx", a.ChainBreaks)
		}
		host := a.Hostname
		if len(host) > 24 {
			host = host[:23] + "…"
		}
		fmt.Printf("%-11s %-24s %-8s %-9s %-10s %-10s %6d  %s%s\n", a.State, orDash(host), orDash(a.OSFamily), orDash(a.AgentVersion),
			ago(a.AgentLastSeen), ago(a.LastCollected), a.Installed, orDash(a.Unit), extra)
	}
	fmt.Printf("\n%d ativo(s):", len(p.Assets))
	for _, s := range []string{api.StateOK, api.StatePending, api.StateNoData, api.StateStale, api.StateFailed, api.StateNoAgent} {
		if n := p.ByState[s]; n > 0 {
			fmt.Printf(" %d %s", n, s)
		}
	}
	fmt.Printf("\nsistemas:   %s\nversões:    %s\n", counts(p.ByOS), counts(p.ByVersion))
	fmt.Printf("software:   %d nome(s) sem produto (GET /v1/software/unmapped)\n", p.Unmapped)
	fmt.Printf("cadastros:  %d restante(s) nos tokens válidos (token list)\n", p.ValidTokens)
	for _, e := range p.Errors {
		fmt.Printf("erro:       %s, snapshot %s de %s: %s\n", orDash(deref(e.Hostname)), e.ID, e.CollectedAt.Format("2006-01-02 15:04"), deref(e.IngestError))
	}
	if *check && p.Problems() > 0 {
		return fmt.Errorf("%w: %d", errProblems, p.Problems())
	}
	return nil
}

func counts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func runTokenList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("token list", flag.ExitOnError)
	tenant := fs.String("tenant", "", "nome do tenant (obrigatório)")
	all := fs.Bool("all", false, "incluir gastos, vencidos e revogados")
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
	rows, err := q.ListEnrollmentTokens(ctx, store.ListEnrollmentTokensParams{TenantID: t.ID, IncludeAll: *all})
	if err != nil {
		return err
	}
	units, err := q.ListUnits(ctx, t.ID)
	if err != nil {
		return err
	}
	path := map[uuid.UUID]string{}
	for _, u := range units {
		path[u.ID] = u.Path
	}
	if len(rows) == 0 {
		fmt.Fprintln(os.Stderr, "nenhum token válido (--all mostra também os gastos, vencidos e revogados)")
		return nil
	}
	now := time.Now()
	for _, r := range rows {
		state := "válido"
		switch {
		case r.RevokedAt != nil:
			state = "revogado"
		case r.Uses >= r.MaxUses:
			state = "gasto"
		case !r.ExpiresAt.After(now):
			state = "vencido"
		}
		unit := "-"
		if r.UnitID != nil {
			unit = path[*r.UnitID]
		}
		fmt.Printf("%s  %-8s  %d/%d usos  expira %s  unidade %s  %s\n",
			r.ID, state, r.Uses, r.MaxUses, r.ExpiresAt.Format("2006-01-02 15:04"), unit, r.Description)
	}
	return nil
}

func runTokenRevoke(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("token revoke", flag.ExitOnError)
	tenant := fs.String("tenant", "", "nome do tenant (obrigatório)")
	fs.Parse(args)
	if *tenant == "" || fs.NArg() != 1 {
		return errors.New("uso: invenire-server token revoke --tenant <nome> <id do token>")
	}
	id, err := uuid.Parse(fs.Arg(0))
	if err != nil {
		return errors.New("id do token inválido (veja: invenire-server token list)")
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
	r, err := api.RevokeEnrollmentToken(ctx, q, t.ID, nil, id)
	if err != nil {
		return err
	}
	fmt.Printf("token %s revogado depois de %d de %d uso(s); as máquinas já cadastradas continuam\n", r.ID, r.Uses, r.MaxUses)
	return nil
}

func runUnitCreate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("unit create", flag.ExitOnError)
	tenant := fs.String("tenant", "", "nome do tenant (obrigatório)")
	parent := fs.String("parent", "", "caminho da unidade-mãe, ex.: Sede/Financeiro; vazio = raiz")
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
// da CA da instalação (internal), um público (public) ou se está atrás de um
// proxy que termina o TLS (proxy).
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

// runHealth confere se o serve local responde: é o healthcheck do contêiner,
// que não tem curl. Só pergunta ao próprio processo, por isso não confere o
// certificado.
func runHealth(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("health", flag.ExitOnError)
	url := fs.String("url", "https://127.0.0.1:8443/healthz", "endereço do /healthz")
	fs.Parse(args)
	c := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, // #nosec: só localhost
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, *url, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("%s respondeu %d", *url, resp.StatusCode)
	}
	return nil
}
