package account

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) (*Store, Config, Secrets, time.Time) {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"db", "witness", "run", "backups"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{Database: root + "/db/account.sqlite", Anchor: root + "/witness/revision.json", SecretsFile: root + "/secrets.json", AuthListen: "127.0.0.1:18081", PageListen: "127.0.0.1:18082", AdminSocket: root + "/run/admin.sock", StatsURL: "http://127.0.0.1:18083", GenerationFile: root + "/run/core.json", GuardStatusFile: root + "/run/guard.json", HealthFile: root + "/run/health.json", HealthBlockedFile: root + "/run/blocked", Certificate: root + "/cert.pem", Server: "rr.example.com", SNI: "rr.example.com", Port: 443, QuotaPoolBytes: 140 * GiB, MaxAccounts: 8, MaxSessionsPerUser: 64, MaxSessionsTotal: 256, BackupDir: root + "/backups"}
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	secrets := Secrets{AuthKey: key, CredentialKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)), StatsSecret: strings.Repeat("s", 43), PageUser: "usage", PagePassword: strings.Repeat("p", 43)}
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	s, err := OpenStore(cfg, secrets, true, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.AddUsers([]NewUser{{Name: "owner", Password: strings.Repeat("o", 32), Quota: 1000000}, {Name: "friend", Password: strings.Repeat("f", 32), Quota: 1000}}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Arm(map[string]int64{"owner": 0, "friend": 0}, "initial verified local test", true, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{}, Online: map[string]int{}}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Activate(now); err != nil {
		t.Fatal(err)
	}
	return s, cfg, secrets, now
}
func session(t *testing.T, s *Store, name string, now time.Time) string {
	t.Helper()
	p := strings.Repeat("o", 32)
	if name == "friend" {
		p = strings.Repeat("f", 32)
	}
	id, err := s.Authenticate(p, "generation-1", now)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func user(t *testing.T, s *Store, name string, now time.Time) UserView {
	t.Helper()
	v, err := s.View(now)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range v.Users {
		if u.Name == name {
			return u
		}
	}
	t.Fatal("missing user")
	return UserView{}
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestUsageCursorAtomicIdempotentAndControllerRestart(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	id1, id2 := session(t, s, "friend", now), session(t, s, "friend", now)
	shot := Snapshot{Traffic: map[string]Counter{id1: {Tx: 10, Rx: 20}, id2: {Tx: 30, Rx: 40}}, Online: map[string]int{id1: 1, id2: 1}}
	must(t, s.Apply("generation-1", shot, now.Add(time.Second)))
	must(t, s.Apply("generation-1", shot, now.Add(2*time.Second)))
	u := user(t, s, "friend", now)
	if u.Tx != 40 || u.Rx != 60 || u.Used != 100 {
		t.Fatalf("wrong totals: %+v", u)
	}
	shot.Traffic[id1] = Counter{Tx: 20, Rx: 50}
	s.beforeCommit = func() error { return errors.New("injected transaction failure") }
	if err := s.Apply("generation-1", shot, now.Add(3*time.Second)); err == nil {
		t.Fatal("transaction failure swallowed")
	}
	s.beforeCommit = nil
	if user(t, s, "friend", now).Used != 100 {
		t.Fatal("failed transaction advanced usage")
	}
	must(t, s.Apply("generation-1", shot, now.Add(4*time.Second)))
	if user(t, s, "friend", now).Used != 140 {
		t.Fatal("retry did not recover cumulative delta")
	}
	must(t, s.Close())
	s2, err := OpenStore(cfg, secrets, false, now.Add(5*time.Second))
	must(t, err)
	defer s2.Close()
	must(t, s2.Apply("generation-1", shot, now.Add(5*time.Second)))
	if user(t, s2, "friend", now).Used != 140 {
		t.Fatal("reopen duplicated/reset usage")
	}
}

func TestSingleAccountDisableTargetsAllSignedSessions(t *testing.T) {
	s, _, _, now := testStore(t)
	owner := session(t, s, "owner", now)
	friends := []string{session(t, s, "friend", now), session(t, s, "friend", now), session(t, s, "friend", now)}
	// Third signed connection has not appeared in /online yet.
	must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{}, Online: map[string]int{owner: 1, friends[0]: 1, friends[1]: 1}}, now.Add(time.Second)))
	must(t, s.ChangeUser("disable", "friend", "", 0, now.Add(2*time.Second)))
	ids, err := s.Pending("generation-1", now.Add(2*time.Second), 128)
	must(t, err)
	if len(ids) != 3 {
		t.Fatalf("lost pre-online session: %v", ids)
	}
	for _, id := range ids {
		if id == owner {
			t.Fatal("other account included")
		}
	}
	if _, err := s.Authenticate(strings.Repeat("f", 32), "generation-1", now.Add(2*time.Second)); !errors.Is(err, ErrDenied) {
		t.Fatal("new authentication not denied")
	}
	if _, err := s.Authenticate(strings.Repeat("o", 32), "generation-1", now.Add(2*time.Second)); err != nil {
		t.Fatal("other account denied")
	}
	must(t, s.MarkAttempt(ids, now.Add(2*time.Second)))
	must(t, s.KickResult(ids, "issued"))
	u := user(t, s, "friend", now)
	if u.Issued != 3 || u.Offline != 0 {
		t.Fatal("HTTP200 misreported as offline")
	}
	must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{}, Online: map[string]int{owner: 1}}, now.Add(3*time.Second)))
	u = user(t, s, "friend", now)
	if u.Offline != 2 || u.Issued != 1 {
		t.Fatalf("unobserved auth prematurely marked offline: %+v", u)
	}
	must(t, s.ChangeUser("enable", "friend", "", 0, now.Add(4*time.Second)))
	newID := session(t, s, "friend", now.Add(4*time.Second))
	for _, id := range friends {
		if newID == id {
			t.Fatal("session ID reused")
		}
	}
}

