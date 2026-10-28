package account

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func emptySnapshot() Snapshot {
	return Snapshot{Traffic: map[string]Counter{}, Online: map[string]int{}}
}

func TestSequentialReconnectsReclaimCapacityAndRows(t *testing.T) {
	s, _, _, now := testStore(t)
	s.cfg.MaxSessionsPerUser = 8
	for i := 0; i < 50; i++ {
		at := now.Add(time.Duration(i) * 61 * time.Second)
		must(t, s.Apply("generation-1", emptySnapshot(), at))
		id := session(t, s, "owner", at)
		must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{id: {Tx: 1, Rx: 2}}, Online: map[string]int{id: 1}}, at.Add(time.Second)))
		must(t, s.Apply("generation-1", emptySnapshot(), at.Add(2*time.Second)))
		var rows int
		must(t, s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&rows))
		if rows > 16 {
			t.Fatalf("retention grew with lifetime reconnects: %d", rows)
		}
		if u := user(t, s, "owner", at); u.ActiveSessions != 0 || u.Sessions != int64(i+1) {
			t.Fatalf("disconnected session occupies cap: %+v", u)
		}
	}
	end := now.Add(2 * time.Hour)
	must(t, s.Apply("generation-1", emptySnapshot(), end))
	var rows int
	must(t, s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&rows))
	if rows != 0 || user(t, s, "owner", end).Used != 150 {
		t.Fatal("reclamation erased usage or retained dead rows")
	}
	_ = session(t, s, "owner", end)
}

func TestLiveCapIsolationOfflineReleaseAndFreshAuthExpiry(t *testing.T) {
	s, _, _, now := testStore(t)
	s.cfg.MaxSessionsPerUser = 8
	online := map[string]int{}
	for i := 0; i < 8; i++ {
		online[session(t, s, "friend", now)] = 1
	}
	later := now.Add(61 * time.Second)
	must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{}, Online: online}, later))
	if _, err := s.Authenticate(strings.Repeat("f", 32), "generation-1", later); !errors.Is(err, ErrDenied) {
		t.Fatal("live cap ignored")
	}
	_ = session(t, s, "owner", later)
	for id := range online {
		delete(online, id)
		break
	}
	must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{}, Online: online}, later.Add(time.Second)))
	if u := user(t, s, "friend", later); u.ActiveSessions != 7 || u.Sessions != 8 || !strings.Contains(u.Status, "活跃连接数预警") {
		t.Fatalf("wrong active warning: %+v", u)
	}
	_ = session(t, s, "friend", later.Add(time.Second))
	if u := user(t, s, "friend", later.Add(time.Second)); u.ActiveSessions != 8 || u.Sessions != 9 || !strings.Contains(u.Status, "活跃连接数达到上限/拒绝新连接") {
		t.Fatalf("fresh auth missing from cap: %+v", u)
	}
	must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{}, Online: online}, later.Add(121*time.Second)))
	if u := user(t, s, "friend", later.Add(121*time.Second)); u.ActiveSessions != 7 || u.Sessions != 9 {
		t.Fatal("never-online auth did not expire at two minutes")
	}
	_ = session(t, s, "friend", later.Add(121*time.Second))
}

func TestTotalLiveCapAndTwoMinuteBoundary(t *testing.T) {
	s, _, _, now := testStore(t)
	s.cfg.MaxSessionsPerUser, s.cfg.MaxSessionsTotal = 8, 8
	for i := 0; i < 8; i++ {
		_ = session(t, s, "friend", now)
	}
	at := now.Add(119 * time.Second)
	must(t, s.Apply("generation-1", emptySnapshot(), at))
	if _, err := s.Authenticate(strings.Repeat("o", 32), "generation-1", at); !errors.Is(err, ErrDenied) {
		t.Fatal("total fresh cap ignored")
	}
	at = now.Add(2 * time.Minute)
	must(t, s.Apply("generation-1", emptySnapshot(), at))
	if u := user(t, s, "friend", at); u.ActiveSessions != 0 || u.Sessions != 8 {
		t.Fatal("exact two-minute active boundary changed")
	}
	_ = session(t, s, "owner", at)
	_ = session(t, s, "friend", at)
}

