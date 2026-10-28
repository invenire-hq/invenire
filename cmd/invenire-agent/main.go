// Comando invenire-agent inventaria a máquina local.
//
// Na v0 só existe o modo offline:
//
//	invenire-agent collect --out snapshot.json
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/athosbes/invenire/internal/agent/collect"
	"github.com/athosbes/invenire/pkg/snapshot"
)

// version é preenchida no build: -ldflags "-X main.version=0.1.0".
var version = "dev"

func usage() {
	fmt.Fprintf(os.Stderr, `uso: invenire-agent <comando> [opções]

comandos:
  collect   coleta o inventário e grava o snapshot JSON (modo offline)
  version   mostra a versão
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
	switch os.Args[1] {
	case "collect":
		err = runCollect(ctx, os.Args[2:])
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
