package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"example.com/gcp-free-tier-proxy/hy2-account/internal/account"
)

func TestMigrateSessionsCLIRunsOfflineWithReviewedCounts(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "data"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := account.Config{
		Database: dir + "/data/db", Anchor: dir + "/witness", SecretsFile: dir + "/secrets.json",
		AuthListen: "127.0.0.1:18081", PageListen: "127.0.0.1:18082", StatsURL: "http://127.0.0.1:18083",
		AdminSocket: dir + "/admin.sock", GenerationFile: dir + "/generation.json",
		GuardStatusFile: dir + "/guard.json", HealthFile: dir + "/health.json", HealthBlockedFile: dir + "/blocked",
		Certificate: dir + "/cert.pem", Server: "rr.example.com", SNI: "rr.example.com", Port: 443,
		QuotaPoolBytes: 140 * account.GiB, MaxAccounts: 8, MaxSessionsPerUser: 64, MaxSessionsTotal: 256, BackupDir: dir + "/backups",
	}
	secrets := account.Secrets{
		AuthKey:       base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 32))),
		CredentialKey: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("b", 32))),
		StatsSecret:   strings.Repeat("s", 43), PageUser: "usage", PagePassword: strings.Repeat("p", 43),
	}
	configFile := filepath.Join(dir, "config.json")
	countsFile := filepath.Join(dir, "counts.json")
	for path, data := range map[string]any{configFile: cfg, cfg.SecretsFile: secrets, countsFile: map[string]int64{"owner": 0}} {
		b, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := account.OpenStore(cfg, secrets, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddUsers([]account.NewUser{{Name: "owner", Password: strings.Repeat("o", 32), Quota: account.GiB}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Reconstruct a v1 held test fixture. No production data or systemd.
	db, err := sql.Open("sqlite", cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE session_totals; PRAGMA user_version=1;`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(dir, "systemctl")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n[ \"$1\" = show ] || exit 99\nprintf 'inactive\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	previous := os.Args
	t.Cleanup(func() { os.Args = previous })
	os.Args = []string{"hy2-account", "migrate-sessions", "--config", configFile, "--counts-file", countsFile}
	if err := run(); err != nil {
		t.Fatal("offline CLI migration failed", err)
	}
	opened, err := account.OpenStore(cfg, secrets, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	v, err := opened.View(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Users) != 1 || v.Users[0].Sessions != 0 || v.Users[0].Used != 0 || !v.Meta.Hold || !v.Meta.Review {
		t.Fatal("CLI migration changed held ledger")
	}
}
