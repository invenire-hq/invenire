//go:build !windows && !linux

package collect

import (
	"context"

	"github.com/turetos/invenire/pkg/snapshot"
)

func collectHost(ctx context.Context) (snapshot.Host, error) { return snapshot.Host{}, ErrUnsupported }

func collectOS(ctx context.Context) (snapshot.OS, error) {
	return snapshot.OS{Family: "other"}, ErrUnsupported
}

func collectSoftware(ctx context.Context) ([]snapshot.Software, error) { return nil, ErrUnsupported }
