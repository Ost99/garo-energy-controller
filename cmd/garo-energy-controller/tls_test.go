package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTestCert(t *testing.T, certPath, keyPath, name string, mod time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if certPath != "" {
		writePEM(t, certPath, "CERTIFICATE", der, mod)
	}
	if keyPath != "" {
		writePEM(t, keyPath, "EC PRIVATE KEY", keyDER, mod)
	}
}

func writePEM(t *testing.T, path, blockType string, der []byte, mod time.Time) {
	t.Helper()
	data := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func servedName(t *testing.T, r *certReloader) string {
	t.Helper()
	cert, err := r.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf.Subject.CommonName
}

func TestCertReloader(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "fullchain.pem")
	keyPath := filepath.Join(dir, "privkey.pem")
	base := time.Now().Add(-time.Hour)

	writeTestCert(t, certPath, keyPath, "first", base)
	r, err := newCertReloader(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := servedName(t, r); got != "first" {
		t.Fatalf("served %q, want first", got)
	}

	// Renewal half-done: new certificate, old key. Keep serving the old pair.
	writeTestCert(t, certPath, "", "second", base.Add(time.Minute))
	if got := servedName(t, r); got != "first" {
		t.Fatalf("mismatched pair: served %q, want first", got)
	}

	// Renewal complete: the new pair is picked up without a restart.
	writeTestCert(t, certPath, keyPath, "third", base.Add(2*time.Minute))
	if got := servedName(t, r); got != "third" {
		t.Fatalf("after renewal: served %q, want third", got)
	}
}

func TestCertReloaderMissingFilesFailsAtStartup(t *testing.T) {
	dir := t.TempDir()
	_, err := newCertReloader(filepath.Join(dir, "missing.pem"), filepath.Join(dir, "missing.key"))
	if err == nil {
		t.Fatal("expected error for missing certificate files")
	}
}
