// Comando invenire-agent inventaria a máquina local.
//
//	invenire-agent enroll --server https://invenire:8443 --token inv_… --ca-pin sha256:…
//	invenire-agent run
//	invenire-agent service install   (serviço do Windows ou unit do systemd)
//	invenire-agent collect --out snapshot.json   (modo offline, sem servidor)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/athosbes/invenire/internal/agent/collect"
	"github.com/athosbes/invenire/internal/agent/runner"
	"github.com/athosbes/invenire/internal/agent/state"
	"github.com/athosbes/invenire/internal/agent/transport"
	"github.com/athosbes/invenire/pkg/snapshot"
)

// version é preenchida no build: -ldflags "-X main.version=0.1.0".
var version = "dev"

func usage() {
	fmt.Fprintf(os.Stderr, `uso: invenire-agent <comando> [opções]

comandos:
  enroll    cadastra o agente no servidor com um token de uso único
  run       coleta periodicamente e envia ao servidor, com fila local
  set-server  aponta para outro servidor sem recadastrar (o tenant mudou de servidor)
  service   install | uninstall: roda o run como serviço do sistema
  collect   coleta o inventário e grava o snapshot JSON (modo offline)
  version   mostra a versão

O estado (chave, certificado, fila) fica em %s,
acessível só a administradores; rode enroll e run como administrador/root.
`, state.DefaultDir())
}

// exitRevoked é o código de saída quando o servidor recusa o certificado:
// o gerenciador de serviços não deve reiniciar o agente, porque só um novo
// cadastro resolve.
const exitRevoked = 3

func main() {
	// Iniciado pelo gerenciador de serviços do Windows: não há console.
	if isService() {
		os.Exit(serviceMain(os.Args[1:]))
	}
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "collect":
		err = runCollect(ctx, os.Args[2:])
	case "enroll":
		err = runEnroll(ctx, os.Args[2:])
	case "set-server":
		err = runSetServer(ctx, os.Args[2:])
	case "run":
		err = runRun(ctx, os.Args[2:], slog.New(slog.NewTextHandler(os.Stderr, nil)))
	case "service":
		err = runService(ctx, os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println(version)
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "invenire-agent:", err)
		if errors.Is(err, runner.ErrRevoked) {
			os.Exit(exitRevoked)
		}
		os.Exit(1)
	}
}

func runCollect(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("collect", flag.ExitOnError)
	out := fs.String("out", "-", "arquivo de saída (- para a saída padrão)")
	fs.Parse(args)

	s := collect.Collect(ctx, version)
	for _, e := range s.CollectorErrors {
		fmt.Fprintf(os.Stderr, "aviso: coletor %s: %s\n", e.Collector, e.Error)
	}

	body, sum, err := snapshot.Marshal(s)
	if err != nil {
		return err
	}
	if *out == "-" {
		_, err = os.Stdout.Write(append(body, '\n'))
	} else {
		err = os.WriteFile(*out, body, 0o600)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "snapshot: %d softwares, sha256 %s\n", len(s.Software), sum)
	return nil
}

func runEnroll(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	server := fs.String("server", "", "URL do servidor, https://host:porta (obrigatório)")
	token := fs.String("token", "", "token de cadastro (ou INVENIRE_ENROLL_TOKEN)")
	pin := fs.String("ca-pin", "", "pin da CA mostrado por invenire-server token create, sha256:…; vazio = servidor com certificado público")
	dir := fs.String("state-dir", state.DefaultDir(), "diretório de estado do agente")
	force := fs.Bool("force", false, "cadastrar de novo mesmo já havendo cadastro")
	fs.Parse(args)

	if *token == "" {
		*token = os.Getenv("INVENIRE_ENROLL_TOKEN")
	}
	if *server == "" || *token == "" {
		return errors.New("--server e --token são obrigatórios")
	}
	caPin, err := parseOptionalPin(*pin)
	if err != nil {
		return err
	}
	d, err := state.Open(*dir)
	if err != nil {
		return err
	}
	cfg, err := runner.Enroll(ctx, d, runner.EnrollOptions{
		ServerURL:    *server,
		Token:        *token,
		CAPin:        caPin,
		AgentVersion: version,
		Force:        *force,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "cadastrado: agente %s, ativo %s\n", cfg.AgentID, cfg.AssetID)
	return nil
}

func runRun(ctx context.Context, args []string, log *slog.Logger) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	once := fs.Bool("once", false, "coleta, tenta enviar a fila uma vez e sai")
	interval := fs.Duration("interval", 4*time.Hour, "intervalo entre coletas")
	dir := fs.String("state-dir", state.DefaultDir(), "diretório de estado do agente")
	maxQueue := fs.Int("max-queue", 100, "snapshots guardados na fila sem conseguir enviar")
	fs.Parse(args)

	if *interval < time.Minute {
		return errors.New("--interval precisa ser de pelo menos 1m")
	}
	d, err := state.Open(*dir)
	if err != nil {
		return err
	}
	a := &runner.Agent{
		Dir:      d,
		Version:  version,
		Log:      log,
		MaxQueue: *maxQueue,
	}
	if err := a.Open(); err != nil {
		return err
	}
	if !*once {
		err := a.Run(ctx, *interval)
		if ctx.Err() != nil {
			return nil // parada pedida (Ctrl+C, systemctl stop, SCM)
		}
		return err
	}

	if _, err := a.Enqueue(ctx); err != nil {
		return err
	}
	_, err = a.Flush(ctx)
	if n, _ := a.Pending(); n > 0 {
		return fmt.Errorf("%d snapshot(s) continuam na fila: %w", n, err)
	}
	return err
}

// parseOptionalPin aceita pin vazio: servidor com certificado público.
func parseOptionalPin(pin string) ([]byte, error) {
	if pin == "" {
		fmt.Fprintln(os.Stderr, "sem --ca-pin: o servidor será conferido pelas CAs do sistema (certificado público)")
		return nil, nil
	}
	return transport.ParsePin(pin)
}

func runSetServer(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("set-server", flag.ExitOnError)
	server := fs.String("server", "", "URL do servidor novo, https://host:porta (obrigatório)")
	pin := fs.String("ca-pin", "", "pin da CA do servidor novo, sha256:…; vazio = certificado público")
	dir := fs.String("state-dir", state.DefaultDir(), "diretório de estado do agente")
	fs.Parse(args)
	if *server == "" {
		return errors.New("--server é obrigatório")
	}
	caPin, err := parseOptionalPin(*pin)
	if err != nil {
		return err
	}
	d, err := state.Open(*dir)
	if err != nil {
		return err
	}
	info, err := runner.SetServer(ctx, d, runner.SetServerOptions{ServerURL: *server, CAPin: caPin})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "servidor trocado para %s (agente %s reconhecido)\nse o agente roda como serviço, reinicie-o\n", *server, info.AgentID)
	return nil
}
