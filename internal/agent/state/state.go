// Package state guarda o que o agente precisa entre execuções: a chave e o
// certificado do cadastro, a CA do servidor, o endereço do servidor e o hash
// do último snapshot gerado (para o prev_sha256 do próximo).
//
// O diretório só é acessível ao SYSTEM e aos administradores no Windows, e ao
// root no Linux: a chave privada é a identidade do agente.
package state

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	configFile = "agent.json"
	keyFile    = "agent.key"
	certFile   = "agent.crt"
	caFile     = "ca.crt"
	lastFile   = "last.sha256"
	queueDir   = "queue"
)

// ErrNotEnrolled indica que o agente ainda não foi cadastrado.
var ErrNotEnrolled = errors.New("agente não cadastrado: rode invenire-agent enroll")

// Config é o conteúdo de agent.json.
type Config struct {
	ServerURL string `json:"server_url"`
	AgentID   string `json:"agent_id"`
	AssetID   string `json:"asset_id"`
	// ServerTrust diz como o servidor é conferido: "pin" (padrão, também
	// quando vazio) pela CA guardada em ca.crt, ou "system" pelas CAs do
	// sistema, para servidores com certificado público.
	ServerTrust string `json:"server_trust,omitempty"`
}

// Valores de Config.ServerTrust.
const (
	TrustPin    = "pin"
	TrustSystem = "system"
)

// Dir é o diretório de estado do agente.
type Dir string

// Open garante que o diretório existe com as permissões restritas.
func Open(path string) (Dir, error) {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", err
	}
	if err := restrict(path); err != nil {
		return "", fmt.Errorf("restringir %s: %w", path, err)
	}
	return Dir(path), nil
}

// OpenUnrestricted abre o diretório sem mexer nas permissões. Só para testes
// e uso local por um usuário comum: a chave fica legível a quem lê a pasta.
func OpenUnrestricted(path string) (Dir, error) {
	return Dir(path), os.MkdirAll(path, 0o700)
}

func (d Dir) path(name string) string { return filepath.Join(string(d), name) }

// QueueDir é onde fica a fila de snapshots a enviar.
func (d Dir) QueueDir() string { return d.path(queueDir) }

// Enrolled diz se já existe um cadastro neste diretório.
func (d Dir) Enrolled() bool {
	_, err := os.Stat(d.path(configFile))
	return err == nil
}

// Config lê agent.json.
func (d Dir) Config() (Config, error) {
	var c Config
	b, err := os.ReadFile(d.path(configFile))
	if errors.Is(err, os.ErrNotExist) {
		return c, ErrNotEnrolled
	}
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}

// SaveEnrollment grava o resultado do cadastro. agent.json vai por último:
// é ele que marca o cadastro como completo. caPEM vazio = confiar nas CAs do
// sistema.
func (d Dir) SaveEnrollment(c Config, keyPEM, certPEM, caPEM []byte) error {
	for _, f := range []struct {
		name string
		data []byte
	}{{keyFile, keyPEM}, {certFile, certPEM}} {
		if err := writeAtomic(d.path(f.name), f.data); err != nil {
			return err
		}
	}
	return d.saveServer(c, caPEM)
}

// SetServer troca o endereço do servidor e como ele é conferido, mantendo o
// crachá: é o que o agente faz quando o tenant muda de servidor.
func (d Dir) SetServer(serverURL string, caPEM []byte) error {
	c, err := d.Config()
	if err != nil {
		return err
	}
	c.ServerURL = serverURL
	return d.saveServer(c, caPEM)
}

func (d Dir) saveServer(c Config, caPEM []byte) error {
	if len(caPEM) > 0 {
		c.ServerTrust = TrustPin
		if err := writeAtomic(d.path(caFile), caPEM); err != nil {
			return err
		}
	} else {
		c.ServerTrust = TrustSystem
		if err := os.Remove(d.path(caFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(d.path(configFile), append(b, '\n'))
}

// TLSConfig monta o cliente mTLS: apresenta o certificado do agente e
// confere o servidor pela CA recebida no cadastro ou, em ServerTrust
// "system", pelas CAs do sistema.
func (d Dir) TLSConfig() (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(d.path(certFile), d.path(keyFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotEnrolled
	}
	if err != nil {
		return nil, err
	}
	c, err := d.Config()
	if err != nil {
		return nil, err
	}
	if c.ServerTrust == TrustSystem {
		return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}}, nil
	}
	caPEM, err := os.ReadFile(d.path(caFile))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("ca.crt inválido")
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      pool,
		Certificates: []tls.Certificate{cert},
	}, nil
}

// LastSum devolve o SHA-256 do último snapshot gerado, ou vazio.
func (d Dir) LastSum() (string, error) {
	b, err := os.ReadFile(d.path(lastFile))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return strings.TrimSpace(string(b)), err
}

// SetLastSum grava o SHA-256 do último snapshot gerado.
func (d Dir) SetLastSum(sum string) error {
	return writeAtomic(d.path(lastFile), []byte(sum+"\n"))
}

// writeAtomic grava num temporário e renomeia, para uma queda no meio não
// deixar o arquivo pela metade.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// WriteAtomic é writeAtomic para a fila, que mora dentro deste diretório.
func WriteAtomic(path string, data []byte) error { return writeAtomic(path, data) }
