package pki

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MasterKeyEnv é a variável que, se definida, traz a chave-mestra em base64
// (32 bytes) no lugar do arquivo master.key.
const MasterKeyEnv = "INVENIRE_MASTER_KEY"

const masterKeyFile = "master.key"

// MasterKey cifra as chaves das CAs dos tenants guardadas no banco. Sem ela
// o servidor confere os crachás já emitidos, mas não emite novos.
type MasterKey struct{ aead cipher.AEAD }

// LoadOrCreateMasterKey lê a chave-mestra de INVENIRE_MASTER_KEY ou de
// dir/master.key, e cria o arquivo se nenhum dos dois existir.
func LoadOrCreateMasterKey(dir string) (*MasterKey, error) {
	if v := os.Getenv(MasterKeyEnv); v != "" {
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
		if err != nil || len(b) != 32 {
			return nil, fmt.Errorf("pki: %s precisa ter 32 bytes em base64", MasterKeyEnv)
		}
		return NewMasterKey(b)
	}
	path := filepath.Join(dir, masterKeyFile)
	b, err := os.ReadFile(path)
	if err == nil {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(raw) != 32 {
			return nil, fmt.Errorf("pki: %s inválido (esperava 32 bytes em base64)", path)
		}
		return NewMasterKey(raw)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	raw := make([]byte, 32)
	rand.Read(raw)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// O_EXCL: dois servidores subindo juntos não sobrescrevem um ao outro.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(raw) + "\n"); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return NewMasterKey(raw)
}

// NewMasterKey usa 32 bytes como chave AES-256-GCM.
func NewMasterKey(raw []byte) (*MasterKey, error) {
	if len(raw) != 32 {
		return nil, errors.New("pki: a chave-mestra tem 32 bytes")
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &MasterKey{aead: aead}, nil
}

// Seal cifra plaintext. aad amarra o cifrado ao seu dono (o tenant): copiado
// para outra linha, não abre.
func (m *MasterKey) Seal(plaintext, aad []byte) []byte {
	nonce := make([]byte, m.aead.NonceSize())
	rand.Read(nonce)
	return m.aead.Seal(nonce, nonce, plaintext, aad)
}

// Open decifra o que Seal produziu com o mesmo aad.
func (m *MasterKey) Open(sealed, aad []byte) ([]byte, error) {
	n := m.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("pki: chave cifrada curta demais")
	}
	b, err := m.aead.Open(nil, sealed[:n], sealed[n:], aad)
	if err != nil {
		return nil, errors.New("pki: não foi possível abrir a chave da CA (chave-mestra diferente da que a cifrou?)")
	}
	return b, nil
}

// NewTenantCA cria a CA de um tenant e devolve o certificado DER e a chave
// PKCS#8 DER, que quem chama cifra antes de guardar.
func NewTenantCA(tenantName string) (certDER, keyDER []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "Invenire CA: " + tenantName, Organization: []string{"Invenire"}},
		NotBefore:             now.Add(-backdate),
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	certDER, err = x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err = x509.MarshalPKCS8PrivateKey(key)
	return certDER, keyDER, err
}

// ParseCA monta uma CA a partir do certificado e da chave em DER.
func ParseCA(certDER, keyDER []byte) (*CA, error) {
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, err
	}
	k, err := x509.ParsePKCS8PrivateKey(keyDER)
	if err != nil {
		return nil, err
	}
	return fromParts(cert, k)
}

// VerifyAgentCert confere o crachá apresentado contra a CA que o emitiu:
// assinatura, validade e uso para autenticação de cliente.
func VerifyAgentCert(chain []*x509.Certificate, caDER []byte) error {
	if len(chain) == 0 {
		return errors.New("pki: sem certificado")
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	_, err = chain[0].Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	return err
}
