// Comando invenire-agent inventaria a máquina local.
//
//	invenire-agent enroll --server https://invenire:8443 --token inv_… --ca-pin sha256:…
//	invenire-agent run
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
  collect   coleta o inventário e grava o snapshot JSON (modo offline)
  version   mostra a versão

O estado (chave, certificado, fila) fica em %s,
acessível só a administradores; rode enroll e run como administrador/root.
`, state.DefaultDir())
}

func main() {
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
	case "run":
		err = runRun(ctx, os.Args[2:])
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
	pin := fs.String("ca-pin", "", "pin da CA mostrado por invenire-server token create, sha256:… (obrigatório)")
	dir := fs.String("state-dir", state.DefaultDir(), "diretório de estado do agente")
	force := fs.Bool("force", false, "cadastrar de novo mesmo já havendo cadastro")
	fs.Parse(args)

	if *token == "" {
		*token = os.Getenv("INVENIRE_ENROLL_TOKEN")
	}
	if *server == "" || *token == "" || *pin == "" {
		return errors.New("--server, --token e --ca-pin são obrigatórios")
	}
	caPin, err := transport.ParsePin(*pin)
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

func runRun(ctx context.Context, args []string) error {
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
		Log:      slog.New(slog.NewTextHandler(os.Stderr, nil)),
		MaxQueue: *maxQueue,
	}
	if err := a.Open(); err != nil {
		return err
	}
	if !*once {
		return a.Run(ctx, *interval)
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
