package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"example.com/gcp-free-tier-proxy/hy2-account/internal/account"
)

func humanStatusFixture() account.Status {
	return account.Status{
		RecoveryProtocol: account.RecoveryProtocol, Month: "2026-10", QuotaPool: 140 * account.GiB,
		Healthy: true, CoreLive: true, AuthAllowed: true, Used: 81 * account.GiB, Tx: account.GiB, Adjustment: 80 * account.GiB,
		Guard:       account.GuardStatus{Limit: 180 * account.GiB, Warn: 150 * account.GiB, Used: 34 * account.GiB, Healthy: true},
		VMRemaining: 146 * account.GiB,
		Users: []account.UserView{
			{Name: "owner", Quota: 90 * account.GiB, Used: 81 * account.GiB, Tx: account.GiB, Adjustment: 80 * account.GiB, Remaining: 9 * account.GiB, Percent: 90, Online: 1, SessionCap: 64, ActiveSessions: 1, ActiveSessionCap: 64},
			{Name: "friend-1", Quota: 30 * account.GiB, Remaining: 30 * account.GiB, SessionCap: 64, ActiveSessionCap: 64},
		},
	}
}

func fakeStatusAdmin(t *testing.T, response account.AdminResult) (account.Config, chan account.AdminRequest) {
	t.Helper()
	dir := t.TempDir()
	// Any service action is an error, including status probes. The human
	// overview is deliberately just one accountd status request.
	mustWrite := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), 0700); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(filepath.Join(dir, "systemctl"), "#!/bin/sh\nprintf 'unexpected system action\\n' >>\"$TEST_SYSTEM_CALLS\"\nexit 99\n")
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	systemCalls := filepath.Join(dir, "system-calls")
	t.Setenv("TEST_SYSTEM_CALLS", systemCalls)
	t.Cleanup(func() {
		if _, err := os.Lstat(systemCalls); !os.IsNotExist(err) {
			t.Error("read-only overview invoked systemctl")
		}
	})
	cfg := account.Config{AdminSocket: filepath.Join(dir, "admin.sock"), HealthBlockedFile: filepath.Join(dir, "blocked")}
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
		if req.Action != "status" || req.Name != "" {
			t.Error("overview made a non-status request")
		}
		_ = json.NewEncoder(w).Encode(response)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Close() })
	return cfg, calls
}

func captureStatusOutput(t *testing.T, f func() error) (string, error) {
	t.Helper()
	before := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { os.Stdout = before; r.Close(); w.Close() }()
	os.Stdout = w
	type captured struct {
		body []byte
		err  error
	}
	done := make(chan captured, 1)
	go func() { b, err := io.ReadAll(r); done <- captured{b, err} }()
	runErr := f()
	_ = w.Close()
	os.Stdout = before
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	return string(result.body), runErr
}

