package account

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"time"
)

// ValidateCertificateFiles checks the exact staged pair before publication.
// Use the same TLS parser and SAN-only hostname rules as Go/HY2; no CN fallback,
// network, trust-store lookup, account secrets or database are involved here.
func ValidateCertificateFiles(certPath, keyPath, domain string, now time.Time) error {
	if domain == "" || len(domain) > 253 || net.ParseIP(domain) != nil || !hostName.MatchString(domain) {
		return errors.New("SNI must be a concrete certificate hostname")
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return fmt.Errorf("certificate/key invalid or mismatch: %w", err)
	}
	// Parse explicitly rather than depending on the x509keypairleaf GODEBUG flag.
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return errors.New("certificate invalid")
	}
	if leaf.VerifyHostname(domain) != nil {
		return fmt.Errorf("certificate SAN does not cover %s", domain)
	}
	if now.Before(leaf.NotBefore) {
		return errors.New("certificate not valid yet")
	}
	if !leaf.NotAfter.After(now.Add(24 * time.Hour)) {
		return errors.New("certificate expires within 24h")
	}
	return nil
}
