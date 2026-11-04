//go:build !windows

package state

import "os"

// DefaultDir é /var/lib/invenire.
func DefaultDir() string { return "/var/lib/invenire" }

// restrict deixa o diretório só para o dono (root, quando o agente roda como serviço).
func restrict(path string) error { return os.Chmod(path, 0o700) }
