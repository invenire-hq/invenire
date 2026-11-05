package main

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/athosbes/invenire/internal/agent/state"
)

// serviceName é o nome do serviço do Windows, da origem no Event Log e da
// unit do systemd.
const serviceName = "invenire-agent"

// serviceConfig é como o serviço chama o run.
type serviceConfig struct {
	Bin      string // para onde o executável é copiado e de onde o serviço roda
	StateDir string
	Interval time.Duration
	MaxQueue int
}

func (c serviceConfig) runArgs() []string {
	return []string{"run",
		"--interval", c.Interval.String(),
		"--state-dir", c.StateDir,
		"--max-queue", strconv.Itoa(c.MaxQueue)}
}

func runService(ctx context.Context, args []string) error {
	const use = "uso: invenire-agent service install [opções] | uninstall"
	if len(args) == 0 {
		return errors.New(use)
	}
	switch args[0] {
	case "install":
		fs := flag.NewFlagSet("service install", flag.ExitOnError)
		c := serviceConfig{}
		fs.StringVar(&c.Bin, "bin", defaultBin(), "para onde copiar o executável; o serviço roda dali")
		fs.StringVar(&c.StateDir, "state-dir", state.DefaultDir(), "diretório de estado do agente")
		fs.DurationVar(&c.Interval, "interval", 4*time.Hour, "intervalo entre coletas")
		fs.IntVar(&c.MaxQueue, "max-queue", 100, "snapshots guardados na fila sem conseguir enviar")
		fs.Parse(args[1:])
		if c.Interval < time.Minute {
			return errors.New("--interval precisa ser de pelo menos 1m")
		}
		var err error
		if c.Bin, err = filepath.Abs(c.Bin); err != nil {
			return err
		}
		if c.StateDir, err = filepath.Abs(c.StateDir); err != nil {
			return err
		}
		// Sem cadastro o serviço só ficaria falhando: melhor avisar agora.
		d, err := state.Open(c.StateDir)
		if err != nil {
			return err
		}
		if !d.Enrolled() {
			return fmt.Errorf("o agente não está cadastrado em %s: rode invenire-agent enroll antes", c.StateDir)
		}
		return installService(ctx, c)
	case "uninstall":
		return uninstallService(ctx)
	default:
		return errors.New(use)
	}
}

// copySelf copia o executável em uso para dst, a não ser que já seja ele.
// Grava ao lado e renomeia, para nunca deixar um executável pela metade.
func copySelf(dst string) error {
	src, err := os.Executable()
	if err != nil {
		return err
	}
	if src, err = filepath.EvalSymlinks(src); err != nil {
		return err
	}
	si, err := os.Stat(src)
	if err != nil {
		return err
	}
	if di, err := os.Stat(dst); err == nil && os.SameFile(si, di) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

//go:embed invenire-agent.service.tmpl
var unitTemplate string

// systemdUnit monta a unit do systemd para c.
func systemdUnit(c serviceConfig) (string, error) {
	if !strings.HasPrefix(c.Bin, "/") || !strings.HasPrefix(c.StateDir, "/") {
		return "", errors.New("caminhos do serviço precisam ser absolutos")
	}
	exec := []string{systemdQuote(c.Bin)}
	for _, a := range c.runArgs() {
		exec = append(exec, systemdQuote(a))
	}
	var b bytes.Buffer
	err := template.Must(template.New("unit").Parse(unitTemplate)).Execute(&b, map[string]string{
		"ExecStart": strings.Join(exec, " "),
		// "-": sem erro se o diretório sumir; o run é que reclama.
		"StateDir": systemdQuote("-" + c.StateDir),
	})
	return b.String(), err
}

// systemdQuote prepara um argumento para ExecStart: aspas quando há espaço
// ou aspas, e % e $ dobrados para o systemd não os expandir.
func systemdQuote(s string) string {
	s = strings.NewReplacer("%", "%%", "$", "$$").Replace(s)
	if !strings.ContainsAny(s, " \t\"'\\;") && s != "" {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
