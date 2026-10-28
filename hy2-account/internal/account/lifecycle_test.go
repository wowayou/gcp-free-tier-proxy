package account

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func healthyGuard(t *testing.T, cfg Config, now time.Time, blocked bool) {
	t.Helper()
	month, _ := billingTime(now)
	must(t, writeJSON(cfg.GuardStatusFile, GuardStatus{Month: month, Limit: 180 * GiB, Warn: 150 * GiB, Healthy: true, Blocked: blocked, Updated: now.Unix(), Services: []string{"sing-box", "hysteria-server"}}, 0644))
}

func TestPersonalCommandNeverEnforcesAnotherAccountsPendingBan(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	owner := session(t, s, "owner", now)
	friend := session(t, s, "friend", now)
	must(t, s.ChangeUser("disable", "owner", "", 0, now))
	api := &fakeAPI{kickErr: errors.New("bounded test failure")}
	collector := &Collector{Store: s, API: api, Generation: func() (string, error) { return "generation-1", nil }}
	d := NewDaemon(s, collector, cfg, secrets)
	result := d.Admin(context.Background(), AdminRequest{Action: "disable", Name: "friend"}, now)
	if result.OK || !result.Incomplete || len(api.calls) != 1 || len(api.calls[0]) != 1 || api.calls[0][0] != friend {
		t.Fatalf("personal ban was not target-scoped: %+v calls=%v", result, api.calls)
	}
	if user(t, s, "owner", now).Pending != 1 || owner == friend {
		t.Fatal("unrelated pending task consumed by friend's command")
	}
	m, err := s.Meta()
	must(t, err)
	if m.Review || m.Hold {
		t.Fatal("individual /kick failure became a shared outage")
	}
}

func TestRetryBanRejectsUnknownAccount(t *testing.T) {
	s, _, _, now := testStore(t)
	if err := s.RetryBan("missing", now); err == nil {
		t.Fatal("unknown account retry was silently accepted")
	}
}

func TestKickRetryBudgetSurvivesControllerReopen(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	id := session(t, s, "friend", now)
	must(t, s.ChangeUser("disable", "friend", "", 0, now))
	api := &fakeAPI{kickErr: errors.New("timeout")}
	c := &Collector{Store: s, API: api, Generation: func() (string, error) { return "generation-1", nil }}
	for i := 0; i < 2; i++ {
		if c.EnforceUser(context.Background(), now.Add(time.Duration(i)*5*time.Second), "friend") == nil {
			t.Fatal("failure hidden")
		}
	}
	must(t, s.Close())
	s2, err := OpenStore(cfg, secrets, false, now)
	must(t, err)
	defer s2.Close()
	c.Store = s2
	for i := 2; i < 8; i++ {
		_ = c.EnforceUser(context.Background(), now.Add(time.Duration(i)*5*time.Second), "friend")
	}
	if len(api.calls) != 3 || api.calls[2][0] != id || user(t, s2, "friend", now).Failed != 1 {
		t.Fatal("restart reset finite retry budget or false success reported")
	}
}

func TestConsumedQuotaCannotBeReallocatedThroughRevokeOrReduction(t *testing.T) {
	for _, op := range []string{"quota", "revoke"} {
		t.Run(op, func(t *testing.T) {
			s, _, _, now := testStore(t)
			s.cfg.QuotaPoolBytes = 1002000 // owner reserves 1,000,000; friend has used 900.
			id := session(t, s, "friend", now)
			must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{id: {Tx: 400, Rx: 500}}, Online: map[string]int{id: 1}}, now.Add(time.Second)))
			must(t, s.ChangeUser(op, "friend", "", 100, now.Add(2*time.Second)))
			if err := s.AddUsers([]NewUser{{Name: "another", Quota: 1500}}, now); err == nil {
				t.Fatal("consumed retired/reduced quota reused inside the same monthly pool")
			}
			if user(t, s, "friend", now).Used != 900 {
				t.Fatal("consumed usage erased")
			}
		})
	}
}

func TestSessionCapacityPersistsAndOnlyRejectsOffendingAccount(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	s.cfg.MaxSessionsPerUser = 8
	for i := 0; i < 8; i++ {
		_ = session(t, s, "friend", now)
	}
	later := now.Add(61 * time.Second)
	must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{}, Online: map[string]int{}}, later))
	if _, err := s.Authenticate(strings.Repeat("f", 32), "generation-1", later); !errors.Is(err, ErrDenied) {
		t.Fatal("freshly issued live cap was only a minute rate limit")
	}
	if _, err := s.Authenticate(strings.Repeat("o", 32), "generation-1", later); err != nil {
		t.Fatal("account capacity denied another account")
	}
	must(t, s.Close())
	cfg.MaxSessionsPerUser = 8
	s2, err := OpenStore(cfg, secrets, false, later)
	must(t, err)
	defer s2.Close()
	if _, err := s2.Authenticate(strings.Repeat("f", 32), "generation-1", later); err == nil {
		t.Fatal("controller reopen forgot freshly issued session capacity")
	}
	u := user(t, s2, "friend", later)
	if u.Sessions != 8 || u.ActiveSessions != 8 || !strings.Contains(u.Status, "活跃连接数达到上限/拒绝新连接") {
		t.Fatal("capacity boundary is not visible")
	}
}

