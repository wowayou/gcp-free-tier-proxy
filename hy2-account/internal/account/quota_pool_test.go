package account

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestApprovedOwner120Friend30Pool150KeepsUsageAndAllocationChecks(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	must(t, s.ChangeUser("quota", "owner", "", 90*GiB, now))
	must(t, s.ChangeUser("quota", "friend", "", 30*GiB, now))
	id := session(t, s, "owner", now)
	must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{id: {Tx: GiB, Rx: GiB}}, Online: map[string]int{id: 1}}, now))
	must(t, s.Hold("test approved pool update requires maintained accounting hold", now))
	must(t, s.Arm(map[string]int64{"owner": 80 * GiB, "friend": 0}, "test synthetic historical adjustment; not production", true, now))
	must(t, s.Apply("generation-2", Snapshot{Traffic: map[string]Counter{}, Online: map[string]int{}}, now))
	must(t, s.Activate(now))
	if err := s.ChangeUser("quota", "owner", "", 120*GiB, now); err == nil {
		t.Fatal("old 140 GiB allocation incorrectly accepted 120+30")
	}
	cfg.QuotaPoolBytes = 150 * GiB
	must(t, cfg.Validate())
	must(t, s.Close())
	s2, err := OpenStore(cfg, secrets, false, now)
	must(t, err)
	defer s2.Close()
	must(t, s2.ChangeUser("quota", "owner", "", 120*GiB, now))
	owner, friend := user(t, s2, "owner", now), user(t, s2, "friend", now)
	if owner.Quota != 120*GiB || owner.Used != 82*GiB || owner.Adjustment != 80*GiB || owner.Remaining != 38*GiB || friend.Quota != 30*GiB || friend.Used != 0 {
		t.Fatal("raising approved quota/pool erased usage or changed friend")
	}
	view, err := s2.View(now)
	must(t, err)
	if view.QuotaPool != 150*GiB || view.AllocatedQuota() != 150*GiB || view.UnallocatedQuota() != 0 {
		t.Fatal("150 GiB pool reservation incorrect")
	}
	if err := s2.ChangeUser("quota", "owner", "", 120*GiB+1, now); err == nil {
		t.Fatal("raising pool disabled exact allocation bound")
	}
	if err := s2.AddUsers([]NewUser{{Name: "overflow", Quota: 1}}, now); err == nil {
		t.Fatal("full approved pool accepted another byte allocation")
	}
	if user(t, s2, "owner", now).Quota != 120*GiB {
		t.Fatal("rejected over-allocation mutated approved quota")
	}
}

func TestRaisedPoolCannotBypassGuardOrSharedHealth(t *testing.T) {
	for _, kind := range []string{"blocked", "stale", "limit", "shared-lock"} {
		t.Run(kind, func(t *testing.T) {
			s, cfg, secrets, now := testStore(t)
			cfg.QuotaPoolBytes = 150 * GiB
			s.cfg = cfg
			healthyGuard(t, cfg, now, false)
			switch kind {
			case "blocked":
				healthyGuard(t, cfg, now, true)
			case "stale", "limit":
				var g GuardStatus
				b, err := os.ReadFile(cfg.GuardStatusFile)
				must(t, err)
				must(t, json.Unmarshal(b, &g))
				if kind == "stale" {
					g.Updated = now.Add(-181 * time.Second).Unix()
				} else {
					g.Used = 180 * GiB
				}
				must(t, writeJSON(cfg.GuardStatusFile, g, 0644))
			case "shared-lock":
				must(t, AtomicFile(cfg.HealthBlockedFile, []byte("test shared block"), 0600))
			}
			c := &Collector{Store: s, API: &fakeAPI{}, Generation: func() (string, error) { return "generation-1", nil }}
			d := NewDaemon(s, c, cfg, secrets)
			// A larger personal/pool allocation does not unmask, arm, or enable
			// through independent guard/health failures.
			must(t, s.ChangeUser("disable", "friend", "", 0, now))
			if result := d.Admin(context.Background(), AdminRequest{Action: "enable", Name: "friend"}, now); result.OK {
				t.Fatal("pool increase enabled a user through shared protection")
			}
			status, err := d.Status(now)
			must(t, err)
			if status.AuthAllowed || status.Healthy {
				t.Fatal("pool increase made guarded status healthy")
			}
		})
	}
}

func TestQuotaUpdateWhileCoreStoppedCanCommitDespiteCLIIncompleteStatus(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	cfg.QuotaPoolBytes = 150 * GiB
	s.cfg = cfg
	must(t, s.Hold("test stopped maintenance; not zero tail evidence", now))
	c := &Collector{Store: s, API: &fakeAPI{}, Generation: func() (string, error) { return "", errors.New("test stopped core") }}
	d := NewDaemon(s, c, cfg, secrets)
	result := d.Admin(context.Background(), AdminRequest{Action: "quota", Name: "owner", Quota: 120 * GiB}, now)
	if !result.Incomplete || result.OK || !strings.Contains(result.Error, "core identity unavailable") || user(t, s, "owner", now).Quota != 120*GiB {
		t.Fatal("stopped-core quota write semantics changed; errors must be reconciled before retry")
	}
	status, err := d.Status(now)
	must(t, err)
	if !status.Meta.Hold || !status.Meta.Review || status.AuthAllowed || status.QuotaPool != 150*GiB {
		t.Fatal("raising quota cleared maintenance protection")
	}
	// User mutation cannot create a root control lock or service write path.
	if _, err := os.Lstat(cfg.HealthBlockedFile + ".lock"); !os.IsNotExist(err) {
		t.Fatal("personal quota update acquired root health control lock")
	}
}
