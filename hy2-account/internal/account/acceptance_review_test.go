package account

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Deployment gates for the session-reclamation candidate. These exercise
// failure boundaries not covered by the original session_reclaim tests.
func TestAcceptancePendingPullBlocksAuthBeforeCollector(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	// The previous process cleared core counters, then exited before Apply.
	must(t, createPullMarker(cfg.Database+".pull"))
	must(t, s.Close())
	reopened, err := OpenStore(cfg, secrets, false, now.Add(time.Second))
	must(t, err)
	defer reopened.Close()
	// Daemon.Run starts the auth listener before its first collector tick.
	// A fresh last_sample must not authorize a request in that startup window.
	if _, err := reopened.Authenticate(strings.Repeat("o", 32), "generation-1", now.Add(time.Second)); err == nil {
		t.Fatal("interrupted destructive pull allowed authentication before first collector tick")
	}
}

func TestAcceptanceLateOnlineBlockedSessionMustBeKicked(t *testing.T) {
	s, _, _, now := testStore(t)
	id := session(t, s, "friend", now)
	// Auth was persisted, but no online sample has observed the connection yet.
	// The core protocol has no accountd-issued, enforced two-minute auth lease.
	later := now.Add(3 * time.Minute)
	must(t, s.Apply("generation-1", emptySnapshot(), later))
	must(t, s.ChangeUser("disable", "friend", "", 0, later))
	// Once the supposedly dead session is actually observed alive, its ban must
	// become actionable (or the controller must fail closed), not stay offline.
	api := &fakeAPI{shot: Snapshot{
		Traffic: map[string]Counter{id: {Tx: 1}},
		Online:  map[string]int{id: 1},
	}}
	c := &Collector{Store: s, API: api, Generation: func() (string, error) { return "generation-1", nil }}
	err := c.Tick(context.Background(), later.Add(time.Second))
	m, metaErr := s.Meta()
	must(t, metaErr)
	kicked := false
	for _, batch := range api.calls {
		for _, got := range batch {
			if got == id {
				kicked = true
			}
		}
	}
	if !kicked && !(err != nil && m.Review && m.Hold) {
		t.Fatal("disabled account's observed live session stayed ban-offline: no kick and no review")
	}
}

func TestAcceptanceFreshTrafficDefersOfflineReclamation(t *testing.T) {
	s, _, _, now := testStore(t)
	id := session(t, s, "owner", now)
	must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{}, Online: map[string]int{id: 1}}, now))
	offlineAt := now.Add(time.Second)
	must(t, s.Apply("generation-1", emptySnapshot(), offlineAt))
	// /online is sampled before /traffic, not atomically with it. New bytes
	// contradict a claim that a session has been inactive for the whole grace
	// period, regardless of why the two observations differ. Do not delete its
	// mapping in the very transaction that accounts those bytes.
	must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{id: {Rx: 1}}, Online: map[string]int{}}, offlineAt.Add(sessionRetention)))
	var rows int
	must(t, s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id=?`, id).Scan(&rows))
	if rows != 1 {
		t.Fatal("session with freshly observed traffic reclaimed immediately; next delta becomes unknown")
	}
}
