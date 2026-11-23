// SPDX-License-Identifier: AGPL-3.0-only

// Package pki tem as CAs do Invenire.
//
// A CA da instalação fica em disco, no diretório de dados do servidor, e
// assina o certificado TLS do servidor (quando ele não usa um certificado
// público). Cada tenant tem a sua própria CA, independente, que assina os
// crachás (certificados cliente) dos agentes dele; a chave dela fica no banco,
// cifrada com a chave-mestra (tenant.go).
//
// Revogar um agente não passa pela CA: basta preencher agent.revoked_at, que
// o servidor consulta a cada requisição.
package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	caCertFile = "ca.crt"
	caKeyFile  = "ca.key"

	caValidity     = 10 * 365 * 24 * time.Hour
	agentValidity  = 2 * 365 * 24 * time.Hour
	serverValidity = 90 * 24 * time.Hour
	// Folga para relógios de máquinas inventariadas que andam atrasados.
	backdate = time.Hour
)

// CA é a autoridade certificadora interna.
type CA struct {
	Cert *x509.Certificate
	key  crypto.Signer
	pem  []byte
}

// LoadOrCreate abre a CA em dir, ou cria uma nova se ainda não existir.
func LoadOrCreate(dir string) (*CA, error) {
	certPath, keyPath := filepath.Join(dir, caCertFile), filepath.Join(dir, caKeyFile)
	certPEM, errCert := os.ReadFile(certPath)
	keyPEM, errKey := os.ReadFile(keyPath)
	switch {
	case errCert == nil && errKey == nil:
		return parse(certPEM, keyPEM)
	case errors.Is(errCert, os.ErrNotExist) && errors.Is(errKey, os.ErrNotExist):
	case errCert != nil && !errors.Is(errCert, os.ErrNotExist):
		return nil, errCert
	case errKey != nil && !errors.Is(errKey, os.ErrNotExist):
		return nil, errKey
	default:
		// Só um dos dois arquivos: melhor parar do que gerar outra CA e
		// invalidar todos os agentes cadastrados.
		return nil, fmt.Errorf("pki: %s e %s precisam existir juntos", certPath, keyPath)
	}

	ca, keyPEM, err := create()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(certPath, ca.pem, 0o644); err != nil {
		return nil, err
	}
	return ca, nil
}

func create() (*CA, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "Invenire CA", Organization: []string{"Invenire"}},
		NotBefore:             now.Add(-backdate),
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	ca, err := parse(certPEM, keyPEM)
	return ca, keyPEM, err
}

func parse(certPEM, keyPEM []byte) (*CA, error) {
	cb, _ := pem.Decode(certPEM)
	kb, _ := pem.Decode(keyPEM)
	if cb == nil || kb == nil {
		return nil, errors.New("pki: PEM da CA inválido")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, err
	}
	k, err := x509.ParsePKCS8PrivateKey(kb.Bytes)
	if err != nil {
		return nil, err
	}
	return fromParts(cert, k)
}

func fromParts(cert *x509.Certificate, k any) (*CA, error) {
	key, ok := k.(crypto.Signer)
	if !ok || !cert.IsCA {
		return nil, errors.New("pki: chave ou certificado da CA inválido")
	}
	if pub, ok := key.Public().(interface{ Equal(crypto.PublicKey) bool }); !ok || !pub.Equal(cert.PublicKey) {
		return nil, errors.New("pki: a chave não é a do certificado da CA")
	}
	return &CA{Cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})}, nil
}

// Load abre a CA em dir; devolve nil, sem erro, se ela não existir.
func Load(dir string) (*CA, error) {
	certPEM, err := os.ReadFile(filepath.Join(dir, caCertFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, caKeyFile))
	if err != nil {
		return nil, err
	}
	return parse(certPEM, keyPEM)
}

// PEM devolve o certificado da CA, que o agente guarda para validar o servidor.
func (ca *CA) PEM() []byte { return ca.pem }

// Pool devolve um pool só com esta CA.
func (ca *CA) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.Cert)
	return p
}

// SignAgentCSR confere a assinatura do CSR e emite o certificado cliente do
// agente. Do CSR só a chave pública é aproveitada: o nome (o id do agente) e
// os usos vêm do servidor.
func (ca *CA) SignAgentCSR(csrPEM []byte, agentID string) (certPEM []byte, fingerprint string, err error) {
	b, _ := pem.Decode(csrPEM)
	if b == nil || b.Type != "CERTIFICATE REQUEST" {
		return nil, "", errors.New("pki: CSR não está em PEM")
	}
	csr, err := x509.ParseCertificateRequest(b.Bytes)
	if err != nil {
		return nil, "", fmt.Errorf("pki: CSR inválido: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, "", fmt.Errorf("pki: assinatura do CSR: %w", err)
	}
	if err := checkKey(csr.PublicKey); err != nil {
		return nil, "", err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: agentID, OrganizationalUnit: []string{"agent"}},
		NotBefore:    now.Add(-backdate),
		NotAfter:     now.Add(agentValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, csr.PublicKey, ca.key)
	if err != nil {
		return nil, "", err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), Fingerprint(der), nil
}

// checkKey recusa chaves fracas no CSR.
func checkKey(pub any) error {
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		if k.Curve.Params().BitSize < 256 {
			return errors.New("pki: curva ECDSA menor que P-256")
		}
	case *rsa.PublicKey:
		if k.Size()*8 < 2048 {
			return errors.New("pki: chave RSA menor que 2048 bits")
		}
	case ed25519.PublicKey:
	default:
		return fmt.Errorf("pki: tipo de chave não aceito: %T", pub)
	}
	return nil
}

// ServerCert emite um certificado TLS de servidor para os nomes e IPs dados.
// É gerado na subida, com chave nova, e não fica em disco.
func (ca *CA) ServerCert(hosts []string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "invenire-server"},
		NotBefore:    now.Add(-backdate),
		NotAfter:     now.Add(serverValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.key)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	// A CA vai junto na cadeia: no primeiro contato o agente ainda não a tem
	// e confere o certificado dela contra o pin que recebeu com o token.
	return tls.Certificate{Certificate: [][]byte{der, ca.Cert.Raw}, PrivateKey: key, Leaf: leaf}, nil
}

// Fingerprint é o SHA-256 em hex do certificado DER: a chave de agent.cert_fingerprint.
func Fingerprint(der []byte) string {
	h := sha256.Sum256(der)
	return hex.EncodeToString(h[:])
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		panic(err) // crypto/rand não falha
	}
	return n
}
