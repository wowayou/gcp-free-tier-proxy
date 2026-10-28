package account

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

func kickedIDs(api *fakeAPI) map[string]int {
	got := map[string]int{}
	for _, batch := range api.calls {
		for _, id := range batch {
			got[id]++
		}
	}
	return got
}

func assertNoSharedReview(t *testing.T, s *Store, at time.Time) {
	t.Helper()
	m, err := s.Meta()
	must(t, err)
	if m.Review || m.Hold {
		t.Fatalf("single-account ban became a shared review/hold: %+v", m)
	}
	if _, err := s.Authenticate(strings.Repeat("o", 32), "generation-1", at); err != nil {
		t.Fatalf("owner affected by friend's ban handling: %v", err)
	}
}

// R2 strong form: late activity of an aged, never-seen blocked ID must be
// kicked by its own ID only, never via shared review, for every way the ID
// became blocked and survives later enable/rotation.
func TestRepairLateBlockedSessionKickedWithoutSharedReview(t *testing.T) {
	for _, kind := range []string{"online", "traffic-only"} {
		for _, op := range []string{"disable", "revoke", "rotate", "disable-then-enable"} {
			t.Run(kind+"/"+op, func(t *testing.T) {
				s, _, _, now := testStore(t)
				owner := session(t, s, "owner", now)
				old := session(t, s, "friend", now)
				later := now.Add(3 * time.Minute)
				must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{}, Online: map[string]int{owner: 1}}, later))
				switch op {
				case "disable-then-enable":
					must(t, s.ChangeUser("disable", "friend", "", 0, later))
					must(t, s.ChangeUser("enable", "friend", "", 0, later))
				default:
					must(t, s.ChangeUser(op, "friend", "", 0, later))
				}
				shot := Snapshot{Traffic: map[string]Counter{old: {Tx: 1}}, Online: map[string]int{owner: 1}}
				if kind == "online" {
					shot.Online[old] = 1
				}
				api := &fakeAPI{shot: shot}
				c := &Collector{Store: s, API: api, Generation: func() (string, error) { return "generation-1", nil }}
				for i := 1; i <= 3; i++ {
					must(t, c.Tick(context.Background(), later.Add(time.Duration(i)*time.Second)))
					api.shot.Traffic = map[string]Counter{}
				}
				got := kickedIDs(api)
				if got[old] != 1 || got[owner] != 0 || len(got) != 1 {
					t.Fatalf("kick not targeted exactly once at the old ID: %v", got)
				}
				assertNoSharedReview(t, s, later.Add(4*time.Second))
			})
		}
	}
}

// R2: reappearance never refills the bounded budget, including across reopen.
func TestRepairExhaustedBudgetSurvivesReappearanceAndReopen(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	old := session(t, s, "friend", now)
	must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{}, Online: map[string]int{old: 1}}, now.Add(time.Second)))
	must(t, s.ChangeUser("disable", "friend", "", 0, now.Add(time.Second)))
	api := &fakeAPI{shot: Snapshot{Traffic: map[string]Counter{}, Online: map[string]int{old: 1}}, kickErr: fmt.Errorf("simulated timeout")}
	c := &Collector{Store: s, API: api, Generation: func() (string, error) { return "generation-1", nil }}
	at := now.Add(2 * time.Second)
	for i := 0; i < 6; i++ {
		_ = c.Tick(context.Background(), at)
		at = at.Add(6 * time.Second)
	}
	if len(api.calls) != 3 || user(t, s, "friend", at).Failed != 1 {
		t.Fatalf("budget not bounded at 3: calls=%d", len(api.calls))
	}
	// Close observed: resolved. Then the same ID reappears after reopen.
	api.shot.Online = map[string]int{}
	_ = c.Tick(context.Background(), at)
	must(t, s.Close())
	s2, err := OpenStore(cfg, secrets, false, at.Add(time.Second))
	must(t, err)
	defer s2.Close()
	c.Store = s2
	api.shot = Snapshot{Traffic: map[string]Counter{old: {Rx: 1}}, Online: map[string]int{old: 1}}
	for i := 0; i < 4; i++ {
		at = at.Add(6 * time.Second)
		_ = c.Tick(context.Background(), at)
		api.shot.Traffic = map[string]Counter{}
	}
	if len(api.calls) != 3 {
		t.Fatalf("reappearance/reopen refilled kick budget: calls=%d", len(api.calls))
	}
	if user(t, s2, "friend", at).Failed != 1 {
		t.Fatal("exhausted reappearing ban not visibly failed")
	}
	m, err := s2.Meta()
	must(t, err)
	if m.Review {
		t.Fatal("individual ban failure became shared review")
	}
}

