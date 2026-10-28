package sysinfo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const dmiDir = "/sys/class/dmi/id/"

func readTrim(path string) (string, error) {
	b, err := os.ReadFile(path)
	return strings.TrimSpace(string(b)), err
}

// ReadSystem lê a identificação do hardware em /sys/class/dmi/id.
// product_uuid e product_serial só são legíveis pelo root.
func ReadSystem() (System, error) {
	var s System
	var errs []error
	read := func(name string, dst *string) {
		v, err := readTrim(dmiDir + name)
		if err != nil {
			errs = append(errs, err)
			return
		}
		*dst = v
	}
	read("sys_vendor", &s.Manufacturer)
	read("product_name", &s.Product)
	read("product_version", &s.Version)
	read("product_serial", &s.Serial)
	read("product_uuid", &s.UUID)
	s.UUID = strings.ToUpper(s.UUID)
	return s, errors.Join(errs...)
}

// Hostname devolve o nome da máquina reportado pelo kernel.
func Hostname() (string, error) { return os.Hostname() }

// MachineID lê /etc/machine-id, ou /var/lib/dbus/machine-id se faltar.
func MachineID() (string, error) {
	id, err := readTrim("/etc/machine-id")
	if id != "" {
		return id, nil
	}
	if id2, err2 := readTrim("/var/lib/dbus/machine-id"); id2 != "" {
		return id2, nil
	} else if err == nil {
		err = err2
	}
	if err == nil {
		err = errors.New("machine-id vazio")
	}
	return "", err
}

// ReadOS lê /etc/os-release (ou /usr/lib/os-release) e uname.
func ReadOS() (OS, error) {
	o := OS{Family: "linux", Name: "Linux"}
	var u unix.Utsname
	if err := unix.Uname(&u); err == nil {
		o.Kernel = unix.ByteSliceToString(u.Release[:])
		o.Arch = unix.ByteSliceToString(u.Machine[:])
	}

	f, err := os.Open("/etc/os-release")
	if errors.Is(err, os.ErrNotExist) {
		f, err = os.Open("/usr/lib/os-release")
	}
	if err != nil {
		return o, err
	}
	defer f.Close()
	rel := ParseOSRelease(f)
	o.ID = rel["ID"]
	o.Version = rel["VERSION_ID"]
	o.Build = rel["BUILD_ID"]
	if rel["NAME"] != "" {
		o.Name = rel["NAME"]
	}
	return o, nil
}

const dpkgStatus = "/var/lib/dpkg/status"

// InstalledPackages lê os pacotes do dpkg e do rpm, os que existirem.
func InstalledPackages(ctx context.Context) ([]Package, error) {
	var out []Package
	var errs []error
	found := false

	if f, err := os.Open(dpkgStatus); err == nil {
		found = true
		list, err := ParseDpkgStatus(f, dpkgStatus)
		f.Close()
		out = append(out, list...)
		if err != nil {
			errs = append(errs, fmt.Errorf("dpkg: %w", err))
		}
	}

	if hasRpmDB() {
		if _, err := exec.LookPath("rpm"); err == nil {
			found = true
			list, err := queryRpm(ctx)
			out = append(out, list...)
			if err != nil {
				errs = append(errs, fmt.Errorf("rpm: %w", err))
			}
		}
	}

	if !found {
		errs = append(errs, errors.New("nenhum gerenciador de pacotes suportado (dpkg, rpm)"))
	}
	return out, errors.Join(errs...)
}

func hasRpmDB() bool {
	for _, p := range []string{"/var/lib/rpm", "/usr/lib/sysimage/rpm"} {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

func queryRpm(ctx context.Context) ([]Package, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "rpm", "-qa", "--qf", RpmQueryFormat)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return ParseRpmQuery(stdout, "rpm -qa"), nil
}
