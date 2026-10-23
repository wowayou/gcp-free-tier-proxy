package account

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCoreConfigKeepsBusinessUDPDisabledAndCertificateVerificationStrict(t *testing.T) {
	cfg := Config{AuthListen: "127.0.0.1:18081", StatsURL: "http://127.0.0.1:18083", SNI: "rr.example.com", Port: 443}
	core := CoreConfig(cfg, Secrets{StatsSecret: "independent-stats-secret"}, "0.0.0.0:443", "/cert", "/key")
	if core["disableUDP"] != true || core["ignoreClientBandwidth"] != true || core["speedTest"] != false {
		t.Fatal("unsafe HY2 forwarding/bandwidth/speed-test defaults")
	}
	tlsConfig, ok := core["tls"].(map[string]any)
	if !ok || tlsConfig["sniGuard"] != "strict" {
		t.Fatal("strict SNI guard missing")
	}
	auth, ok := core["auth"].(map[string]any)
	if !ok || auth["type"] != "http" || auth["http"].(map[string]any)["insecure"] != false {
		t.Fatal("HTTP auth must stay local and verified")
	}
	acl, ok := core["acl"].(map[string]any)
	if !ok || len(acl["inline"].([]string)) == 0 {
		t.Fatal("private/metadata ACL missing")
	}
	resolver, ok := core["resolver"].(map[string]any)
	if !ok || resolver["type"] != "system" {
		t.Fatal("explicit system resolver missing for domain ACL rules")
	}
}

func TestQuotaPoolCanBeExplicitlyRaisedWithoutChangingGuardCeiling(t *testing.T) {
	_, cfg, _, _ := testStore(t)
	for _, bytes := range []int64{1, 140 * GiB, 150 * GiB, MaxQuotaPoolGiB * GiB} {
		c := cfg
		c.QuotaPoolBytes = bytes
		if err := c.Validate(); err != nil {
			t.Fatalf("explicit quota pool %d rejected: %v", bytes, err)
		}
	}
	for _, bytes := range []int64{0, -1, MaxQuotaPoolGiB*GiB + 1} {
		c := cfg
		c.QuotaPoolBytes = bytes
		if err := c.Validate(); err == nil {
			t.Fatalf("unbounded/invalid quota pool %d accepted", bytes)
		}
	}
}

func TestReloadConfigUsesNewPoolAndPreservesIndependentSecrets(t *testing.T) {
	_, cfg, secrets, _ := testStore(t)
	cfg.QuotaPoolBytes = 150 * GiB
	must(t, writeJSON(cfg.SecretsFile, secrets, 0600))
	path := filepath.Join(t.TempDir(), "config.json")
	must(t, writeJSON(path, cfg, 0640))
	loaded, gotSecrets, err := LoadConfig(path)
	must(t, err)
	if loaded.QuotaPoolBytes != 150*GiB || gotSecrets != secrets {
		t.Fatal("config reload changed secrets or lost the approved pool")
	}
	cfg.QuotaPoolBytes = MaxQuotaPoolGiB*GiB + 1
	must(t, writeJSON(path, cfg, 0640))
	if _, _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "explicit quota pool") {
		t.Fatal("config reload accepted pool above the independent ceiling")
	}
}
