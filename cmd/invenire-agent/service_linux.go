// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const unitPath = "/etc/systemd/system/" + serviceName + ".service"

func defaultBin() string { return "/usr/local/bin/invenire-agent" }

// No Linux o systemd chama o run diretamente.
func isService() bool            { return false }
func serviceMain(_ []string) int { return 0 }

// installService copia o executável, grava a unit e a habilita e (re)inicia.
// Rodar de novo atualiza executável e unit.
func installService(ctx context.Context, c serviceConfig) error {
	if os.Geteuid() != 0 {
		return errors.New("rode como root")
	}
	unit, err := systemdUnit(c)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errors.New("systemctl não encontrado: este sistema não usa systemd")
	}
	// Para antes de trocar o executável; sem erro se ainda não existir.
	systemctl(ctx, "stop", serviceName)
	if err := copySelf(c.Bin); err != nil {
		return fmt.Errorf("copiar o executável: %w", err)
	}
	tmp := unitPath + ".new"
	if err := os.WriteFile(tmp, []byte(unit), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, unitPath); err != nil {
		os.Remove(tmp)
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", serviceName}, {"start", serviceName}} {
		if err := systemctl(ctx, args...); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "serviço %s instalado e iniciado (%s)\nlog: journalctl -u %s\n", serviceName, unitPath, serviceName)
	return nil
}

// uninstallService para e remove a unit. Executável e estado ficam.
func uninstallService(ctx context.Context) error {
	if os.Geteuid() != 0 {
		return errors.New("rode como root")
	}
	if _, err := os.Stat(unitPath); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s não existe: o serviço não está instalado", unitPath)
	}
	if err := systemctl(ctx, "disable", "--now", serviceName); err != nil {
		return err
	}
	if err := os.Remove(unitPath); err != nil {
		return err
	}
	if err := systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	systemctl(ctx, "reset-failed", serviceName) // falha anterior, como a de um agente revogado
	fmt.Fprintf(os.Stderr, "serviço %s removido; o executável e o estado continuam onde estavam\n", serviceName)
	return nil
}

func systemctl(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