// R3: repeated late deltas keep renewing retention; usage is exact.
func TestRepairRepeatedLateDeltasRenewRetention(t *testing.T) {
	s, _, _, now := testStore(t)
	id := session(t, s, "owner", now)
	must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{}, Online: map[string]int{id: 1}}, now))
	at := now.Add(time.Second)
	must(t, s.Apply("generation-1", emptySnapshot(), at))
	for i := 0; i < 4; i++ {
		at = at.Add(sessionRetention - time.Second)
		must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{id: {Rx: 5}}, Online: map[string]int{}}, at))
	}
	if user(t, s, "owner", at).Used != 20 {
		t.Fatal("late deltas lost or duplicated")
	}
	var rows int
	must(t, s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id=?`, id).Scan(&rows))
	if rows != 1 {
		t.Fatal("renewed session reclaimed early")
	}
	must(t, s.Apply("generation-1", emptySnapshot(), at.Add(sessionRetention-time.Nanosecond)))
	must(t, s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id=?`, id).Scan(&rows))
	if rows != 1 {
		t.Fatal("reclaimed before a full quiet window after last delta")
	}
	must(t, s.Apply("generation-1", emptySnapshot(), at.Add(sessionRetention)))
	must(t, s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id=?`, id).Scan(&rows))
	if rows != 0 {
		t.Fatal("quiet session never reclaimed")
	}
}

// R3: nonprunable heads (old blocked tasks) cannot permanently starve
// reclamation of other dead sessions; per-sample work stays bounded.
func TestRepairNonprunableHeadsDoNotStarveReclamation(t *testing.T) {
	s, _, _, now := testStore(t)
	s.cfg.MaxSessionsPerUser, s.cfg.MaxSessionsTotal = 2048, 8192
	must(t, s.mutate(func(tx *sql.Tx, _ Meta) error {
		var owner string
		if err := tx.QueryRow(`SELECT id FROM accounts WHERE name='owner'`).Scan(&owner); err != nil {
			return err
		}
		for i := 0; i < 600; i++ {
			// Never-seen blocked IDs with traffic: retained, possibly live tasks.
			if _, err := tx.Exec(`INSERT INTO sessions(id,account_id,generation,created,has_counter,blocked,kick_state) VALUES(?,?,?,?,1,1,'issued')`, fmt.Sprintf("held-%04d", i), owner, "generation-1", now.UnixNano()); err != nil {
				return err
			}
		}
		for i := 0; i < 20; i++ {
			// Ordinary dead auths queued behind the held heads.
			if _, err := tx.Exec(`INSERT INTO sessions(id,account_id,generation,created) VALUES(?,?,?,?)`, fmt.Sprintf("dead-%04d", i), owner, "generation-1", now.Add(time.Second).UnixNano()); err != nil {
				return err
			}
		}
		return nil
	}))
	for i := 0; i < 600; i++ {
		s.trackOffline(fmt.Sprintf("held-%04d", i), true, now)
	}
	for i := 0; i < 20; i++ {
		s.trackOffline(fmt.Sprintf("dead-%04d", i), true, now.Add(time.Second))
	}
	at := now.Add(time.Second)
	for i := 0; i < 8; i++ {
		at = at.Add(sessionRetention / 2)
		must(t, s.Apply("generation-1", emptySnapshot(), at))
	}
	var left int
	must(t, s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id NOT LIKE 'held-%'`).Scan(&left))
	if left != 0 {
		t.Fatalf("dead sessions starved behind nonprunable heads: %d left", left)
	}
	var held int
	must(t, s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id LIKE 'held-%'`).Scan(&held))
	if held != 600 {
		t.Fatal("possibly-live blocked tasks reclaimed")
	}
}

// R1: an orphan marker survives reopen even when an armed new generation is
// pending; only a validated empty new baseline clears it.
func TestRepairOrphanMarkerArmedReopenRecovery(t *testing.T) {
	for _, kind := range []string{"armed-new", "same-generation"} {
		t.Run(kind, func(t *testing.T) {
			s, cfg, secrets, now := testStore(t)
			must(t, createPullMarker(cfg.Database+".pull"))
			must(t, s.Arm(map[string]int64{"owner": 0, "friend": 0}, "reviewed old generation delta loss", true, now))
			must(t, s.Close())
			s2, err := OpenStore(cfg, secrets, false, now.Add(time.Second))
			must(t, err)
			defer s2.Close()
			if _, err := s2.Authenticate(strings.Repeat("o", 32), "generation-1", now.Add(time.Second)); err == nil {
				t.Fatal("armed orphan allowed authentication")
			}
			gen := "generation-2"
			if kind == "same-generation" {
				gen = "generation-1"
			}
			api := &fakeAPI{shot: emptySnapshot()}
			c := &Collector{Store: s2, API: api, Generation: func() (string, error) { return gen, nil }}
			err = c.Collect(context.Background(), now.Add(2*time.Second))
			if kind == "armed-new" {
				must(t, err)
				assertMarker(t, cfg, false)
				must(t, s2.Activate(now.Add(2*time.Second)))
				if _, err := s2.Authenticate(strings.Repeat("o", 32), gen, now.Add(3*time.Second)); err != nil {
					t.Fatalf("reviewed new generation still blocked: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("same-generation orphan accepted after arm")
			}
			if _, err := s2.Authenticate(strings.Repeat("o", 32), gen, now.Add(3*time.Second)); err == nil {
				t.Fatal("same-generation orphan allowed authentication")
			}
		})
	}
}

// R1: ordinary in-process sampling never closes authentication (no jitter).
func TestRepairNormalSamplingKeepsAuthOpen(t *testing.T) {
	s, cfg, _, now := testStore(t)
	id := session(t, s, "owner", now)
	api := &fakeAPI{shot: Snapshot{Traffic: map[string]Counter{id: {Tx: 1}}, Online: map[string]int{id: 1}}}
	c := &Collector{Store: s, API: api, Generation: func() (string, error) { return "generation-1", nil }}
	at := now
	for i := 0; i < 20; i++ {
		at = at.Add(5 * time.Second)
		must(t, c.Tick(context.Background(), at))
		assertMarker(t, cfg, false)
	}
	m, err := s.Meta()
	must(t, err)
	if m.Review || m.Hold {
		t.Fatal("normal sampling closed authentication")
	}
	if _, err := s.Authenticate(strings.Repeat("o", 32), "generation-1", at); err != nil {
		t.Fatalf("auth jitter after normal sampling: %v", err)
	}
}

// R4: >=4096 reconnects in one generation (far beyond the old cumulative
// 512/2048 caps) with exact accounting and bounded retained state. Virtual
// time only; not an e2-micro performance claim.
func TestRepairLongRunReconnectsStayBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("long-run reconnect simulation")
	}
	s, _, _, now := testStore(t)
	s.cfg.MaxSessionsPerUser, s.cfg.MaxSessionsTotal = 8, 32
	const n = 4100
	at := now
	prev := ""
	maxRows, maxIndex := 0, 0
	for i := 0; i < n; i++ {
		at = at.Add(9 * time.Second) // < 8 auths per minute
		id, err := s.Authenticate(strings.Repeat("o", 32), "generation-1", at)
		if err != nil {
			t.Fatalf("reconnect %d denied: %v", i, err)
		}
		shot := Snapshot{Traffic: map[string]Counter{id: {Tx: 1, Rx: 2}}, Online: map[string]int{id: 1}}
		if prev != "" {
			shot.Traffic[prev] = Counter{}
		}
		must(t, s.Apply("generation-1", shot, at.Add(time.Second)))
		prev = id
		if i%256 == 0 {
			var rows int
			must(t, s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&rows))
			maxRows = max(maxRows, rows)
			maxIndex = max(maxIndex, len(s.offlineSince), len(s.offlineQueue))
		}
	}
	u := user(t, s, "owner", at)
	if u.Used != 3*n {
		t.Fatalf("accounting drift: used=%d want=%d", u.Used, 3*n)
	}
	if u.ActiveSessions > s.cfg.MaxSessionsPerUser || u.Sessions != n {
		t.Fatal("active capacity not released")
	}
	// 15 min retention / 9 s per reconnect ~= 100 rows; allow generous slack.
	if maxRows > 300 || maxIndex > 300 {
		t.Fatalf("retained state grows with cumulative reconnects: rows=%d index=%d", maxRows, maxIndex)
	}
	m, err := s.Meta()
	must(t, err)
	if m.Review {
		t.Fatalf("long run entered review: %s", m.Reason)
	}
}
