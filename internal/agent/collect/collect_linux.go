package collect

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

	"github.com/turetos/invenire/pkg/snapshot"
)

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func collectHost(ctx context.Context) (snapshot.Host, error) {
	const dmi = "/sys/class/dmi/id/"
	h := snapshot.Host{
		// product_uuid e product_serial só são legíveis pelo root.
		SMBIOSUUID:   readTrim(dmi + "product_uuid"),
		Serial:       readTrim(dmi + "product_serial"),
		Manufacturer: readTrim(dmi + "sys_vendor"),
		Model:        readTrim(dmi + "product_name"),
		MachineID:    readTrim("/etc/machine-id"),
	}
	if h.MachineID == "" {
		h.MachineID = readTrim("/var/lib/dbus/machine-id")
	}
	h.Hostname, _ = os.Hostname()
	if h.SMBIOSUUID == "" && h.MachineID == "" {
		return h, errors.New("sem product_uuid nem machine-id (o agente está rodando como root?)")
	}
	return h, nil
}

func collectOS(ctx context.Context) (snapshot.OS, error) {
	o := snapshot.OS{Family: "linux"}
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
		o.Name = "Linux"
		return o, err
	}
	defer f.Close()
	rel := parseOSRelease(f)
	o.ID = rel["ID"]
	o.Name = rel["NAME"]
	o.Version = rel["VERSION_ID"]
	o.Build = rel["BUILD_ID"]
	if o.Name == "" {
		o.Name = "Linux"
	}
	return o, nil
}

func collectSoftware(ctx context.Context) ([]snapshot.Software, error) {
	var out []snapshot.Software
	var errs []error
	found := false

	if f, err := os.Open("/var/lib/dpkg/status"); err == nil {
		found = true
		list, err := parseDpkgStatus(f)
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

const rpmFormat = `%{NAME}\t%{EPOCH}\t%{VERSION}-%{RELEASE}\t%{ARCH}\t%{VENDOR}\t%{INSTALLTIME}\n`

func queryRpm(ctx context.Context) ([]snapshot.Software, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "rpm", "-qa", "--qf", rpmFormat)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseRpmOutput(stdout), nil
}
