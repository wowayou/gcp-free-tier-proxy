package account

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var certificateNow = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func certificateTemplate() *x509.Certificate {
	return &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "rr.example.com"},
		DNSNames: []string{"rr.example.com"}, NotBefore: certificateNow.Add(-time.Hour),
		NotAfter: certificateNow.Add(30 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
}

// Fixed Ed25519 keys and times keep the fixtures deterministic and test-only.
func certificateFiles(t *testing.T, template *x509.Certificate, seed byte) (string, string) {
	t.Helper()
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	for path, data := range map[string][]byte{
		certPath: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		keyPath:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return certPath, keyPath
}

func TestCertificatePublicationUsesGoSANRules(t *testing.T) {
	for _, tc := range []struct {
		name, domain string
		dns          []string
		wantError    bool
	}{
		{"exact", "rr.example.com", []string{"rr.example.com"}, false},
		{"wildcard", "rr.example.com", []string{"*.example.com"}, false},
		{"multiple SANs", "rr.example.com", []string{"other.example.net", "rr.example.com"}, false},
		{"case and trailing dot", "RR.EXAMPLE.COM.", []string{"rr.example.com"}, false},
		{"CN only", "rr.example.com", nil, true},
		{"IP SAN only", "rr.example.com", nil, true},
		{"wrong SAN with matching CN", "rr.example.com", []string{"other.example.net"}, true},
		{"partial wildcard", "rr.example.com", []string{"r*.example.com"}, true},
		{"wildcard apex", "example.com", []string{"*.example.com"}, true},
		{"wildcard nested", "deep.rr.example.com", []string{"*.example.com"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cert := certificateTemplate()
			cert.DNSNames = tc.dns
			if tc.name == "IP SAN only" {
				cert.IPAddresses = []net.IP{net.ParseIP("203.0.113.10")}
			}
			certPath, keyPath := certificateFiles(t, cert, 1)
			err := ValidateCertificateFiles(certPath, keyPath, tc.domain, certificateNow)
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "SAN does not cover") {
					t.Fatalf("expected SAN rejection, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCertificatePublicationValidityBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name             string
		notBefore, until time.Duration
		wantError        string
	}{
		{"already valid", -time.Hour, 48 * time.Hour, ""},
		{"valid from now", 0, 48 * time.Hour, ""},
		{"future", time.Second, 48 * time.Hour, "not valid yet"},
		{"expired", -48 * time.Hour, -time.Second, "expires within 24h"},
		{"expires now", -time.Hour, 0, "expires within 24h"},
		{"inside safety margin", -time.Hour, 24*time.Hour - time.Second, "expires within 24h"},
		{"exactly 24h", -time.Hour, 24 * time.Hour, "expires within 24h"},
		{"over 24h", -time.Hour, 24*time.Hour + time.Second, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cert := certificateTemplate()
			cert.NotBefore, cert.NotAfter = certificateNow.Add(tc.notBefore), certificateNow.Add(tc.until)
			certPath, keyPath := certificateFiles(t, cert, 1)
			err := ValidateCertificateFiles(certPath, keyPath, "rr.example.com", certificateNow)
			if tc.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("expected %q, got %v", tc.wantError, err)
			}
		})
	}
}

func TestCertificatePublicationRejectsInvalidFilesAndHostname(t *testing.T) {
	cert, key := certificateFiles(t, certificateTemplate(), 1)
	_, otherKey := certificateFiles(t, certificateTemplate(), 2)
	if err := ValidateCertificateFiles(cert, otherKey, "rr.example.com", certificateNow); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("mismatched key accepted: %v", err)
	}
	for _, domain := range []string{"", "203.0.113.10", "*.example.com", "rr.example.com\n", strings.Repeat("a", 254)} {
		if err := ValidateCertificateFiles(cert, key, domain, certificateNow); err == nil || !strings.Contains(err.Error(), "concrete certificate hostname") {
			t.Fatalf("invalid hostname %q accepted: %v", domain, err)
		}
	}
	invalid := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(invalid, []byte("not PEM\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{invalid, key}, {cert, invalid}, {invalid + ".missing", key}, {cert, invalid + ".missing"}} {
		if err := ValidateCertificateFiles(pair[0], pair[1], "rr.example.com", certificateNow); err == nil {
			t.Fatalf("invalid file pair accepted: %v", pair)
		}
	}
}

func TestCertificatePublicationChecksLeafNotAnotherChainEntry(t *testing.T) {
	wrong := certificateTemplate()
	wrong.DNSNames = []string{"other.example.net"}
	cert, key := certificateFiles(t, wrong, 1)
	good, _ := certificateFiles(t, certificateTemplate(), 1)
	wrongPEM, err := os.ReadFile(cert)
	if err != nil {
		t.Fatal(err)
	}
	goodPEM, err := os.ReadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cert, append(wrongPEM, goodPEM...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCertificateFiles(cert, key, "rr.example.com", certificateNow); err == nil || !strings.Contains(err.Error(), "SAN does not cover") {
		t.Fatalf("invalid leaf accepted via later chain entry: %v", err)
	}
	// A valid leaf with the same trailing certificate is still a loadable pair.
	if err := os.WriteFile(cert, append(goodPEM, wrongPEM...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCertificateFiles(cert, key, "rr.example.com", certificateNow); err != nil {
		t.Fatal(err)
	}
}

func TestCertificatePublicationDoesNotDependOnCachedLeaf(t *testing.T) {
	t.Setenv("GODEBUG", "x509keypairleaf=0")
	cert, key := certificateFiles(t, certificateTemplate(), 1)
	if err := ValidateCertificateFiles(cert, key, "rr.example.com", certificateNow); err != nil {
		t.Fatal(err)
	}
}