func TestDeltaSumsSessionsDaysAndMonthAndAllowsMissingIDs(t *testing.T) {
	s, _, _, now := testStore(t)
	id := session(t, s, "owner", now)
	for i, n := range []Counter{{Tx: 10, Rx: 20}, {Tx: 3, Rx: 4}, {Tx: 0, Rx: 1}} {
		must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{id: n}, Online: map[string]int{id: 1}}, now.Add(time.Duration(i+1)*time.Second)))
	}
	must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{}, Online: map[string]int{id: 1}}, now.Add(4*time.Second)))
	var tx, rx int64
	var has bool
	must(t, s.db.QueryRow(`SELECT tx,rx,has_counter FROM sessions WHERE id=?`, id).Scan(&tx, &rx, &has))
	if tx != 13 || rx != 25 || !has {
		t.Fatal("session deltas not summed")
	}
	must(t, s.db.QueryRow(`SELECT SUM(tx),SUM(rx) FROM days`).Scan(&tx, &rx))
	if tx != 13 || rx != 25 || user(t, s, "owner", now).Used != 38 {
		t.Fatal("day/month deltas not summed")
	}
}

func assertMarker(t *testing.T, cfg Config, exists bool) {
	t.Helper()
	st, err := os.Lstat(cfg.Database + ".pull")
	if exists {
		must(t, err)
		if !st.Mode().IsRegular() || st.Mode().Perm() != 0600 {
			t.Fatal("pull marker is not private/regular")
		}
	} else if !os.IsNotExist(err) {
		t.Fatalf("marker not removed: %v", err)
	}
}

func assertReviewClosed(t *testing.T, s *Store, now time.Time) {
	t.Helper()
	m, err := s.Meta()
	must(t, err)
	if !m.Review || !m.Hold || m.Reason == "" {
		t.Fatalf("loss not held for review: %+v", m)
	}
	if _, err := s.Authenticate(strings.Repeat("o", 32), "generation-1", now); err == nil {
		t.Fatal("uncertain delta left auth open")
	}
}

func TestPullMarkerNormalAndHeldSamples(t *testing.T) {
	for _, held := range []bool{false, true} {
		for _, kind := range []string{"success", "online-error", "not-sent", "post-send", "existing", "generation-change", "apply-error"} {
			t.Run(fmt.Sprintf("held=%v/%s", held, kind), func(t *testing.T) {
				s, cfg, _, now := testStore(t)
				id := session(t, s, "owner", now)
				if held {
					must(t, s.BeginMaintenance(now))
				}
				api := &fakeAPI{shot: Snapshot{Traffic: map[string]Counter{id: {Tx: 2, Rx: 3}}, Online: map[string]int{id: 1}}}
				genCalls := 0
				c := &Collector{Store: s, API: api, Generation: func() (string, error) {
					genCalls++
					if kind == "generation-change" && genCalls > 1 {
						return "generation-2", nil
					}
					return "generation-1", nil
				}}
				switch kind {
				case "online-error":
					api.readErr = errors.New("online unavailable")
				case "not-sent":
					api.trafficErr = fmt.Errorf("dial fixture: %w", ErrNotSent)
				case "post-send":
					api.trafficErr = errors.New("response lost after clear")
				case "existing":
					must(t, createPullMarker(cfg.Database+".pull"))
				case "apply-error":
					s.beforeCommit = func() error { s.beforeCommit = nil; return errors.New("SQLite commit fixture failure") }
				}
				var err error
				if held {
					err = c.CollectHeld(context.Background(), now.Add(time.Second))
				} else {
					err = c.Collect(context.Background(), now.Add(time.Second))
				}
				switch kind {
				case "success":
					must(t, err)
					if user(t, s, "owner", now).Used != 5 {
						t.Fatal("delta was not committed")
					}
				case "online-error", "not-sent":
					if err == nil || errors.Is(err, ErrReview) {
						t.Fatalf("safe failure became review: %v", err)
					}
					m, e := s.Meta()
					must(t, e)
					if m.Review || m.Hold != held {
						t.Fatal("safe failure changed review/hold")
					}
					if kind == "online-error" && api.trafficReads != 0 {
						t.Fatal("clear sent after online failure")
					}
				default:
					if !errors.Is(err, ErrReview) {
						t.Fatalf("uncertain pull accepted: %v", err)
					}
					assertReviewClosed(t, s, now.Add(time.Second))
					m, e := s.Meta()
					must(t, e)
					if m.Reason != interruptedPull {
						t.Fatal("missing interrupted delta reason")
					}
					if user(t, s, "owner", now).Used != 0 {
						t.Fatal("uncertain pull committed partial usage")
					}
					if kind == "existing" && (api.onlineReads != 0 || api.trafficReads != 0) {
						t.Fatal("pending marker allowed another clear")
					}
				}
				assertMarker(t, cfg, false)
			})
		}
	}
}

