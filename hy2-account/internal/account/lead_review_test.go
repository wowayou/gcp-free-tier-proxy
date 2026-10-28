package account

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Independent lead acceptance: do not use a shared-failure latch to make a
// single-account late-session test pass, and do not regain kick attempts by aging.
func TestLeadBanBudgetCannotBeRenewedByOfflineAge(t *testing.T) {
	s, _, _, now := testStore(t)
	old := session(t, s, "friend", now)
	must(t, s.ChangeUser("disable", "friend", "", 0, now))
	for i := 0; i < 3; i++ {
		must(t, s.MarkAttempt([]string{old}, now.Add(time.Duration(i)*5*time.Second)))
		must(t, s.KickResult([]string{old}, "pending"))
	}
	at := now.Add(3 * time.Minute)
	must(t, s.Apply("generation-1", emptySnapshot(), at))
	must(t, s.ChangeUser("enable", "friend", "", 0, at))
	api := &fakeAPI{shot: Snapshot{Traffic: map[string]Counter{old: {Rx: 1}}, Online: map[string]int{old: 1}}}
	c := &Collector{Store: s, API: api, Generation: func() (string, error) { return "generation-1", nil }}
	for i := 0; i < 4; i++ {
		must(t, c.Tick(context.Background(), at.Add(time.Duration(i+1)*5*time.Second)))
	}
	var attempts int
	var state string
	must(t, s.db.QueryRow(`SELECT attempts,kick_state FROM sessions WHERE id=?`, old).Scan(&attempts, &state))
	if attempts != 3 || state != "failed" || len(api.calls) != 0 {
		t.Fatalf("exhausted old ban resurrected: attempts=%d state=%s calls=%d", attempts, state, len(api.calls))
	}
	m, err := s.Meta()
	must(t, err)
	if m.Review || m.Hold {
		t.Fatal("individual ban became shared failure")
	}
	if _, err := s.Authenticate(strings.Repeat("o", 32), "generation-1", at.Add(21*time.Second)); err != nil {
		t.Fatal("owner affected by friend's failed ban", err)
	}
}

func TestLeadNeverSeenZeroCounterRenewalSurvivesReopen(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	id := session(t, s, "owner", now)
	at := now.Add(16 * time.Minute)
	// Presence in /traffic is activity evidence even for a zero counter.
	must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{id: {}}, Online: map[string]int{}}, at))
	must(t, s.Close())
	s2, err := OpenStore(cfg, secrets, false, at.Add(time.Second))
	must(t, err)
	defer s2.Close()
	must(t, s2.Apply("generation-1", emptySnapshot(), at.Add(5*time.Minute)))
	var rows int
	must(t, s2.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id=?`, id).Scan(&rows))
	if rows != 1 {
		t.Fatal("reopen pruned a never-seen session before a full quiet grace period")
	}
	must(t, s2.Apply("generation-1", Snapshot{Traffic: map[string]Counter{id: {Rx: 2}}, Online: map[string]int{}}, at.Add(10*time.Minute)))
	if user(t, s2, "owner", at).Used != 2 {
		t.Fatal("late usage lost or duplicated")
	}
}

func TestLeadOrphanMarkerCannotBeWashedByDirectSample(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	must(t, createPullMarker(cfg.Database+".pull"))
	must(t, s.Close())
	s2, err := OpenStore(cfg, secrets, false, now.Add(time.Second))
	must(t, err)
	defer s2.Close()
	_ = s2.Apply("generation-1", emptySnapshot(), now.Add(2*time.Second))
	_ = s2.Activate(now.Add(2 * time.Second))
	if _, err := s2.Authenticate(strings.Repeat("o", 32), "generation-1", now.Add(2*time.Second)); err == nil {
		t.Fatal("ordinary same-generation sample/activate washed unresolved pull evidence")
	}
	v, err := s2.View(now.Add(2 * time.Second))
	must(t, err)
	if !v.Meta.Review && !v.Meta.Hold && v.DataError == "" {
		t.Fatal("view hides unresolved startup accounting loss")
	}
}
