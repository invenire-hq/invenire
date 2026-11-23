// SPDX-License-Identifier: AGPL-3.0-only

//go:build !windows && !linux

package sysinfo

import (
	"context"
	"os"
)

func ReadSystem() (System, error) { return System{}, ErrUnsupported }

func Hostname() (string, error) { return os.Hostname() }

func MachineID() (string, error) { return "", ErrUnsupported }

func ReadOS() (OS, error) { return OS{}, ErrUnsupported }

func InstalledPackages(ctx context.Context) ([]Package, error) { return nil, ErrUnsupported }
