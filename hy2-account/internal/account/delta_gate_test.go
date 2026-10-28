package account

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// simulateCumulativeState makes the store look like a database last written by
// the cumulative (non-clearing) r3 binary: same live generation, no sidecar.
func simulateCumulativeState(t *testing.T, s *Store, cfg Config, secrets Secrets, at time.Time) *Store {
	t.Helper()
	must(t, s.Close())
	must(t, os.Remove(deltaBaselinePath(cfg)))
	s2, err := OpenStore(cfg, secrets, false, at)
	must(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	return s2
}

func TestDeltaGateHotSwapOntoCumulativeCoreFailsClosed(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	id := session(t, s, "owner", now)
	s2 := simulateCumulativeState(t, s, cfg, secrets, now.Add(time.Second))
	v, err := s2.View(now.Add(time.Second))
	must(t, err)
	if !v.Meta.Review || !v.Meta.Hold || v.Meta.Reason != deltaGateReason || v.Healthy {
		t.Fatalf("hot swap not reported as reviewable gate: %+v", v.Meta)
	}
	if v.DataError != "" {
		t.Fatal("gate must stay reviewable, not a data error")
	}
	if _, err := s2.Authenticate(strings.Repeat("o", 32), "generation-1", now.Add(time.Second)); err == nil {
		t.Fatal("unbaselined generation allowed authentication")
	}
	// Cumulative totals already in the core must never be cleared/charged.
	api := &fakeAPI{shot: Snapshot{Traffic: map[string]Counter{id: {Tx: 900, Rx: 900}}, Online: map[string]int{id: 1}}}
	c := &Collector{Store: s2, API: api, Generation: func() (string, error) { return "generation-1", nil }}
	if err := c.Collect(context.Background(), now.Add(2*time.Second)); err == nil {
		t.Fatal("unbaselined generation sampled")
	}
	if api.trafficReads != 0 || user(t, s2, "owner", now).Used != 0 {
		t.Fatal("destructive read or charge on an unbaselined cumulative core")
	}
}

func TestDeltaGateArmedSameGenerationCannotBypass(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	id := session(t, s, "owner", now)
	s2 := simulateCumulativeState(t, s, cfg, secrets, now.Add(time.Second))
	// An operator arms while the old cumulative core is still running.
	must(t, s2.Arm(map[string]int64{"owner": 0, "friend": 0}, "operator armed without stopping core", true, now.Add(time.Second)))
	api := &fakeAPI{shot: Snapshot{Traffic: map[string]Counter{id: {Tx: 900, Rx: 900}}, Online: map[string]int{id: 1}}}
	c := &Collector{Store: s2, API: api, Generation: func() (string, error) { return "generation-1", nil }}
	if err := c.Collect(context.Background(), now.Add(2*time.Second)); err == nil {
		t.Fatal("armed same-generation cumulative core sampled")
	}
	if api.trafficReads != 0 || user(t, s2, "owner", now).Used != 0 {
		t.Fatal("armed hold let cumulative totals be cleared/charged")
	}
	m, err := s2.Meta()
	must(t, err)
	if !m.Review || m.Reason != deltaGateReason {
		t.Fatalf("bypass attempt not durably held for review: %+v", m)
	}
	if err := s2.Activate(now.Add(3 * time.Second)); err == nil {
		t.Fatal("activation allowed on unbaselined generation")
	}
}

func TestDeltaGateReviewedRecoveryIntoNewGenerationClears(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	s2 := simulateCumulativeState(t, s, cfg, secrets, now.Add(time.Second))
	// Normal path: core stopped, reviewed arm, fresh core with empty baseline.
	must(t, s2.Arm(map[string]int64{"owner": 0, "friend": 0}, "reviewed cutover to delta accounting", true, now.Add(time.Second)))
	api := &fakeAPI{shot: emptySnapshot()}
	c := &Collector{Store: s2, API: api, Generation: func() (string, error) { return "generation-2", nil }}
	must(t, c.Collect(context.Background(), now.Add(2*time.Second)))
	if got := readDeltaBaseline(cfg); got != "generation-2" {
		t.Fatalf("sidecar not written for new baseline: %q", got)
	}
	must(t, s2.Activate(now.Add(2*time.Second)))
	if _, err := s2.Authenticate(strings.Repeat("o", 32), "generation-2", now.Add(3*time.Second)); err != nil {
		t.Fatalf("recovered generation still gated: %v", err)
	}
	// Survives reopen.
	must(t, s2.Close())
	s3, err := OpenStore(cfg, secrets, false, now.Add(4*time.Second))
	must(t, err)
	defer s3.Close()
	if m, err := s3.Meta(); err != nil || m.Reason == deltaGateReason {
		t.Fatalf("baseline forgotten across reopen: %+v %v", m, err)
	}
}

func TestDeltaGateSidecarAheadOfMetaOrCorruptStaysGated(t *testing.T) {
	for _, body := range []string{`{"generation":"generation-9"}`, `{`, ``, `{"generation":""}`} {
		s, cfg, secrets, now := testStore(t)
		must(t, s.Close())
		must(t, os.WriteFile(deltaBaselinePath(cfg), []byte(body), 0600))
		s2, err := OpenStore(cfg, secrets, false, now)
		must(t, err)
		if _, err := s2.Authenticate(strings.Repeat("o", 32), "generation-1", now); err == nil {
			t.Fatalf("sidecar %q treated as baselined", body)
		}
		must(t, s2.Close())
	}
}

func TestStatusExposesExplicitActiveSessionKeys(t *testing.T) {
	s, _, _, now := testStore(t)
	_ = session(t, s, "owner", now)
	v, err := s.View(now)
	must(t, err)
	b, err := json.Marshal(v)
	must(t, err)
	for _, key := range []string{`"sessions_generation":1`, `"session_cap_generation":64`, `"active_sessions":1`, `"active_session_cap":64`} {
		if !strings.Contains(string(b), key) {
			t.Fatalf("status JSON missing %s", key)
		}
	}
}