func TestMarkerSurvivesFailedReviewAndControllerReopen(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	api := &fakeAPI{shot: emptySnapshot(), trafficErr: errors.New("post-send failure")}
	c := &Collector{Store: s, API: api, Generation: func() (string, error) { return "generation-1", nil }}
	s.beforeCommit = func() error { return errors.New("review commit failed") }
	if err := c.Collect(context.Background(), now); err == nil {
		t.Fatal("failed durable review hidden")
	}
	assertMarker(t, cfg, true)
	s.beforeCommit = nil
	if _, err := s.Authenticate(strings.Repeat("o", 32), "generation-1", now); err == nil {
		t.Fatal("failed review commit left authentication open")
	}
	must(t, s.Close())
	s2, err := OpenStore(cfg, secrets, false, now)
	must(t, err)
	defer s2.Close()
	c.Store = s2
	api.trafficErr = nil
	if err := c.Collect(context.Background(), now); !errors.Is(err, ErrReview) {
		t.Fatalf("restart forgot pending pull: %v", err)
	}
	if api.trafficReads != 1 {
		t.Fatal("restart performed another clear before review")
	}
	assertReviewClosed(t, s2, now)
	assertMarker(t, cfg, false)
}

func TestStaleMarkerExceptionOnlyForArmedEmptyNewGeneration(t *testing.T) {
	for _, kind := range []string{"armed-new", "same-generation", "expired", "not-held", "review", "not-armed", "nonempty-baseline", "identity-error"} {
		t.Run(kind, func(t *testing.T) {
			s, cfg, _, now := testStore(t)
			must(t, createPullMarker(cfg.Database+".pull"))
			must(t, s.Arm(map[string]int64{"owner": 0, "friend": 0}, "reviewed old generation delta loss", true, now))
			gen := "generation-2"
			at := now
			shot := emptySnapshot()
			switch kind {
			case "same-generation":
				gen = "generation-1"
			case "expired":
				at = now.Add(time.Minute)
			case "not-held":
				must(t, s.mutate(func(tx *sql.Tx, _ Meta) error { _, e := tx.Exec(`UPDATE meta SET hold=0`); return e }))
			case "review":
				must(t, s.markReview("prior review", gen, now))
			case "not-armed":
				must(t, s.mutate(func(tx *sql.Tx, _ Meta) error { _, e := tx.Exec(`UPDATE meta SET arm_until=0`); return e }))
			case "nonempty-baseline":
				shot.Traffic["unknown"] = Counter{Tx: 1}
			}
			api := &fakeAPI{shot: shot}
			c := &Collector{Store: s, API: api, Generation: func() (string, error) {
				if kind == "identity-error" {
					return "", errors.New("not live")
				}
				return gen, nil
			}}
			err := c.Collect(context.Background(), at)
			if kind == "armed-new" {
				must(t, err)
				m, e := s.Meta()
				must(t, e)
				if m.Review || !m.Hold || m.Generation != gen || m.ArmUntil != 0 {
					t.Fatal("new baseline bypassed existing hold/arm semantics")
				}
			} else {
				if !errors.Is(err, ErrReview) {
					t.Fatalf("broad stale-marker exception: %v", err)
				}
				assertReviewClosed(t, s, at)
				if kind != "nonempty-baseline" && api.trafficReads != 0 {
					t.Fatal("unsafe exception sent clear")
				}
			}
			assertMarker(t, cfg, false)
		})
	}
}

