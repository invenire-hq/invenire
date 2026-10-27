package collect

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/turetos/invenire/pkg/snapshot"
)

// Abre sempre a visão de 64 bits do registro, mesmo num binário de 32 bits.
const view64 = registry.WOW64_64KEY

var procGetSystemFirmwareTable = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemFirmwareTable")

// readSMBIOS lê a tabela SMBIOS bruta pela API do Windows, sem WMI.
func readSMBIOS() (table []byte, major, minor byte, err error) {
	const rsmb = 'R'<<24 | 'S'<<16 | 'M'<<8 | 'B'
	n, _, err := procGetSystemFirmwareTable.Call(rsmb, 0, 0, 0)
	if n == 0 {
		return nil, 0, 0, fmt.Errorf("GetSystemFirmwareTable: %w", err)
	}
	buf := make([]byte, n)
	n2, _, err := procGetSystemFirmwareTable.Call(rsmb, 0, uintptr(unsafe.Pointer(&buf[0])), n)
	if n2 == 0 || n2 > n {
		return nil, 0, 0, fmt.Errorf("GetSystemFirmwareTable: %w", err)
	}
	// RawSMBIOSData: Used20CallingMethod, Major, Minor, DmiRevision, Length (DWORD), dados.
	if n2 < 8 {
		return nil, 0, 0, errors.New("smbios: cabeçalho curto")
	}
	length := int(uint32(buf[4]) | uint32(buf[5])<<8 | uint32(buf[6])<<16 | uint32(buf[7])<<24)
	if 8+length > int(n2) {
		length = int(n2) - 8
	}
	return buf[8 : 8+length], buf[1], buf[2], nil
}

func collectHost(ctx context.Context) (snapshot.Host, error) {
	var h snapshot.Host
	var errs []error

	if name, err := windows.ComputerName(); err == nil {
		h.Hostname = name
	}

	table, major, minor, err := readSMBIOS()
	if err == nil {
		var si systemInfo
		si, err = parseSMBIOSSystem(table, major, minor)
		h.SMBIOSUUID, h.Serial, h.Manufacturer, h.Model = si.UUID, si.Serial, si.Manufacturer, si.Product
	}
	if err != nil {
		errs = append(errs, err)
	}

	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|view64)
	if err == nil {
		h.MachineID, _, err = k.GetStringValue("MachineGuid")
		k.Close()
	}
	if err != nil {
		errs = append(errs, fmt.Errorf("MachineGuid: %w", err))
	}
	return h, errors.Join(errs...)
}

func collectOS(ctx context.Context) (snapshot.OS, error) {
	o := snapshot.OS{Family: "windows", Arch: windowsArch()}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE|view64)
	if err != nil {
		return o, err
	}
	defer k.Close()

	str := func(name string) string { v, _, _ := k.GetStringValue(name); return v }
	o.Name = str("ProductName")
	o.Version = str("DisplayVersion")
	if o.Version == "" {
		o.Version = str("ReleaseId")
	}
	o.Build = str("CurrentBuildNumber")

	// O registro continua dizendo "Windows 10" no Windows 11; o build desfaz a dúvida.
	if b, _ := strconv.Atoi(o.Build); b >= 22000 {
		o.Name = strings.Replace(o.Name, "Windows 10", "Windows 11", 1)
	}
	if mj, _, err := k.GetIntegerValue("CurrentMajorVersionNumber"); err == nil {
		mn, _, _ := k.GetIntegerValue("CurrentMinorVersionNumber")
		o.Kernel = fmt.Sprintf("%d.%d.%s", mj, mn, o.Build)
		if ubr, _, err := k.GetIntegerValue("UBR"); err == nil {
			o.Kernel += fmt.Sprintf(".%d", ubr)
		}
	}
	return o, nil
}

func windowsArch() string {
	var process, native uint16
	if err := windows.IsWow64Process2(windows.CurrentProcess(), &process, &native); err != nil {
		return ""
	}
	switch native {
	case 0x8664:
		return "amd64"
	case 0xAA64:
		return "arm64"
	case 0x014c:
		return "386"
	}
	return ""
}

const uninstallPath = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`

func collectSoftware(ctx context.Context) ([]snapshot.Software, error) {
	var out []snapshot.Software
	var errs []error
	seen := map[string]bool{}

	add := func(root registry.Key, path string, access uint32, arch, sid string) {
		list, err := readUninstall(root, path, access, arch, sid)
		if err != nil && !errors.Is(err, registry.ErrNotExist) {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
		}
		for _, sw := range list {
			// Num Windows de 32 bits as duas visões são a mesma chave.
			key := sw.UserSID + "\x00" + sw.PackageID + "\x00" + sw.Name + "\x00" + sw.Version
			if !seen[key] {
				seen[key] = true
				out = append(out, sw)
			}
		}
	}

	arch64 := "x64"
	if windowsArch() == "arm64" {
		arch64 = "arm64"
	}
	add(registry.LOCAL_MACHINE, uninstallPath, registry.WOW64_64KEY, arch64, "")
	add(registry.LOCAL_MACHINE, uninstallPath, registry.WOW64_32KEY, "x86", "")

	// Instalações por usuário: só as hives carregadas (usuários com sessão ou
	// serviços). Carregar o NTUSER.DAT dos demais fica para depois.
	users, err := registry.USERS.ReadSubKeyNames(-1)
	if err != nil {
		errs = append(errs, fmt.Errorf("HKU: %w", err))
	}
	for _, sid := range users {
		if strings.HasSuffix(sid, "_Classes") || sid == ".DEFAULT" {
			continue
		}
		add(registry.USERS, sid+`\`+uninstallPath, 0, "", sid)
	}
	return out, errors.Join(errs...)
}

func readUninstall(root registry.Key, path string, access uint32, arch, sid string) ([]snapshot.Software, error) {
	k, err := registry.OpenKey(root, path, registry.ENUMERATE_SUB_KEYS|access)
	if err != nil {
		return nil, err
	}
	defer k.Close()
	names, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return nil, err
	}

	var out []snapshot.Software
	for _, name := range names {
		sk, err := registry.OpenKey(k, name, registry.QUERY_VALUE|access)
		if err != nil {
			continue
		}
		str := func(v string) string { s, _, _ := sk.GetStringValue(v); return strings.TrimSpace(s) }
		sw := snapshot.Software{
			Source:      snapshot.SourceWinRegistry,
			PackageID:   name,
			Name:        str("DisplayName"),
			Publisher:   str("Publisher"),
			Version:     str("DisplayVersion"),
			Arch:        arch,
			InstallPath: strings.Trim(str("InstallLocation"), `"`),
			InstallDate: str("InstallDate"),
			UserSID:     sid,
		}
		sc, _, _ := sk.GetIntegerValue("SystemComponent")
		sw.SystemComponent = sc == 1
		parent, release := str("ParentKeyName"), str("ReleaseType")
		sk.Close()

		// Sem nome não há o que identificar; ParentKeyName e ReleaseType marcam
		// atualizações e hotfixes, que pertencem a outro produto.
		if sw.Name == "" || parent != "" || isUpdate(release) {
			continue
		}
		out = append(out, sw)
	}
	return out, nil
}

func isUpdate(releaseType string) bool {
	switch strings.ToLower(releaseType) {
	case "update", "hotfix", "security update", "service pack":
		return true
	}
	return false
}