func TestEndOfCollectionClockAndLosAngelesMonthBoundary(t *testing.T) {
	s, _, _, now := testStore(t)
	id := session(t, s, "friend", now)
	start := time.Date(2026, 11, 1, 6, 59, 59, 0, time.UTC) // Oct 31 23:59:59 PDT.
	must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{id: {Tx: 10, Rx: 20}}, Online: map[string]int{id: 1}}, start))
	end := start.Add(2 * time.Second)
	api := &fakeAPI{shot: Snapshot{Traffic: map[string]Counter{id: {Tx: 7, Rx: 11}}, Online: map[string]int{id: 1}}}
	c := &Collector{Store: s, API: api, Generation: func() (string, error) { return "generation-1", nil }, Clock: func() time.Time { return end }}
	must(t, c.Collect(context.Background(), start))
	v, err := s.View(end)
	must(t, err)
	if v.Month != "2026-11" || user(t, s, "friend", end).Used != 18 || v.Meta.LastSample != end.UnixNano() {
		t.Fatal("boundary delta not attributed to sampling-end billing month")
	}
	if user(t, s, "friend", start).Used != 30 {
		t.Fatal("prior month history erased")
	}
}

func TestGlobalOrSharedHealthBlockCannotBeBypassedByUserEnable(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	c := &Collector{Store: s, API: &fakeAPI{}, Generation: func() (string, error) { return "generation-1", nil }}
	d := NewDaemon(s, c, cfg, secrets)
	must(t, s.ChangeUser("disable", "friend", "", 0, now))
	healthyGuard(t, cfg, now, true)
	if d.Admin(context.Background(), AdminRequest{Action: "enable", Name: "friend"}, now).OK {
		t.Fatal("user enable crossed global block")
	}
	healthyGuard(t, cfg, now, false)
	must(t, AtomicFile(cfg.HealthBlockedFile, []byte("held"), 0600))
	if d.Admin(context.Background(), AdminRequest{Action: "enable", Name: "friend"}, now).OK {
		t.Fatal("user enable crossed independent shared-health latch")
	}
	if !user(t, s, "friend", now).Disabled {
		t.Fatal("blocked enable mutated manual disable")
	}
	// Runtime auth must also deny a fresh, otherwise-allowed owner connection.
	actual := time.Now()
	must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{}, Online: map[string]int{}}, actual))
	healthyGuard(t, cfg, actual, false)
	r := httptest.NewRequest("POST", "/auth", strings.NewReader(`{"auth":"`+strings.Repeat("o", 32)+`","addr":"127.0.0.1:1234","tx":0}`))
	w := httptest.NewRecorder()
	d.AuthHandler(w, r)
	if strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatal("auth bypassed root-owned shared-health latch")
	}
}

func TestPostCommitWitnessFailureRetainsUsageAndFailsClosed(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	id := session(t, s, "friend", now)
	oldWitness, err := os.ReadFile(cfg.Anchor)
	must(t, err)
	s.beforeCommit = func() error {
		if err := os.Remove(cfg.Anchor); err != nil {
			return err
		}
		return os.Mkdir(cfg.Anchor, 0700) // Read was valid; post-commit rename will fail.
	}
	if err := s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{id: {Tx: 10, Rx: 20}}, Online: map[string]int{id: 1}}, now.Add(time.Second)); err == nil {
		t.Fatal("post-commit witness failure hidden")
	}
	s.beforeCommit = nil
	if user(t, s, "friend", now).Used != 30 {
		t.Fatal("SQLite commit incorrectly described as rolled back")
	}
	if _, err := s.Authenticate(strings.Repeat("o", 32), "generation-1", now); err == nil {
		t.Fatal("witness failure left auth open")
	}
	must(t, s.Close())
	must(t, os.Remove(cfg.Anchor))
	must(t, os.WriteFile(cfg.Anchor, oldWitness, 0600))
	s2, err := OpenStore(cfg, secrets, false, now)
	must(t, err)
	defer s2.Close()
	if _, err := s2.Meta(); err == nil {
		t.Fatal("interrupted durable commit accepted on restart")
	}
	must(t, s2.Arm(map[string]int64{"friend": 0, "owner": 0}, "verified durable usage already present", true, now))
	if user(t, s2, "friend", now).Used != 30 {
		t.Fatal("explicit witness repair erased already committed usage")
	}
}

func TestPageShowsStorageFailureNotOldGreenCache(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	healthyGuard(t, cfg, now, false)
	c := &Collector{Store: s, API: &fakeAPI{}, Generation: func() (string, error) { return "generation-1", nil }}
	d := NewDaemon(s, c, cfg, secrets)
	must(t, d.publish(now))
	must(t, s.Close())
	must(t, d.publish(now.Add(time.Second)))
	r := httptest.NewRequest("GET", "/api/status", nil)
	r.SetBasicAuth(secrets.PageUser, secrets.PagePassword)
	w := httptest.NewRecorder()
	d.PageHandler(w, r)
	var v Status
	must(t, json.Unmarshal(w.Body.Bytes(), &v))
	if v.Healthy || v.AuthAllowed || v.DataError == "" {
		t.Fatal("cached page hid storage failure")
	}
}