func TestAuthenticationDisableRaceSerialized(t *testing.T) {
	s, _, _, now := testStore(t)
	var wg sync.WaitGroup
	ids := make(chan string, 32)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := s.Authenticate(strings.Repeat("f", 32), "generation-1", now)
			if err == nil {
				ids <- id
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := s.ChangeUser("disable", "friend", "", 0, now); err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()
	close(ids)
	pending, err := s.Pending("generation-1", now, 128)
	must(t, err)
	got := map[string]bool{}
	for _, id := range pending {
		got[id] = true
	}
	for id := range ids {
		if !got[id] {
			t.Fatal("signed auth escaped concurrent disable")
		}
	}
}

func TestQuotaExhaustionChangesOnlyTargetAndKeepsUsage(t *testing.T) {
	s, _, _, now := testStore(t)
	id := session(t, s, "friend", now)
	owner := session(t, s, "owner", now)
	shot := Snapshot{Traffic: map[string]Counter{id: {Tx: 400, Rx: 601}, owner: {Tx: 2, Rx: 3}}, Online: map[string]int{id: 1, owner: 1}}
	must(t, s.Apply("generation-1", shot, now.Add(time.Second)))
	u := user(t, s, "friend", now)
	if !u.Exhausted || u.Disabled || u.Pending != 1 {
		t.Fatalf("wrong exhaustion state: %+v", u)
	}
	if user(t, s, "owner", now).Pending != 0 {
		t.Fatal("quota banned owner")
	}
	must(t, s.ChangeUser("quota", "friend", "", 2000, now.Add(2*time.Second)))
	if user(t, s, "friend", now).Used != 1001 {
		t.Fatal("quota increase reset usage")
	}
	newID := session(t, s, "friend", now.Add(2*time.Second))
	must(t, s.ChangeUser("quota", "friend", "", 500, now.Add(3*time.Second)))
	pending, err := s.Pending("generation-1", now.Add(3*time.Second), 128)
	must(t, err)
	if len(pending) != 2 || newID == id {
		t.Fatal("lower quota failed to cover new and old sessions")
	}
	if _, err := s.Authenticate(strings.Repeat("f", 32), "generation-1", now.Add(3*time.Second)); err == nil {
		t.Fatal("quota allowed new login")
	}
}

func TestMonthBoundaryAndPermanentDisableRevoke(t *testing.T) {
	s, _, _, now := testStore(t)
	id := session(t, s, "friend", now)
	must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{id: {Tx: 500, Rx: 501}}, Online: map[string]int{id: 1}}, now.Add(time.Second)))
	next := time.Date(2026, 11, 1, 8, 0, 1, 0, time.UTC) // November boundary is still PDT (UTC-7).
	month, _ := billingTime(next)
	if month != "2026-11" {
		t.Fatal(month)
	}
	must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{id: {Tx: 510, Rx: 511}}, Online: map[string]int{}}, next))
	u := user(t, s, "friend", next)
	if u.Used != 20 || u.Exhausted || u.Disabled {
		t.Fatal("soft quota did not roll over")
	}
	if _, err := s.Authenticate(strings.Repeat("f", 32), "generation-1", next); err != nil {
		t.Fatal(err)
	}
	must(t, s.ChangeUser("disable", "friend", "", 0, next))
	later := next.AddDate(0, 1, 0)
	must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{id: {Tx: 510, Rx: 511}}, Online: map[string]int{}}, later))
	if _, err := s.Authenticate(strings.Repeat("f", 32), "generation-1", later); err == nil {
		t.Fatal("manual disable lost across month")
	}
	must(t, s.ChangeUser("revoke", "friend", "", 0, later))
	if err := s.ChangeUser("enable", "friend", "", 0, later); err == nil {
		t.Fatal("revoked re-enabled")
	}
	if err := s.AddUsers([]NewUser{{Name: "friend", Quota: 500}}, later); err == nil {
		t.Fatal("revoked name recreated")
	}
}

