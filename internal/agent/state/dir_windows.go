package state

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// DefaultDir é %ProgramData%\Invenire.
func DefaultDir() string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	return base + `\Invenire`
}

// restrict troca a DACL do diretório por uma protegida (sem herança de
// C:\ProgramData, que deixa Users ler) com controle total só para SYSTEM e
// Administradores, herdada por arquivos e subpastas. O dono passa a ser o
// grupo Administradores: qualquer usuário pode criar pastas em ProgramData, e
// quem criasse esta antes ficaria dono e poderia devolver o acesso a si mesmo.
// Trocar o dono exige elevação, então sem ela o agente falha aqui, cedo.
func restrict(path string) error {
	sd, err := windows.SecurityDescriptorFromString("O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner, nil, dacl, nil)
	if errors.Is(err, windows.ERROR_INVALID_OWNER) || errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return fmt.Errorf("%w (rode como administrador)", err)
	}
	return err
}
