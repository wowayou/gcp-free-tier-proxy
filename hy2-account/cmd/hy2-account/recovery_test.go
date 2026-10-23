package main

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"example.com/gcp-free-tier-proxy/hy2-account/internal/account"
)

func TestArmCLIControlsHealthFromStoppedCheckThroughLatchClear(t *testing.T) {
	for _, response := range []string{`{"ok":true}`, `{"ok":false,"error":"test post-commit error"}`} {
		t.Run(response, func(t *testing.T) {
			dir := t.TempDir()
			cfg := account.Config{AdminSocket: filepath.Join(dir, "admin.sock"), HealthBlockedFile: filepath.Join(dir, "blocked")}
			if err := os.WriteFile(cfg.HealthBlockedFile, []byte("held"), 0600); err != nil {
				t.Fatal(err)
			}
			// This executable is the only systemctl visible to the real CLI.
			// Check that both its stopped probe and reload run under the lock.
			script := `#!/bin/sh
if flock -n "$TEST_CONTROL_LOCK" true; then exit 77; fi
case "$1" in
  show) printf 'inactive\n' ;;
  daemon-reload) [ ! -e "$TEST_LATCH" ] || exit 78; printf 'reload\n' >>"$TEST_CALLS" ;;
  *) exit 99 ;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
			t.Setenv("TEST_CONTROL_LOCK", cfg.HealthBlockedFile+".lock")
			t.Setenv("TEST_LATCH", cfg.HealthBlockedFile)
			t.Setenv("TEST_CALLS", filepath.Join(dir, "calls"))
			listener, err := net.Listen("unix", cfg.AdminSocket)
			if err != nil {
				t.Fatal(err)
			}
			requests := make(chan account.AdminRequest, 1)
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				control, err := account.LockHealthControl(cfg, 0)
				if control != nil {
					control.Close()
				}
				if !errors.Is(err, account.ErrHealthControlBusy) {
					t.Error("arm request was sent without the health control lock")
				}
				var req account.AdminRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				requests <- req
				_, _ = w.Write([]byte(response))
			})}
			go func() { _ = server.Serve(listener) }()
			t.Cleanup(func() { server.Close() })
			charges := privateFile(t, `{"owner":1073741824}`)
			err = admin(cfg, []string{"system", "arm", "--charges-file", charges, "--reason", "test reviewed increment", "--accept-gap", "--expected-month", "2026-10"})
			if strings.Contains(response, `"ok":true`) {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(cfg.HealthBlockedFile); !os.IsNotExist(err) {
					t.Fatal("successful arm did not clear its own latch")
				}
				if data, err := os.ReadFile(filepath.Join(dir, "calls")); err != nil || string(data) != "reload\n" {
					t.Fatal("reload was not performed once under the control lock")
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "post-commit") {
					t.Fatal("ambiguous arm failure was hidden")
				}
				if _, err := os.Stat(cfg.HealthBlockedFile); err != nil {
					t.Fatal("failed arm removed the safety latch")
				}
				if _, err := os.Stat(filepath.Join(dir, "calls")); !os.IsNotExist(err) {
					t.Fatal("failed arm reached daemon-reload")
				}
			}
			req := <-requests
			if req.Action != "arm" || req.ExpectedMonth != "2026-10" || req.Charges["owner"] != 1<<30 {
				t.Fatalf("recovery intent not forwarded: %+v", req)
			}
			control, err := account.LockHealthControl(cfg, 0)
			if err != nil {
				t.Fatal("CLI leaked its health control lock", err)
			}
			control.Close()
		})
	}
}
