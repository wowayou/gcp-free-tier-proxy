package account

import (
	"net"
	"strings"
	"testing"
)

// The official core gives ACL IPv4 literals as 16-byte IPv4-mapped IPs. A
// broad reject(::ffff:0:0/96) therefore rejects every ordinary IPv4 target.
// This contract test runs without the pinned core binary.
func TestPrivateACLKeepsPublicIPv4AndBlocksMappedPrivate(t *testing.T) {
	rules := PrivateACL()
	if rules[len(rules)-1] != "direct(all)" {
		t.Fatal("allow rule must be last")
	}
	var cidrs []*net.IPNet
	for _, rule := range rules {
		if !strings.HasPrefix(rule, "reject(") || !strings.HasSuffix(rule, ")") {
			continue
		}
		value := strings.TrimSuffix(strings.TrimPrefix(rule, "reject("), ")")
		if !strings.Contains(value, "/") {
			continue
		}
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			t.Fatalf("invalid reject CIDR %q: %v", value, err)
		}
		cidrs = append(cidrs, network)
	}
	matches := func(ip net.IP) bool {
		for _, network := range cidrs {
			if network.Contains(ip) {
				return true
			}
		}
		return false
	}
	for _, value := range []string{"203.0.113.10", "8.8.8.8", "1.1.1.1", "::ffff:203.0.113.10"} {
		if matches(net.ParseIP(value)) {
			t.Fatalf("public address rejected by ACL: %s", value)
		}
	}
	for _, value := range []string{
		"0.1.2.3", "10.1.2.3", "100.64.0.1", "127.0.0.1", "169.254.169.254",
		"172.16.0.1", "192.0.0.1", "192.168.1.1", "198.18.0.1", "224.0.0.1", "240.0.0.1",
		"::ffff:10.1.2.3", "::ffff:127.0.0.1", "::ffff:169.254.169.254", "::ffff:192.168.1.1",
		"::", "::1", "::a01:203", "64:ff9b::a01:203", "64:ff9b:1::1", "100::1",
		"2001::1", "2002::1", "fc00::1", "fd00::1", "fe80::1", "ff02::1",
	} {
		if !matches(net.ParseIP(value)) {
			t.Fatalf("private/special address not rejected by ACL: %s", value)
		}
	}
}
