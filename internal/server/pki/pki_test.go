package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func newCSR(t *testing.T, curve elliptic.Curve) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "nome que o servidor ignora"},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func TestCAPersistsAndSigns(t *testing.T) {
	dir := t.TempDir()
	ca, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	again, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Cert.Equal(ca.Cert) {
		t.Fatal("a segunda abertura gerou outra CA")
	}

	certPEM, fp, err := ca.SignAgentCSR(newCSR(t, elliptic.P256()), "agente-1")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != "agente-1" || fp != Fingerprint(b.Bytes) {
		t.Fatalf("CN %q, fingerprint %s", cert.Subject.CommonName, fp)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: again.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("certificado do agente não valida contra a CA: %v", err)
	}

	srv, err := ca.ServerCert([]string{"localhost", "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Leaf.Verify(x509.VerifyOptions{Roots: ca.Pool(), DNSName: "localhost"}); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsBadCSR(t *testing.T) {
	ca, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ca.SignAgentCSR(newCSR(t, elliptic.P224()), "x"); err == nil {
		t.Error("aceitou P-224")
	}
	csr := newCSR(t, elliptic.P256())
	b, _ := pem.Decode(csr)
	b.Bytes[len(b.Bytes)-1] ^= 0xff // corrompe a assinatura
	if _, _, err := ca.SignAgentCSR(pem.EncodeToMemory(b), "x"); err == nil {
		t.Error("aceitou CSR com assinatura inválida")
	}
}

func TestRefusesHalfCA(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrCreate(dir); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(dir, caKeyFile))
	if _, err := LoadOrCreate(dir); err == nil {
		t.Fatal("gerou CA nova com o certificado antigo ainda no disco")
	}
}
