// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/invenire-hq/invenire/internal/agent/runner"
)

// defaultBin fica em Arquivos de Programas, que só administradores alteram:
// o serviço roda como SYSTEM, e quem pudesse trocar o executável viraria SYSTEM.
func defaultBin() string {
	base := os.Getenv("ProgramFiles")
	if base == "" {
		base = `C:\Program Files`
	}
	return filepath.Join(base, "Invenire", "invenire-agent.exe")
}

func isService() bool {
	ok, err := svc.IsWindowsService()
	return err == nil && ok
}

// serviceMain roda quando o gerenciador de serviços inicia o executável, com
// os argumentos gravados no install ("run --interval …"). O log vai para o
// Event Log (Aplicativo, origem invenire-agent).
func serviceMain(args []string) int {
	log := slog.New(slog.DiscardHandler)
	if elog, err := eventlog.Open(serviceName); err == nil {
		defer elog.Close()
		log = slog.New(newEventHandler(elog))
	}
	if len(args) == 0 || args[0] != "run" {
		log.Error("serviço instalado com argumentos inesperados; rode service install de novo", "args", strings.Join(args, " "))
		return 2
	}
	h := &serviceHandler{args: args[1:], log: log}
	if err := svc.Run(serviceName, h); err != nil {
		log.Error("falha ao falar com o gerenciador de serviços", "err", err)
		return 1
	}
	return int(h.exit)
}

type serviceHandler struct {
	args []string
	log  *slog.Logger
	exit uint32
}

func (h *serviceHandler) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runRun(ctx, h.args, h.log) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for {
		select {
		case err := <-done:
			// O run só termina sozinho com erro (certificado recusado, estado
			// ilegível). Código próprio de serviço: o Windows não aplica as
			// ações de recuperação, que são só para quedas.
			h.exit = 1
			if errors.Is(err, runner.ErrRevoked) {
				h.exit = exitRevoked
			}
			h.log.Error("o agente parou", "err", err)
			return true, h.exit
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending, WaitHint: 25000}
				cancel()
				select {
				case <-done:
				case <-time.After(20 * time.Second):
					h.log.Warn("o agente não terminou a tempo; parando assim mesmo")
				}
				return false, 0
			}
		}
	}
}

// installService copia o executável, cria ou atualiza o serviço e o inicia.
// Rodar de novo com um executável novo é a atualização.
func installService(_ context.Context, c serviceConfig) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("gerenciador de serviços: %w (rode como administrador)", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	exists := err == nil
	if exists {
		defer s.Close()
		if err := stopService(s); err != nil {
			return err
		}
	}
	if err := copySelf(c.Bin); err != nil {
		return fmt.Errorf("copiar o executável: %w", err)
	}

	cfg := mgr.Config{
		DisplayName:      "Invenire Agent",
		Description:      "Inventário desta máquina (hardware, sistema e software), enviado ao servidor Invenire.",
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true,
		ErrorControl:     mgr.ErrorNormal,
	}
	if exists {
		cur, err := s.Config()
		if err != nil {
			return err
		}
		cmd := []string{windows.EscapeArg(c.Bin)}
		for _, a := range c.runArgs() {
			cmd = append(cmd, windows.EscapeArg(a))
		}
		cur.BinaryPathName = strings.Join(cmd, " ")
		cur.DisplayName, cur.Description = cfg.DisplayName, cfg.Description
		cur.StartType, cur.DelayedAutoStart, cur.ErrorControl = cfg.StartType, cfg.DelayedAutoStart, cfg.ErrorControl
		if err := s.UpdateConfig(cur); err != nil {
			return err
		}
	} else {
		if s, err = m.CreateService(serviceName, c.Bin, cfg, c.runArgs()...); err != nil {
			return err
		}
		defer s.Close()
	}
	// Queda do processo: tenta de novo em 1, 5 e 15 min; o contador zera em um dia.
	err = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: time.Minute},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Minute},
		{Type: mgr.ServiceRestart, Delay: 15 * time.Minute},
	}, 24*60*60)
	if err != nil {
		return err
	}

	eventlog.Remove(serviceName) // de uma instalação anterior
	if err := eventlog.InstallAsEventCreate(serviceName, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil {
		return fmt.Errorf("origem no Event Log: %w", err)
	}
	if err := s.Start(); err != nil {
		return fmt.Errorf("iniciar o serviço: %w", err)
	}
	fmt.Fprintf(os.Stderr, "serviço %s instalado e iniciado (%s)\nlog: Visualizador de Eventos > Logs do Windows > Aplicativo, origem %s\n",
		serviceName, c.Bin, serviceName)
	return nil
}

// uninstallService para e remove o serviço. Executável e estado ficam.
func uninstallService(context.Context) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("gerenciador de serviços: %w (rode como administrador)", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("serviço %s: %w", serviceName, err)
	}
	defer s.Close()
	if err := stopService(s); err != nil {
		return err
	}
	if err := s.Delete(); err != nil {
		return err
	}
	eventlog.Remove(serviceName)
	fmt.Fprintf(os.Stderr, "serviço %s removido; o executável e o estado continuam onde estavam\n", serviceName)
	return nil
}

// stopService pede a parada e espera até 30s.
func stopService(s *mgr.Service) error {
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Stopped {
		return nil
	}
	if st.State != svc.StopPending {
		if _, err := s.Control(svc.Stop); err != nil {
			return fmt.Errorf("parar o serviço: %w", err)
		}
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		time.Sleep(300 * time.Millisecond)
		if st, err = s.Query(); err != nil {
			return err
		}
		if st.State == svc.Stopped {
			return nil
		}
	}
	return errors.New("o serviço não parou em 30s")
}

// eventHandler escreve cada registro do slog como um evento, com o nível
// virando Informação, Aviso ou Erro.
type eventHandler struct {
	inner slog.Handler
	w     *eventWriter
}

type eventWriter struct {
	mu    sync.Mutex
	elog  *eventlog.Log
	level slog.Level
}

func newEventHandler(elog *eventlog.Log) *eventHandler {
	w := &eventWriter{elog: elog}
	inner := slog.NewTextHandler(w, &slog.HandlerOptions{
		// O evento já tem data e nível.
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && (a.Key == slog.TimeKey || a.Key == slog.LevelKey) {
				return slog.Attr{}
			}
			return a
		},
	})
	return &eventHandler{inner: inner, w: w}
}

func (h *eventHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *eventHandler) Handle(ctx context.Context, r slog.Record) error {
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	h.w.level = r.Level
	return h.inner.Handle(ctx, r)
}

func (h *eventHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &eventHandler{inner: h.inner.WithAttrs(as), w: h.w}
}

func (h *eventHandler) WithGroup(name string) slog.Handler {
	return &eventHandler{inner: h.inner.WithGroup(name), w: h.w}
}

// Write recebe um registro inteiro por vez: o TextHandler escreve cada um
// numa chamada só, e Handle segura o mutex enquanto isso.
func (w *eventWriter) Write(p []byte) (int, error) {
	const eventID = 1
	msg := strings.TrimSpace(string(p))
	var err error
	switch {
	case w.level >= slog.LevelError:
		err = w.elog.Error(eventID, msg)
	case w.level >= slog.LevelWarn:
		err = w.elog.Warning(eventID, msg)
	default:
		err = w.elog.Info(eventID, msg)
	}
	return len(p), err
}