func TestCounterAnomaliesPersistReviewWithoutPartialDeltas(t *testing.T) {
	for _, kind := range []string{"rollback", "missing", "unknown", "online-reuse", "generation", "overflow"} {
		t.Run(kind, func(t *testing.T) {
			s, _, _, now := testStore(t)
			id := session(t, s, "friend", now)
			must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{id: {Tx: 10, Rx: 10}}, Online: map[string]int{id: 1}}, now.Add(time.Second)))
			shot := Snapshot{Traffic: map[string]Counter{id: {Tx: 20, Rx: 20}}, Online: map[string]int{id: 1}}
			gen := "generation-1"
			switch kind {
			case "rollback":
				shot.Traffic[id] = Counter{Tx: 5, Rx: 30}
			case "missing":
				delete(shot.Traffic, id)
			case "unknown":
				shot.Traffic["unknown"] = Counter{Tx: 5}
			case "online-reuse":
				shot.Online[id] = 2
			case "generation":
				gen = "generation-2"
				shot.Traffic = map[string]Counter{}
				shot.Online = map[string]int{}
			case "overflow":
				shot.Traffic[id] = Counter{Tx: 1 << 62, Rx: 1 << 62}
			}
			if err := s.Apply(gen, shot, now.Add(2*time.Second)); !errors.Is(err, ErrReview) {
				t.Fatalf("anomaly accepted: %v", err)
			}
			m, err := s.Meta()
			must(t, err)
			if !m.Review || !m.Hold || m.Reason == "" {
				t.Fatal("anomaly not persisted")
			}
			if user(t, s, "friend", now).Used != 20 {
				t.Fatal("anomaly partially committed usage")
			}
			if _, err := s.Authenticate(strings.Repeat("o", 32), gen, now.Add(2*time.Second)); err == nil {
				t.Fatal("anomaly authentication remained open")
			}
		})
	}
}

func TestRestoreWitnessAndRepairValidation(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	must(t, s.Backup(now))
	backups, _ := filepath.Glob(cfg.BackupDir + "/*.sqlite")
	id := session(t, s, "friend", now)
	must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{id: {Tx: 100, Rx: 200}}, Online: map[string]int{id: 1}}, now.Add(time.Second)))
	must(t, s.Close())
	data, err := os.ReadFile(backups[0])
	must(t, err)
	must(t, os.WriteFile(cfg.Database, data, 0600))
	s2, err := OpenStore(cfg, secrets, false, now)
	must(t, err)
	defer s2.Close()
	m, err := s2.Meta()
	if err == nil || !m.Review {
		t.Fatal("old DB backup accepted")
	}
	before, err := os.ReadFile(cfg.Anchor)
	must(t, err)
	if err := s2.Arm(map[string]int64{"owner": 0}, "invalid incomplete charges", true, now); err == nil {
		t.Fatal("incomplete charges accepted")
	}
	after, err := os.ReadFile(cfg.Anchor)
	must(t, err)
	if !bytes.Equal(before, after) {
		t.Fatal("bad repair reset witness")
	}
	if _, err := s2.Authenticate(strings.Repeat("o", 32), "generation-1", now); err == nil {
		t.Fatal("bad repair re-opened auth")
	}
	if _, err := s2.Export("friend"); err == nil {
		t.Fatal("credential export allowed during restore mismatch")
	}
	must(t, s2.Arm(map[string]int64{"owner": 0, "friend": 300}, "conservative recovery charge", true, now))
	m, err = s2.Meta()
	must(t, err)
	if m.Review || !m.Hold {
		t.Fatal("valid repair must stay held")
	}
	must(t, s2.Apply("generation-2", Snapshot{Traffic: map[string]Counter{}, Online: map[string]int{}}, now))
	must(t, s2.Activate(now))
	if user(t, s2, "friend", now).Used != 300 {
		t.Fatal("recovery charge lost")
	}
}

