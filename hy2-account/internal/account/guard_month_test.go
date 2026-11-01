package account

import (
	"testing"
	"time"
)

func guardFile(t *testing.T, cfg Config, month string, updated time.Time, blocked bool) {
	t.Helper()
	must(t, writeJSON(cfg.GuardStatusFile, GuardStatus{Month: month, Limit: 180 * GiB, Warn: 150 * GiB, Used: 120 * GiB, Healthy: true, Blocked: blocked, Updated: updated.Unix(), Services: []string{"sing-box", "hysteria-server"}}, 0644))
}

// Production 2026-11-01: the guard wrote "2026-10" at 06:59:16Z and only
// switched at 07:00:16Z, while hy2-health ran at 07:00:04Z (LA midnight
// 07:00:00Z) and latched HY2 off. The pre-rollover file must stay valid.
func TestGuardMonthRolloverAcceptsLastPreviousMonthWrite(t *testing.T) {
	_, cfg, _, _ := testStore(t)
	for _, c := range []struct {
		name, previous string
		start          time.Time
	}{
		{"PDT incident 2026-11", "2026-10", time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)},
		{"PST 2026-12", "2026-11", time.Date(2026, 12, 1, 8, 0, 0, 0, time.UTC)},
		{"year 2027-01", "2026-12", time.Date(2027, 1, 1, 8, 0, 0, 0, time.UTC)},
	} {
		if m, _ := billingTime(c.start); m == c.previous {
			t.Fatalf("%s: fixture start is not the LA month boundary", c.name)
		}
		if m, _ := billingTime(c.start.Add(-time.Second)); m != c.previous {
			t.Fatalf("%s: fixture previous month mismatch", c.name)
		}
		guardFile(t, cfg, c.previous, c.start.Add(-44*time.Second), false)
		for _, after := range []time.Duration{0, 4 * time.Second, 60 * time.Second, 135 * time.Second} {
			if _, err := ReadGuard(cfg.GuardStatusFile, c.start.Add(after)); err != nil {
				t.Fatalf("%s +%s: pre-rollover guard write rejected: %v", c.name, after, err)
			}
		}
		// Normal staleness still ends the tolerance; no separate longer window.
		if _, err := ReadGuard(cfg.GuardStatusFile, c.start.Add(137*time.Second)); err == nil {
			t.Fatalf("%s: stale previous-month guard accepted", c.name)
		}
		guardFile(t, cfg, c.previous, c.start.Add(-time.Second), false)
		if _, err := ReadGuard(cfg.GuardStatusFile, c.start.Add(181*time.Second)); err == nil {
			t.Fatalf("%s: previous-month guard accepted past the bound", c.name)
		}
	}
}

func TestGuardMonthRolloverKeepsFailClosedChecks(t *testing.T) {
	_, cfg, _, _ := testStore(t)
	start := time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)
	now := start.Add(4 * time.Second)
	for _, c := range []struct {
		name    string
		month   string
		updated time.Time
		blocked bool
	}{
		// Old label written AFTER the boundary means the guard's month logic is
		// wrong or the clock jumped; it must not inherit the rollover allowance.
		{"previous month written after rollover", "2026-10", start.Add(time.Second), false},
		{"two months old", "2026-09", start.Add(-10 * time.Second), false},
		{"future month", "2026-12", start.Add(-10 * time.Second), false},
		{"blocked previous month", "2026-10", start.Add(-10 * time.Second), true},
	} {
		guardFile(t, cfg, c.month, c.updated, c.blocked)
		if _, err := ReadGuard(cfg.GuardStatusFile, now); err == nil {
			t.Fatalf("%s accepted", c.name)
		}
	}
	// The month after the boundary must not accept a stale previous label
	// mid-month (ordinary staleness plus the bounded allowance).
	guardFile(t, cfg, "2026-10", start.Add(-10*time.Second), false)
	if _, err := ReadGuard(cfg.GuardStatusFile, start.Add(24*time.Hour)); err == nil {
		t.Fatal("previous-month guard accepted mid-month")
	}
	// Current month keeps working exactly as before.
	guardFile(t, cfg, "2026-11", start.Add(16*time.Second), false)
	if _, err := ReadGuard(cfg.GuardStatusFile, start.Add(20*time.Second)); err != nil {
		t.Fatal(err)
	}
}

// accountd uses the same check for status/health witness; it must not report
// a guard error (and thus an unhealthy witness) during the rollover gap.
func TestDaemonStatusHasNoGuardErrorDuringRollover(t *testing.T) {
	s, cfg, secrets, _ := testStore(t)
	start := time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)
	guardFile(t, cfg, "2026-10", start.Add(-44*time.Second), false)
	c := &Collector{Store: s, API: &fakeAPI{}, Generation: func() (string, error) { return "generation-1", nil }}
	d := NewDaemon(s, c, cfg, secrets)
	v, err := d.Status(start.Add(4 * time.Second))
	must(t, err)
	if v.GuardError != "" || v.Guard.Month != "2026-10" {
		t.Fatalf("rollover guard reported as error: %q", v.GuardError)
	}
	if v.Month != "2026-11" {
		t.Fatalf("ledger month must follow the clock, got %s", v.Month)
	}
}