func TestHTTPDeltaFailuresAndDurableMarker(t *testing.T) {
	for _, kind := range []string{"success", "non-200", "decode", "null", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			s, cfg, _, now := testStore(t)
			var paths []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.String())
				if r.URL.Path == "/online" {
					assertMarker(t, cfg, false)
					_, _ = w.Write([]byte(`{}`))
					return
				}
				assertMarker(t, cfg, true)
				switch kind {
				case "non-200":
					w.WriteHeader(http.StatusInternalServerError)
				case "decode":
					_, _ = w.Write([]byte(`{`))
				case "null":
					_, _ = w.Write([]byte(`null`))
				case "oversized":
					_, _ = w.Write([]byte(strings.Repeat(" ", MaxStatsBody+1)))
				default:
					_, _ = w.Write([]byte(`{}`))
				}
			}))
			defer srv.Close()
			c := &Collector{Store: s, API: NewHTTPAPI(srv.URL, "fixture"), Generation: func() (string, error) { return "generation-1", nil }}
			err := c.Collect(context.Background(), now)
			if kind == "success" {
				must(t, err)
			} else {
				if !errors.Is(err, ErrReview) {
					t.Fatalf("post-send failure not held: %v", err)
				}
				assertReviewClosed(t, s, now)
			}
			if len(paths) != 2 || paths[0] != "/online" || paths[1] != "/traffic?clear=1" {
				t.Fatalf("wrong pull order: %v", paths)
			}
			assertMarker(t, cfg, false)
		})
	}
	// Inject refusal rather than racing ephemeral port reuse after listener close.
	api := NewHTTPAPI("http://127.0.0.1:18083", "fixture")
	api.Client.Transport.(*http.Transport).DialContext = func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	}
	_, err := api.ReadTrafficDelta(context.Background())
	if !errors.Is(err, ErrNotSent) {
		t.Fatalf("refused dial not classified safely: %v", err)
	}
}

func TestHTTPDestructiveGETIsNotTransparentlyReplayed(t *testing.T) {
	s, cfg, _, now := testStore(t)
	var clears atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/online" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		clears.Add(1)
		// The core may have cleared its map before the connection drops.
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer srv.Close()
	api := NewHTTPAPI(srv.URL, "fixture")
	if !api.Client.Transport.(*http.Transport).DisableKeepAlives {
		t.Fatal("destructive GET permits reused-connection retries")
	}
	c := &Collector{Store: s, API: api, Generation: func() (string, error) { return "generation-1", nil }}
	if err := c.Collect(context.Background(), now); !errors.Is(err, ErrReview) {
		t.Fatalf("dropped delta response not held: %v", err)
	}
	if clears.Load() != 1 {
		t.Fatalf("destructive GET replayed: %d", clears.Load())
	}
	assertReviewClosed(t, s, now)
	assertMarker(t, cfg, false)
}

func TestActiveConnectionPresentation(t *testing.T) {
	v := presentationFixture()
	u := v.Users[1]
	u.ActiveSessions = u.ActiveSessionCap
	notice := v.UserNotice(u)
	for _, text := range []string{"活跃连接", "含刚认证", "自动回收", "不是物理设备数限制"} {
		if !strings.Contains(notice.Detail, text) {
			t.Fatalf("capacity notice lacks %q", text)
		}
		if !strings.Contains(renderPresentation(t, v), text) {
			t.Fatalf("page lacks %q", text)
		}
	}
	if strings.Contains(renderPresentation(t, v), "本次核心运行已签发") {
		t.Fatal("page retains lifetime cap semantics")
	}
}