func TestStoreLockMissingDBAndFileReplacement(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	if other, err := OpenStore(cfg, secrets, false, now); err == nil {
		other.Close()
		t.Fatal("two stores opened same DB")
	}
	must(t, os.Rename(cfg.Database, cfg.Database+".moved"))
	if _, err := s.Authenticate(strings.Repeat("o", 32), "generation-1", now); err == nil {
		t.Fatal("unlinked database allowed authentication")
	}
	must(t, s.Close())
	if other, err := OpenStore(cfg, secrets, false, now); err == nil {
		other.Close()
		t.Fatal("runtime auto-created missing DB")
	}
}

func TestRenameRotateTombstonesAndCapacity(t *testing.T) {
	s, _, _, now := testStore(t)
	id := session(t, s, "friend", now)
	must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{id: {Tx: 20, Rx: 30}}, Online: map[string]int{id: 1}}, now.Add(time.Second)))
	must(t, s.ChangeUser("rename", "friend", "renamed", 0, now.Add(2*time.Second)))
	if err := s.AddUsers([]NewUser{{Name: "friend", Quota: 100}}, now); err == nil {
		t.Fatal("historical name reused")
	}
	must(t, s.ChangeUser("rotate", "renamed", "", 0, now.Add(3*time.Second)))
	if err := s.AddUsers([]NewUser{{Name: "new-friend", Password: strings.Repeat("f", 32), Quota: 100}}, now); err == nil {
		t.Fatal("old credential reused under new ledger")
	}
	p, err := s.Export("renamed")
	must(t, err)
	if p == strings.Repeat("f", 32) {
		t.Fatal("rotation kept password")
	}
	if user(t, s, "renamed", now).Used != 50 {
		t.Fatal("rename/rotate reset usage")
	}
	if _, err := s.Authenticate(p, "generation-1", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	// Rate/capacity fail only the offending account, never restart the core.
	for i := 0; i < 12; i++ {
		_, _ = s.Authenticate(p, "generation-1", now.Add(3*time.Second))
	}
	if _, err := s.Authenticate(p, "generation-1", now.Add(3*time.Second)); err == nil {
		t.Fatal("per-account rate cap ignored")
	}
	if _, err := s.Authenticate(strings.Repeat("o", 32), "generation-1", now.Add(3*time.Second)); err != nil {
		t.Fatal("other account affected by account cap")
	}
}

type fakeAPI struct {
	shot             Snapshot
	readErr, kickErr error
	calls            [][]string
}

