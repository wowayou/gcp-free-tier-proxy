package account

import "testing"

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