func TestPruneDeadSessionsAndUnknownReappearance(t *testing.T) {
	for _, kind := range []string{"seen", "never-seen", "online", "blocked-offline", "blocked-failed", "blocked-pending", "blocked-issued"} {
		t.Run(kind, func(t *testing.T) {
			s, _, _, now := testStore(t)
			id := session(t, s, "owner", now)
			online := map[string]int{}
			if kind != "never-seen" && kind != "blocked-pending" && kind != "blocked-issued" {
				must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{}, Online: map[string]int{id: 1}}, now.Add(time.Second)))
			}
			if kind == "online" {
				online[id] = 1
			}
			offlineAt := now.Add(2 * time.Second)
			must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{}, Online: online}, offlineAt))
			if strings.HasPrefix(kind, "blocked-") {
				state := strings.TrimPrefix(kind, "blocked-")
				must(t, s.mutate(func(tx *sql.Tx, _ Meta) error {
					_, e := tx.Exec(`UPDATE sessions SET blocked=1,kick_state=? WHERE id=?`, state, id)
					return e
				}))
			}
			deadline := offlineAt.Add(sessionRetention)
			if kind == "never-seen" {
				deadline = now.Add(sessionRetention)
			}
			must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{}, Online: online}, deadline.Add(-time.Nanosecond)))
			var count int
			must(t, s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id=?`, id).Scan(&count))
			if count != 1 {
				t.Fatal("row reclaimed before retention window")
			}
			must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{}, Online: online}, deadline))
			must(t, s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id=?`, id).Scan(&count))
			prunable := kind != "online" && kind != "blocked-pending" && kind != "blocked-issued"
			if prunable && count != 0 {
				t.Fatal("dead session was not reclaimed")
			}
			if !prunable && count != 1 {
				t.Fatal("possibly-live blocked/online row reclaimed")
			}
			if prunable {
				err := s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{id: {Tx: 1}}, Online: map[string]int{}}, deadline.Add(time.Second))
				if !errors.Is(err, ErrReview) {
					t.Fatal("pruned ID reappearance not held")
				}
				assertReviewClosed(t, s, deadline.Add(time.Second))
				m, e := s.Meta()
				must(t, e)
				if !strings.Contains(m.Reason, "unknown session") {
					t.Fatalf("wrong reappearance reason: %s", m.Reason)
				}
			}
		})
	}
}

func TestReopenDelaysOfflinePruningAndRollbackKeepsTimestamp(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	id := session(t, s, "owner", now)
	must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{}, Online: map[string]int{id: 1}}, now))
	must(t, s.Apply("generation-1", emptySnapshot(), now.Add(time.Second)))
	must(t, s.Close())
	reopened := now.Add(time.Hour)
	s2, err := OpenStore(cfg, secrets, false, reopened)
	must(t, err)
	defer s2.Close()
	must(t, s2.Apply("generation-1", emptySnapshot(), reopened))
	if len(s2.offlineSince) != 1 {
		t.Fatal("reopen did not conservatively track offline row")
	}
	s2.beforeCommit = func() error { return errors.New("prune rollback") }
	deadline := reopened.Add(sessionRetention)
	if err := s2.Apply("generation-1", emptySnapshot(), deadline); err == nil {
		t.Fatal("rollback fixture did not fail")
	}
	s2.beforeCommit = nil
	if len(s2.offlineSince) != 1 || len(s2.offlineQueue) != 1 {
		t.Fatal("rollback removed retention state")
	}
	must(t, s2.Apply("generation-1", emptySnapshot(), deadline))
	if len(s2.offlineSince) != 0 || len(s2.offlineQueue) != 0 {
		t.Fatal("committed prune retained memory state")
	}
	var rows int
	must(t, s2.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&rows))
	if rows != 0 {
		t.Fatal("reopened offline row not pruned after new grace period")
	}
}

func TestQueueUserSkipsDeadNeverSeenButProtectsFreshRace(t *testing.T) {
	s, _, _, now := testStore(t)
	dead := session(t, s, "friend", now)
	must(t, s.Apply("generation-1", emptySnapshot(), now.Add(time.Minute)))
	boundary := session(t, s, "friend", now.Add(time.Minute))
	at := now.Add(3 * time.Minute)
	must(t, s.Apply("generation-1", emptySnapshot(), at))
	fresh := session(t, s, "friend", at)
	must(t, s.ChangeUser("disable", "friend", "", 0, at))
	pending, err := s.Pending("generation-1", at, 128)
	must(t, err)
	got := map[string]bool{}
	for _, id := range pending {
		got[id] = true
	}
	if len(got) != 2 || got[dead] || !got[boundary] || !got[fresh] {
		t.Fatal("dead auth queued or fresh auth escaped ban")
	}
	u := user(t, s, "friend", at)
	if u.Offline != 1 || u.Pending != 2 || !u.Disabled {
		t.Fatalf("untruthful ban counters: %+v", u)
	}
	must(t, s.Apply("generation-1", emptySnapshot(), at.Add(3*time.Minute)))
	pending, err = s.Pending("generation-1", at.Add(3*time.Minute), 128)
	must(t, err)
	if len(pending) != 0 || user(t, s, "friend", at).Offline != 3 {
		t.Fatal("aged never-seen ban tasks remained waiting")
	}
}