func TestCounterJSONRequiresBothFieldsAndNoNegativeOrOverflow(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `{"tx":1}`, `{"tx":1,"rx":null}`, `{"tx":-1,"rx":2}`, `{"tx":9223372036854775808,"rx":2}`, `{"tx":1,"rx":2,"extra":3}`} {
		var n Counter
		if json.Unmarshal([]byte(body), &n) == nil {
			t.Fatalf("invalid delta record accepted: %s", body)
		}
	}
	var n Counter
	must(t, json.Unmarshal([]byte(`{"tx":0,"rx":0}`), &n))
}

func TestSignedHealthRejectsStaleSampleAndExpiredBootstrap(t *testing.T) {
	_, cfg, _, now := testStore(t)
	key := bytes.Repeat([]byte{1}, 32)
	for _, h := range []Health{
		{Updated: now.Unix(), LastSample: now.Add(-31 * time.Second).UnixNano(), Healthy: true},
		{Updated: now.Unix(), LastSample: now.UnixNano(), Healthy: true, Review: true},
		{Updated: now.Unix(), Bootstrap: true, Hold: true, ArmUntil: now.Add(-time.Second).Unix()},
	} {
		h.MAC = healthMAC(h, key)
		must(t, writeJSON(cfg.HealthFile, h, 0600))
		if _, err := VerifyHealth(cfg.HealthFile, key, now); err == nil {
			t.Fatal("signed but stale/inconsistent health allowed core")
		}
	}
}

func mockSystemctl(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$MOCK_SYSTEMCTL_CALLS"
case "$1" in
  stop) [ "${MOCK_STOP_FAIL:-}" != yes ] ;;
  show) case "$3" in
    --property=ActiveState) printf '%s\n' "${MOCK_ACTIVE_STATE:-inactive}" ;;
    --property=LoadState) printf '%s\n' "${MOCK_LOAD_STATE:-masked}" ;;
  esac ;;
  daemon-reload) exit 0 ;;
  *) exit 88 ;;
esac
`
	must(t, os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0755))
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("MOCK_SYSTEMCTL_CALLS", calls)
	return calls
}

func TestIndependentHealthShutdownIsPersistentVerifiedAndHY2Only(t *testing.T) {
	for _, failure := range []string{"stale", "stop", "mask", "latch"} {
		t.Run(failure, func(t *testing.T) {
			_, cfg, secrets, _ := testStore(t)
			now := time.Now()
			healthyGuard(t, cfg, now, false)
			calls := mockSystemctl(t)
			if failure == "stop" {
				t.Setenv("MOCK_STOP_FAIL", "yes")
				t.Setenv("MOCK_ACTIVE_STATE", "active")
			}
			if failure == "mask" {
				t.Setenv("MOCK_LOAD_STATE", "loaded")
			}
			if failure == "latch" {
				must(t, os.Mkdir(cfg.HealthBlockedFile, 0700))
			}
			if err := HealthCheck(cfg, secrets, "run"); err == nil {
				t.Fatal("shared failure reported healthy")
			}
			body, err := os.ReadFile(calls)
			must(t, err)
			if !strings.Contains(string(body), "stop hysteria-server.service") || !strings.Contains(string(body), "--property=LoadState") || strings.Contains(string(body), "sing-box") || strings.Contains(string(body), "restart") {
				t.Fatal("shared failure skipped shutdown verification or touched backup service")
			}
			if _, err := os.Lstat(cfg.HealthBlockedFile); err != nil {
				t.Fatal("shared-health block not retained")
			}
		})
	}
}

func TestConfigRejectsCollidingPathsKeysAndPublicListeners(t *testing.T) {
	_, cfg, secrets, _ := testStore(t)
	must(t, cfg.Validate())
	for _, mutate := range []func(*Config){
		func(c *Config) { c.PageListen = "0.0.0.0:18082" },
		func(c *Config) { c.StatsURL = "http://localhost:18083" },
		func(c *Config) { c.HealthFile = c.Database },
		func(c *Config) { c.Anchor = filepath.Join(c.BackupDir, "sub/revision.json") },
	} {
		c := cfg
		mutate(&c)
		if c.Validate() == nil {
			t.Fatal("unsafe config accepted")
		}
	}
	secrets.CredentialKey = secrets.AuthKey
	must(t, writeJSON(cfg.SecretsFile, secrets, 0600))
	configPath := filepath.Join(filepath.Dir(cfg.SecretsFile), "config.json")
	must(t, writeJSON(configPath, cfg, 0600))
	if _, _, err := LoadConfig(configPath); err == nil {
		t.Fatal("key reuse accepted")
	}
}
