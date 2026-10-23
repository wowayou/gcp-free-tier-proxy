package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"example.com/gcp-free-tier-proxy/hy2-account/internal/account"
)

func TestQuotaBytesUsesExactDecimalGiBAndRejectsAmbiguity(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "181", "180.0000001", "NaN", "Inf", "1e2", "1/2", "1.2.3", " 1", "0.000000000000001"} {
		if _, err := quotaBytes(value); err == nil {
			t.Fatalf("invalid decimal quota accepted: %q", value)
		}
	}
	for value, expected := range map[string]int64{"1": 1073741824, "1.25": 1342177280, "0.1": 107374182, "140": 150323855360, "141": 151397597184, "150": 161061273600, "180": 193273528320} {
		got, err := quotaBytes(value)
		if err != nil || got != expected {
			t.Fatalf("%s: got %d, %v; want %d", value, got, err, expected)
		}
	}
}

func TestValidateCertModeRunsOfflineWithoutAccountConfig(t *testing.T) {
	// Exercise the real CLI dispatch: no root, config, secrets, database or
	// accountd are needed for a read-only staged-file check.
	dir := t.TempDir()
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, ed25519.SeedSize))
	now := time.Now()
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"rr.example.com"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(48 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	for path, data := range map[string][]byte{
		certPath: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		keyPath:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := os.Args
	t.Cleanup(func() { os.Args = before })
	invoke := func(args ...string) error {
		os.Args = append([]string{"hy2-account", "validate-cert", "--config", filepath.Join(dir, "missing-config.json")}, args...)
		return run()
	}
	if err := invoke(certPath, keyPath, "rr.example.com"); err != nil {
		t.Fatalf("valid pair rejected by offline CLI: %v", err)
	}
	if err := invoke(certPath, keyPath, "other.example.net"); err == nil || !strings.Contains(err.Error(), "SAN does not cover") {
		t.Fatalf("wrong hostname accepted: %v", err)
	}
	for _, args := range [][]string{{}, {certPath, keyPath}, {certPath, keyPath, "rr.example.com", "extra"}} {
		if err := invoke(args...); err == nil || !strings.Contains(err.Error(), "validate-cert requires") {
			t.Fatalf("invalid arguments accepted: %v, %v", args, err)
		}
	}
}

// fakeAdmin serves the admin socket on a temp Unix path and puts a fake
// systemctl (reporting an inactive core) first in PATH. No root, daemon or
// systemd action is involved.
func fakeAdmin(t *testing.T) (account.Config, chan account.AdminRequest) {
	t.Helper()
	dir := t.TempDir()
	fake := filepath.Join(dir, "systemctl")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n[ \"$1\" = show ] || exit 99\nprintf 'inactive\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	cfg := account.Config{AdminSocket: filepath.Join(dir, "admin.sock")}
	listener, err := net.Listen("unix", cfg.AdminSocket)
	if err != nil {
		t.Fatal(err)
	}
	calls := make(chan account.AdminRequest, 4)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req account.AdminRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		calls <- req
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Close() })
	return cfg, calls
}

func privateFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "import.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The mapping format must stay identical to the IMPLEMENTATION.md example.
func TestImportAcceptsDocumentedMappingFormat(t *testing.T) {
	cfg, calls := fakeAdmin(t)
	path := privateFile(t, `{"users":[{"name":"friend-1","password":"EXISTING_STRONG_PASSWORD_FOR_TEST_ONLY","quota_bytes":21474836480}]}`)
	if err := admin(cfg, []string{"import", path}); err != nil {
		t.Fatalf("documented import format rejected: %v", err)
	}
	select {
	case req := <-calls:
		if req.Action != "import" || len(req.Users) != 1 || req.Users[0].Name != "friend-1" || req.Users[0].Quota != 21474836480 {
			t.Fatalf("mapping not forwarded intact: %+v", req)
		}
	default:
		t.Fatal("no import request delivered")
	}
}

func TestImportRejectsAmbiguousOrUnsafeMappings(t *testing.T) {
	cfg, calls := fakeAdmin(t)
	for name, body := range map[string]string{
		"bare array":    `[{"name":"friend-1","password":"EXISTING_STRONG_PASSWORD_FOR_TEST_ONLY","quota_bytes":1}]`,
		"empty list":    `{"users":[]}`,
		"missing users": `{}`,
		"unknown field": `{"users":[{"name":"friend-1","password":"EXISTING_STRONG_PASSWORD_FOR_TEST_ONLY","quota_bytes":1,"owner":"guess"}]}`,
	} {
		if err := admin(cfg, []string{"import", privateFile(t, body)}); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	readable := privateFile(t, `{"users":[{"name":"friend-1","password":"EXISTING_STRONG_PASSWORD_FOR_TEST_ONLY","quota_bytes":1}]}`)
	if err := os.Chmod(readable, 0644); err != nil {
		t.Fatal(err)
	}
	if err := admin(cfg, []string{"import", readable}); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("group/world-readable credential mapping accepted: %v", err)
	}
	if len(calls) != 0 {
		t.Fatal("rejected mapping still reached accountd")
	}
}