func (f *fakeAPI) Read(context.Context) (Snapshot, error) { return f.shot, f.readErr }
func (f *fakeAPI) Kick(_ context.Context, ids []string) error {
	f.calls = append(f.calls, append([]string{}, ids...))
	return f.kickErr
}
func TestKickBoundedFailurePersistsAndDoesNotStopOthers(t *testing.T) {
	s, _, _, now := testStore(t)
	friend := session(t, s, "friend", now)
	owner := session(t, s, "owner", now)
	must(t, s.ChangeUser("disable", "friend", "", 0, now))
	f := &fakeAPI{shot: Snapshot{Traffic: map[string]Counter{}, Online: map[string]int{friend: 1, owner: 1}}, kickErr: errors.New("simulated timeout")}
	c := &Collector{Store: s, API: f, Generation: func() (string, error) { return "generation-1", nil }}
	for i := 0; i < 8; i++ {
		_ = c.Tick(context.Background(), now.Add(time.Duration(i)*5*time.Second))
	}
	if len(f.calls) != 3 {
		t.Fatalf("unbounded retries: %d", len(f.calls))
	}
	for _, call := range f.calls {
		if len(call) != 1 || call[0] != friend {
			t.Fatal("kick affected other session")
		}
	}
	u := user(t, s, "friend", now)
	if !u.Disabled || u.Failed != 1 {
		t.Fatal("failed ban reported success/reset")
	}
	m, err := s.Meta()
	must(t, err)
	if m.Review || m.Hold {
		t.Fatal("individual ban failure became shared outage")
	}
	if _, err := s.Authenticate(strings.Repeat("o", 32), "generation-1", now.Add(35*time.Second)); err != nil {
		t.Fatal(err)
	}
	must(t, s.RetryBan("friend", now.Add(40*time.Second)))
	f.kickErr = nil
	must(t, c.Enforce(context.Background(), now.Add(40*time.Second)))
	if user(t, s, "friend", now).Issued != 1 {
		t.Fatal("explicit retry did not issue ban")
	}
}

func TestMaintenanceSampleAndArmWindow(t *testing.T) {
	s, _, _, now := testStore(t)
	id := session(t, s, "friend", now)
	must(t, s.BeginMaintenance(now))
	must(t, s.applyHeld("generation-1", Snapshot{Traffic: map[string]Counter{id: {Tx: 10, Rx: 20}}, Online: map[string]int{id: 1}}, now.Add(time.Second)))
	must(t, s.Hold("planned maintenance verified sample", now.Add(time.Second)))
	if user(t, s, "friend", now).Used != 30 {
		t.Fatal("final sample lost")
	}
	must(t, s.Arm(map[string]int64{"friend": 4, "owner": 0}, "reviewed residual in-flight bytes", true, now.Add(2*time.Second)))
	if err := s.Apply("generation-2", Snapshot{Traffic: map[string]Counter{}, Online: map[string]int{}}, now.Add(time.Minute)); err == nil {
		t.Fatal("expired arm permitted new generation")
	}
}

func TestHTTPPageReadOnlyAuthAndRequestBounds(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	c := &Collector{Store: s, API: &fakeAPI{}, Generation: func() (string, error) { return "generation-1", nil }}
	d := NewDaemon(s, c, cfg, secrets)
	must(t, d.publish(now))
	for _, path := range []string{"/", "/api/status"} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		d.PageHandler(w, r)
		if w.Code != 401 {
			t.Fatal("page lacks independent auth")
		}
		r.SetBasicAuth(secrets.PageUser, secrets.PagePassword)
		w = httptest.NewRecorder()
		d.PageHandler(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		for _, p := range []string{strings.Repeat("f", 32), secrets.AuthKey, secrets.CredentialKey, secrets.StatsSecret, "hysteria2://"} {
			if strings.Contains(w.Body.String(), p) {
				t.Fatal("page leaked credential")
			}
		}
		if w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing page security headers")
		}
	}
	w := httptest.NewRecorder()
	d.PageHandler(w, httptest.NewRequest("POST", "/api/status", nil))
	if w.Code != 404 {
		t.Fatal("page offers write endpoint")
	}
	w = httptest.NewRecorder()
	d.AdminHandler(w, httptest.NewRequest("POST", "/admin", strings.NewReader(`{"action":"disable","name":"friend"}`)))
	if w.Code != 403 {
		t.Fatal("admin not root-peer protected")
	}
	w = httptest.NewRecorder()
	d.AuthHandler(w, httptest.NewRequest("POST", "/auth", strings.NewReader(strings.Repeat("x", MaxBody+1))))
	if w.Code != 400 {
		t.Fatal("oversized auth accepted")
	}
	if !eFresh(Meta{LastSample: now.UnixNano()}, now.Add(29*time.Second)) || eFresh(Meta{LastSample: now.UnixNano()}, now.Add(31*time.Second)) {
		t.Fatal("shared-stat timeout wrong")
	}
}