func TestHumanStatusCLIReadsOnceAndKeepsDefaultJSON(t *testing.T) {
	v := humanStatusFixture()
	secret := "SYNTHETIC_PRIVATE_PASSWORD_MUST_NOT_APPEAR"
	cfg, calls := fakeStatusAdmin(t, account.AdminResult{OK: true, Status: &v, Password: secret})
	body, err := captureStatusOutput(t, func() error { return admin(cfg, []string{"status", "--human"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"HY2 用量与账户", "服务端已就绪", "剩余 146.00 GiB", "正常采样 1.00 + 保守补账 80.00", "未分配 20.00 GiB", "owner", "余额偏低", "friend-1", "30.00", "只读", "不等于公网访问已验证"} {
		if !strings.Contains(body, text) {
			t.Fatalf("human overview omitted %q", text)
		}
	}
	if strings.Contains(body, secret) || strings.Contains(body, "hysteria2://") || len(calls) != 1 {
		t.Fatal("overview leaked credentials or made extra requests")
	}
	<-calls
	body, err = captureStatusOutput(t, func() error { return admin(cfg, []string{"status"}) })
	if err != nil {
		t.Fatal(err)
	}
	var decoded account.Status
	if err := json.Unmarshal([]byte(body), &decoded); err != nil || decoded.Users[0].Remaining != 9*account.GiB || decoded.Used != 81*account.GiB || strings.Contains(body, secret) || len(calls) != 1 {
		t.Fatal("default status JSON changed or rounded exact counters")
	}
	if _, err := os.Lstat(cfg.HealthBlockedFile + ".lock"); !os.IsNotExist(err) {
		t.Fatal("read-only overview acquired a control lock")
	}
}

func TestHumanStatusCLIRejectsExtraFlagsBeforeRequest(t *testing.T) {
	v := humanStatusFixture()
	cfg, calls := fakeStatusAdmin(t, account.AdminResult{OK: true, Status: &v})
	for _, args := range [][]string{{"status", "--json"}, {"status", "--human", "extra"}, {"status", "--human=true"}, {"status", "friend-1"}} {
		if err := admin(cfg, args); err == nil {
			t.Fatalf("unexpected status flags accepted: %v", args)
		}
	}
	if len(calls) != 0 {
		t.Fatal("invalid flags still queried accountd")
	}
}

func TestHumanStatusMissingOrFailedResponseNeverClaimsCompletion(t *testing.T) {
	for _, response := range []account.AdminResult{{OK: true}, {Error: "test accountd status unavailable"}} {
		t.Run(response.Error, func(t *testing.T) {
			cfg, calls := fakeStatusAdmin(t, response)
			body, err := captureStatusOutput(t, func() error { return admin(cfg, []string{"status", "--human"}) })
			if err == nil || body != "" || len(calls) != 1 {
				t.Fatal("unknown/failed status falsely reported completion")
			}
		})
	}
}

func TestHumanStatusUnknownUsageWarningsAndBanProgress(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*account.Status)
	}{
		{"unknown ledger", "账户用量：未知", func(v *account.Status) { v.DataError = "test witness unavailable" }},
		{"unknown guard", "余量不可核验（不是 0）", func(v *account.Status) { v.GuardError = "test stale guard" }},
		{"certificate", "证书异常，接入待核验", func(v *account.Status) { v.CertificateError = "test expired certificate" }},
		{"current statistics", "采集有异常，留意最新采样", func(v *account.Status) { v.StatsError = "test timeout" }},
		{"paused with failed ban", "旧连接踢出 2 待下发 · 1 失败 · 3 已下发/待确认离线", func(v *account.Status) {
			v.Users[1].Disabled = true
			v.Users[1].Pending = 2
			v.Users[1].Failed = 1
			v.Users[1].Issued = 3
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := humanStatusFixture()
			tc.change(&v)
			var b bytes.Buffer
			if err := writeHumanStatus(&b, v); err != nil {
				t.Fatal(err)
			}
			body := b.String()
			if !strings.Contains(body, tc.want) {
				t.Fatalf("overview hid %q", tc.want)
			}
			if v.DataError != "" && (strings.Contains(body, "owner") || strings.Contains(body, "81.00") || strings.Contains(body, "未分配 20.00")) {
				t.Fatal("unknown ledger showed stale users or balances")
			}
			if v.GuardError != "" && strings.Contains(body, "剩余 146.00") {
				t.Fatal("unknown guard displayed old remainder")
			}
		})
	}
}

func TestHumanStatusEmptyAccountsTimesAndOutputFailure(t *testing.T) {
	v := humanStatusFixture()
	v.Users = nil
	var b bytes.Buffer
	if err := writeHumanStatus(&b, v); err != nil || !strings.Contains(b.String(), "尚无账户") || !strings.Contains(b.String(), "尚无可信记录") {
		t.Fatal("empty status not explained")
	}
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	if humanTime(now.Unix(), false) != "2026-10-23T12:00:00Z" || humanTime(now.UnixNano(), true) != "2026-10-23T12:00:00Z" {
		t.Fatal("seconds/nanoseconds display confused")
	}
	v.Meta.Reason = "test reason\n\x1b[2Jfabricated terminal line"
	v.StatsError = "test error\r\x1b[31mred"
	b.Reset()
	if err := writeHumanStatus(&b, v); err != nil || strings.Contains(b.String(), "\x1b") || strings.Contains(b.String(), "\nfabricated terminal line") {
		t.Fatal("diagnostic text could inject terminal control sequences")
	}
	if err := writeHumanStatus(statusFailedWriter{}, v); !errors.Is(err, errStatusOutput) {
		t.Fatal("output failure swallowed")
	}
}

var errStatusOutput = errors.New("test output closed")

type statusFailedWriter struct{}

func (statusFailedWriter) Write([]byte) (int, error) { return 0, errStatusOutput }
