// SPDX-License-Identifier: AGPL-3.0-only

// Package collect monta o snapshot da máquina local a partir do pkg/sysinfo.
// Cada coletor roda de forma independente: se um falha, o erro vai para
// collector_errors e o snapshot segue com o restante.
package collect

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/invenire-hq/invenire/pkg/snapshot"
	"github.com/invenire-hq/invenire/pkg/sysinfo"
)

type collector struct {
	name string
	run  func(ctx context.Context, s *snapshot.Snapshot) error
}

var collectors = []collector{
	{"host", collectHost},
	{"os", collectOS},
	{"software", collectSoftware},
}

// Collect roda todos os coletores e devolve o snapshot. Não devolve erro:
// falhas ficam registradas no próprio snapshot.
func Collect(ctx context.Context, agentVersion string) *snapshot.Snapshot {
	s := snapshot.New(agentVersion, time.Now())
	for _, c := range collectors {
		if err := runSafe(ctx, c, s); err != nil {
			s.CollectorErrors = append(s.CollectorErrors, snapshot.CollectorError{
				Collector: c.name,
				Error:     err.Error(),
			})
		}
	}
	return s
}

// Identity coleta só o host e o SO, que o cadastro precisa.
func Identity(ctx context.Context) (snapshot.Host, snapshot.OS, error) {
	s := snapshot.New("", time.Now())
	var errs []error
	for _, c := range []collector{{"host", collectHost}, {"os", collectOS}} {
		if err := runSafe(ctx, c, s); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", c.name, err))
		}
	}
	return s.Host, s.OS, errors.Join(errs...)
}

func runSafe(ctx context.Context, c collector, s *snapshot.Snapshot) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return c.run(ctx, s)
}

func collectHost(ctx context.Context, s *snapshot.Snapshot) error {
	sys, errSys := sysinfo.ReadSystem()
	mid, errMID := sysinfo.MachineID()
	s.Host = snapshot.Host{
		SMBIOSUUID:   sys.UUID,
		MachineID:    mid,
		Serial:       sys.Serial,
		Manufacturer: sys.Manufacturer,
		Model:        sys.Product,
	}
	var errHost error
	s.Host.Hostname, errHost = sysinfo.Hostname()
	return errors.Join(errSys, errMID, errHost)
}

func collectOS(ctx context.Context, s *snapshot.Snapshot) error {
	o, err := sysinfo.ReadOS()
	s.OS = snapshot.OS{
		Family:  o.Family,
		Name:    o.Name,
		Version: o.Version,
		Build:   o.Build,
		Kernel:  o.Kernel,
		Arch:    o.Arch,
		ID:      o.ID,
	}
	if s.OS.Family == "" {
		s.OS.Family = "other"
	}
	if s.OS.Arch == "" {
		s.OS.Arch = runtime.GOARCH
	}
	return err
}

func collectSoftware(ctx context.Context, s *snapshot.Snapshot) error {
	pkgs, err := sysinfo.InstalledPackages(ctx)
	s.Software = make([]snapshot.Software, 0, len(pkgs))
	for _, p := range pkgs {
		s.Software = append(s.Software, snapshot.Software{
			Source:          p.Source,
			PackageID:       noNUL(p.ID),
			Name:            noNUL(p.Name),
			Publisher:       noNUL(p.Publisher),
			Version:         noNUL(p.Version),
			Arch:            noNUL(p.Arch),
			InstallPath:     noNUL(p.InstallPath),
			InstallDate:     noNUL(p.InstallDate),
			UserSID:         p.UserSID,
			SystemComponent: p.SystemComponent,
		})
	}
	return err
}

// noNUL tira caracteres NUL, que o servidor recusa (o jsonb do PostgreSQL
// não os guarda). Aparecem em valores de registro gravados com o terminador
// incluído no comprimento.
func noNUL(s string) string {
	return strings.ReplaceAll(s, "\x00", "")
}