func TestAPIRejectsClearRedirectsNullAndLimits(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.String())
		if r.Header.Get("Authorization") != "independent-key" {
			t.Error("stats key missing")
		}
		if r.URL.Path == "/traffic" {
			_, _ = ioString(w, `{"sid":{"tx":1,"rx":2}}`)
		} else {
			_, _ = ioString(w, `{"sid":1}`)
		}
	}))
	defer srv.Close()
	api := NewHTTPAPI(srv.URL, "independent-key")
	shot, err := api.Read(context.Background())
	must(t, err)
	if shot.Traffic["sid"].Tx != 1 || shot.Traffic["sid"].Rx != 2 {
		t.Fatal("tx/rx direction changed")
	}
	for _, p := range paths {
		if strings.Contains(p, "clear") {
			t.Fatal("read cleared upstream counters")
		}
	}
	for _, body := range []string{`null`, `{"sid":{"tx":-1,"rx":2}}`, strings.Repeat("x", MaxStatsBody+1)} {
		srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = ioString(w, body) })
		_, err = api.Read(context.Background())
		if body == `null` && err == nil {
			t.Fatal("null accepted")
		}
		if len(body) > MaxStatsBody && err == nil {
			t.Fatal("response limit ignored")
		}
	}
}
func ioString(w http.ResponseWriter, s string) (int, error) { return w.Write([]byte(s)) }

func TestGuardAndSignedHealthWitness(t *testing.T) {
	_, cfg, secrets, now := testStore(t)
	month, _ := billingTime(now)
	g := GuardStatus{Month: month, Limit: 180 * GiB, Warn: 150 * GiB, Healthy: true, Updated: now.Unix(), Services: []string{"sing-box", "hysteria-server"}}
	must(t, writeJSON(cfg.GuardStatusFile, g, 0644))
	_, err := ReadGuard(cfg.GuardStatusFile, now)
	must(t, err)
	g.Services = []string{"sing-box"}
	must(t, writeJSON(cfg.GuardStatusFile, g, 0644))
	if _, err := ReadGuard(cfg.GuardStatusFile, now); err == nil {
		t.Fatal("HY2 missing global coverage accepted")
	}
	key, _ := base64.StdEncoding.DecodeString(secrets.AuthKey)
	h := Health{Updated: now.Unix(), LastSample: now.UnixNano(), Healthy: true}
	h.MAC = healthMAC(h, key)
	must(t, writeJSON(cfg.HealthFile, h, 0600))
	_, err = VerifyHealth(cfg.HealthFile, key, now)
	must(t, err)
	if _, err := VerifyHealth(cfg.HealthFile, key, now.Add(16*time.Second)); err == nil {
		t.Fatal("dead/hung accountd witness accepted")
	}
	h.Review = true
	must(t, writeJSON(cfg.HealthFile, h, 0600))
	if _, err := VerifyHealth(cfg.HealthFile, key, now); err == nil {
		t.Fatal("tampered health witness accepted")
	}
}

func TestPoolDuplicatesAndPrivateExport(t *testing.T) {
	s, cfg, _, now := testStore(t)
	if err := s.AddUsers([]NewUser{{Name: "duplicate", Password: strings.Repeat("f", 32), Quota: 10}}, now); err == nil {
		t.Fatal("shared password accepted")
	}
	if err := s.AddUsers([]NewUser{{Name: "big", Quota: 140 * GiB}}, now); err == nil {
		t.Fatal("quota pool overallocated")
	}
	p, err := s.Export("friend")
	must(t, err)
	paths, err := ExportFiles(cfg, "friend", p, filepath.Join(t.TempDir(), "exports"))
	must(t, err)
	for _, p := range paths {
		st, err := os.Stat(p)
		must(t, err)
		if st.Mode().Perm() != 0600 {
			t.Fatal("export permissions")
		}
	}
	var outbound map[string]any
	b, err := os.ReadFile(paths[0])
	must(t, err)
	must(t, json.Unmarshal(b, &outbound))
	if outbound["network"] != "tcp" || outbound["tls"].(map[string]any)["insecure"] != false {
		t.Fatal("unsafe client export")
	}
	// Store contains keyed digests and authenticated encryption, not plaintext.
	var enc []byte
	must(t, s.db.QueryRow(`SELECT credential FROM accounts WHERE name='friend'`).Scan(&enc))
	if bytes.Contains(enc, []byte(p)) {
		t.Fatal("credential stored plaintext")
	}
}
