package main

import (
	"crypto/tls"
	"os"
	"sync"
	"time"
)

// certReloader serves a certificate from PEM files and picks up renewals
// without restarting the controller (a restart would drop in-memory control
// state mid-hour). Each handshake only stats the files; they are re-read when
// a modification time changes. If the new pair fails to load (for example
// while a renewal has replaced the certificate but not yet the key), the
// current certificate keeps being served and loading is retried on the next
// handshake.
type certReloader struct {
	certPath string
	keyPath  string

	mu      sync.Mutex
	cert    *tls.Certificate
	certMod time.Time
	keyMod  time.Time
}

func newCertReloader(certPath, keyPath string) (*certReloader, error) {
	r := &certReloader{certPath: certPath, keyPath: keyPath}
	if _, err := r.GetCertificate(nil); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	certInfo, certErr := os.Stat(r.certPath)
	keyInfo, keyErr := os.Stat(r.keyPath)
	if certErr == nil && keyErr == nil &&
		r.cert != nil &&
		certInfo.ModTime().Equal(r.certMod) &&
		keyInfo.ModTime().Equal(r.keyMod) {
		return r.cert, nil
	}

	cert, err := tls.LoadX509KeyPair(r.certPath, r.keyPath)
	if err != nil {
		if r.cert != nil {
			return r.cert, nil
		}
		return nil, err
	}

	r.cert = &cert
	if certErr == nil && keyErr == nil {
		r.certMod = certInfo.ModTime()
		r.keyMod = keyInfo.ModTime()
	}
	return r.cert, nil
}