func TestApplyDoesNotUpdateRetainedOfflineRowsAndPruneIsBounded(t *testing.T) {
	s, _, _, now := testStore(t)
	id := session(t, s, "owner", now)
	must(t, s.mutate(func(tx *sql.Tx, _ Meta) error {
		stmt, err := tx.Prepare(`INSERT INTO sessions(id,account_id,generation,created) SELECT ?,account_id,generation,? FROM sessions WHERE id=?`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for i := 0; i < 5000; i++ {
			if _, err := stmt.Exec(fmt.Sprintf("retained-%d", i), now.Add(-10*time.Minute).UnixNano(), id); err != nil {
				return err
			}
		}
		_, err = tx.Exec(`CREATE TEMP TRIGGER no_offline_updates BEFORE UPDATE ON sessions WHEN OLD.id LIKE 'retained-%' BEGIN SELECT RAISE(ABORT,'apply touched retained offline row'); END`)
		return err
	}))
	must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{id: {Tx: 1}}, Online: map[string]int{id: 1}}, now.Add(time.Second)))
	if user(t, s, "owner", now).Used != 1 {
		t.Fatal("snapshot row was not accounted")
	}
	at := now.Add(5 * time.Minute)
	must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{}, Online: map[string]int{id: 1}}, at))
	var rows int
	must(t, s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id LIKE 'retained-%'`).Scan(&rows))
	if rows != 5000-256 {
		t.Fatalf("prune exceeded/ignored bounded budget: %d", rows)
	}
}

func TestTrafficDeltaAllowsTwiceTheLiveCapacity(t *testing.T) {
	s, _, _, now := testStore(t)
	s.cfg.MaxSessionsPerUser, s.cfg.MaxSessionsTotal = 8, 8
	id := session(t, s, "owner", now)
	shot := emptySnapshot()
	must(t, s.mutate(func(tx *sql.Tx, _ Meta) error {
		for i := 0; i < 16; i++ {
			sid := fmt.Sprintf("delta-%d", i)
			if _, err := tx.Exec(`INSERT INTO sessions(id,account_id,generation,created) SELECT ?,account_id,generation,? FROM sessions WHERE id=?`, sid, now.Add(-3*time.Minute).UnixNano(), id); err != nil {
				return err
			}
			shot.Traffic[sid] = Counter{Tx: 1}
			if i < 8 {
				shot.Online[sid] = 1
			}
		}
		return nil
	}))
	must(t, s.Apply("generation-1", shot, now.Add(time.Second)))
	if u := user(t, s, "owner", now); u.Used != 16 || u.Online != 8 {
		t.Fatalf("valid bounded delta rejected/miscounted: %+v", u)
	}
}

func TestPullMarkerCreationIsExclusiveAndDoesNotFollowSymlinks(t *testing.T) {
	_, cfg, _, _ := testStore(t)
	for _, collide := range []func(*Config){
		func(c *Config) { c.SecretsFile = c.Database + ".pull" },
		func(c *Config) { c.Certificate = c.Database + ".pull" },
		func(c *Config) { c.HealthBlockedFile = c.Database + ".pull" },
	} {
		c := cfg
		collide(&c)
		if c.Validate() == nil {
			t.Fatal("pull marker collides with a protected state/secret path")
		}
	}
	path := t.TempDir() + "/marker"
	must(t, createPullMarker(path))
	if err := createPullMarker(path); !os.IsExist(err) {
		t.Fatalf("marker overwrite allowed: %v", err)
	}
	must(t, removePullMarker(path))
	victim := t.TempDir() + "/unchanged"
	must(t, os.WriteFile(victim, []byte("fixture"), 0600))
	must(t, os.Symlink(victim, path))
	if err := createPullMarker(path); err == nil {
		t.Fatal("marker followed a symlink")
	}
	body, err := os.ReadFile(victim)
	must(t, err)
	if string(body) != "fixture" {
		t.Fatal("symlink target was modified")
	}
}

func TestDeltaCapacityAndSessionSumOverflowFailClosed(t *testing.T) {
	for _, kind := range []string{"traffic-capacity", "online-capacity", "session-sum", "usage-sum", "unknown-online", "negative"} {
		t.Run(kind, func(t *testing.T) {
			s, _, _, now := testStore(t)
			id := session(t, s, "owner", now)
			shot := emptySnapshot()
			switch kind {
			case "traffic-capacity":
				for i := 0; i <= 2*s.cfg.MaxSessionsTotal; i++ {
					shot.Traffic[fmt.Sprint(i)] = Counter{}
				}
			case "online-capacity":
				for i := 0; i <= s.cfg.MaxSessionsTotal; i++ {
					shot.Online[fmt.Sprint(i)] = 1
				}
			case "session-sum":
				must(t, s.mutate(func(tx *sql.Tx, _ Meta) error {
					_, e := tx.Exec(`UPDATE sessions SET tx=?,rx=1 WHERE id=?`, int64(math.MaxInt64-1), id)
					return e
				}))
				shot.Traffic[id] = Counter{Tx: 1}
			case "usage-sum":
				must(t, s.mutate(func(tx *sql.Tx, _ Meta) error {
					_, e := tx.Exec(`UPDATE months SET tx=? WHERE account_id=(SELECT account_id FROM sessions WHERE id=?)`, int64(math.MaxInt64-1), id)
					return e
				}))
				shot.Traffic[id] = Counter{Rx: 2}
			case "unknown-online":
				shot.Online["unknown"] = 1
			case "negative":
				shot.Traffic[id] = Counter{Tx: -1}
			}
			if err := s.Apply("generation-1", shot, now.Add(time.Second)); !errors.Is(err, ErrReview) {
				t.Fatalf("invalid delta accepted: %v", err)
			}
			assertReviewClosed(t, s, now.Add(time.Second))
		})
	}
}

// After prepare-stop (or any review), no sample can commit. The collector must
// not issue the destructive clear then, or reviewed stop evidence is destroyed.
func TestReviewSkipsDestructiveClear(t *testing.T) {
	s, cfg, _, now := testStore(t)
	id := session(t, s, "owner", now)
	api := &fakeAPI{shot: Snapshot{Traffic: map[string]Counter{id: {Tx: 2, Rx: 3}}, Online: map[string]int{id: 1}}}
	c := &Collector{Store: s, API: api, Generation: func() (string, error) { return "generation-1", nil }}
	must(t, s.BeginMaintenance(now))
	must(t, c.CollectHeld(context.Background(), now.Add(time.Second)))
	must(t, s.Hold("planned core maintenance", now.Add(time.Second)))
	reads := api.trafficReads
	for i := 2; i < 5; i++ {
		if err := c.Tick(context.Background(), now.Add(time.Duration(i)*5*time.Second)); !errors.Is(err, ErrReview) {
			t.Fatalf("held tick did not stop: %v", err)
		}
	}
	if api.trafficReads != reads || api.onlineReads != reads {
		t.Fatal("destructive clear issued while accounting is under review")
	}
	assertMarker(t, cfg, false)
	if m, err := s.Meta(); err != nil || m.Reason == interruptedPull {
		t.Fatal("skipped clear was recorded as a lost delta")
	}
}

// Only the destructive read gets the longer budget; a slow but successful
// response must commit instead of becoming a maintenance event.
func TestDestructiveReadToleratesBriefStall(t *testing.T) {
	if destructiveReadTimeout < 5*time.Second || destructiveReadTimeout >= 30*time.Second || ordinaryRequestTimeout != 2*time.Second {
		t.Fatal("unexpected loopback request budgets")
	}
	s, cfg, _, now := testStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/traffic" {
			time.Sleep(2500 * time.Millisecond)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := &Collector{Store: s, API: NewHTTPAPI(srv.URL, "fixture"), Generation: func() (string, error) { return "generation-1", nil }}
	must(t, c.Collect(context.Background(), now))
	assertMarker(t, cfg, false)
}
