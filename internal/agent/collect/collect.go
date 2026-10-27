// Package collect monta o snapshot da máquina local. Cada coletor roda de
// forma independente: se um falha, o erro vai para collector_errors e o
// snapshot segue com o restante.
package collect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/turetos/invenire/pkg/snapshot"
)

// ErrUnsupported indica coletor não implementado nesta plataforma.
var ErrUnsupported = errors.New("não suportado nesta plataforma")

type collector struct {
	name string
	run  func(ctx context.Context, s *snapshot.Snapshot) error
}

var collectors = []collector{
	{"host", func(ctx context.Context, s *snapshot.Snapshot) (err error) {
		s.Host, err = collectHost(ctx)
		if s.Host.Hostname == "" {
			s.Host.Hostname, _ = os.Hostname()
		}
		return err
	}},
	{"os", func(ctx context.Context, s *snapshot.Snapshot) (err error) {
		s.OS, err = collectOS(ctx)
		if s.OS.Arch == "" {
			s.OS.Arch = runtime.GOARCH
		}
		return err
	}},
	{"software", func(ctx context.Context, s *snapshot.Snapshot) (err error) {
		s.Software, err = collectSoftware(ctx)
		return err
	}},
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

func runSafe(ctx context.Context, c collector, s *snapshot.Snapshot) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return c.run(ctx, s)
}
