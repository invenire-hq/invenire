// SPDX-License-Identifier: AGPL-3.0-only

//go:build !windows && !linux

package main

import (
	"context"
	"errors"
)

var errNoService = errors.New("instalar como serviço só é suportado no Windows e no Linux com systemd")

func defaultBin() string                     { return "/usr/local/bin/invenire-agent" }
func isService() bool                        { return false }
func serviceMain(_ []string) int             { return 0 }
func uninstallService(context.Context) error { return errNoService }
func installService(context.Context, serviceConfig) error {
	return errNoService
}
