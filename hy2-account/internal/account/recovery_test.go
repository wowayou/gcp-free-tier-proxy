package account

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func makeFIFO(t *testing.T, path string) {
	t.Helper()
	must(t, syscall.Mkfifo(path, 0600))
}

func openFIFOWriter(t *testing.T, path string) *os.File {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
		if err == nil {
			return os.NewFile(uintptr(fd), path)
		}
		if !errors.Is(err, syscall.ENXIO) || time.Now().After(deadline) {
			t.Fatalf("bounded FIFO reader readiness failed: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestArmPostCommitWitnessFailureIsVisibleToRootStatus(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	healthyGuard(t, cfg, now, false)
	c := &Collector{Store: s, API: &fakeAPI{}, Generation: func() (string, error) { return "", errors.New("test core stopped") }}
	d := NewDaemon(s, c, cfg, secrets)
	oldWitness, err := os.ReadFile(cfg.Anchor)
	must(t, err)
	s.beforeCommit = func() error {
		if err := os.Remove(cfg.Anchor); err != nil {
			return err
		}
		return os.Mkdir(cfg.Anchor, 0700)
	}
	result := d.Admin(context.Background(), AdminRequest{Action: "arm", Charges: map[string]int64{"owner": GiB, "friend": 0}, Reason: "test reviewed recovery increment", AcceptGap: true}, now)
	s.beforeCommit = nil
	if result.OK || !strings.Contains(result.Error, "durable commit succeeded") {
		t.Fatalf("expected an error after a committed charge: %+v", result)
	}
	check := func(daemon *Daemon) {
		t.Helper()
		result := daemon.Admin(context.Background(), AdminRequest{Action: "status"}, now)
		if !result.OK || result.Status == nil {
			t.Fatal("root status must remain readable for reconciliation")
		}
		v := result.Status
		if v.RecoveryProtocol != RecoveryProtocol || v.DataError == "" || !v.Meta.Review || v.AuthAllowed || v.Healthy || v.Adjustment != GiB {
			t.Fatalf("root status hid the committed-but-unwitnessed state: %+v", v)
		}
		body, err := json.Marshal(v)
		must(t, err)
		if !strings.Contains(string(body), `"data_error":`) {
			t.Fatal("recovery preflight cannot see the witness error")
		}
	}
	check(d)
	must(t, s.Close())
	must(t, os.Remove(cfg.Anchor))
	must(t, os.WriteFile(cfg.Anchor, oldWitness, 0600))
	s2, err := OpenStore(cfg, secrets, false, now)
	must(t, err)
	defer s2.Close()
	c.Store = s2
	d = NewDaemon(s2, c, cfg, secrets)
	check(d) // Reopening does not hide the witness mismatch or erase the charge.
	must(t, s2.Arm(map[string]int64{"owner": 0, "friend": 0}, "test verified prior increment already committed", true, now))
	v, err := d.Status(now)
	must(t, err)
	if v.DataError != "" || v.Adjustment != GiB {
		t.Fatal("explicit zero-increment witness repair lost or duplicated usage")
	}
}

func TestRecoveryMustReviewSuccessfulPrepareStopTail(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	id := session(t, s, "friend", now)
	api := &fakeAPI{shot: Snapshot{Traffic: map[string]Counter{id: {Tx: 40, Rx: 60}}, Online: map[string]int{id: 1}}}
	c := &Collector{Store: s, API: api, Generation: func() (string, error) { return "generation-1", nil }}
	d := NewDaemon(s, c, cfg, secrets)
	result := d.Admin(context.Background(), AdminRequest{Action: "prepare-stop"}, now.Add(time.Second))
	if !result.OK {
		t.Fatalf("prepare-stop failed: %+v", result)
	}
	// Existing connections are not closed by the auth hold. Their last bytes
	// cannot be presumed zero merely because prepare-stop returned success.
	api.shot.Traffic[id] = Counter{Tx: 20, Rx: 30}
	if err := c.Collect(context.Background(), now.Add(2*time.Second)); !errors.Is(err, ErrReview) {
		t.Fatalf("expected review hold after the final sample, got %v", err)
	}
	if user(t, s, "friend", now).Used != 100 {
		t.Fatal("test did not leave an uncommitted 50-byte tail")
	}
	must(t, s.Arm(map[string]int64{"owner": 0, "friend": 50}, "test explicit reviewed in-flight tail", true, now.Add(3*time.Second)))
	must(t, s.Apply("generation-2", Snapshot{Traffic: map[string]Counter{}, Online: map[string]int{}}, now.Add(4*time.Second)))
	if user(t, s, "friend", now).Used != 150 {
		t.Fatal("reviewed residual increment was not retained")
	}
}

func TestSamplingPeriodIsNotMaximumUncommittedWindow(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	healthyGuard(t, cfg, now, false)
	c := &Collector{Store: s, API: &fakeAPI{readErr: errors.New("test statistics timeout")}, Generation: func() (string, error) { return "generation-1", nil }}
	d := NewDaemon(s, c, cfg, secrets)
	for i := 1; i <= 3; i++ {
		if c.Collect(context.Background(), now.Add(time.Duration(i)*5*time.Second)) == nil {
			t.Fatal("fixture must fail collection")
		}
	}
	v, err := d.Status(now.Add(15 * time.Second))
	must(t, err)
	if v.Meta.LastSample != now.UnixNano() || v.StatsError == "" || !v.Healthy || !v.AuthAllowed {
		t.Fatal("fixture must model a tolerated statistics outage longer than one tick")
	}
	// Preserve the deliberate soft-limit policy; recovery must not derive a
	// fixed 1-GiB upper bound from its nominal five-second ticker.
	if eFresh(v.Meta, now.Add(31*time.Second)) {
		t.Fatal("shared statistics freshness boundary was weakened")
	}
}

func TestRevokedPendingSessionStillNeedsExplicitRecoveryCharge(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	_ = session(t, s, "friend", now)
	c := &Collector{Store: s, API: &fakeAPI{kickErr: errors.New("test kick timeout")}, Generation: func() (string, error) { return "generation-1", nil }}
	d := NewDaemon(s, c, cfg, secrets)
	result := d.Admin(context.Background(), AdminRequest{Action: "revoke", Name: "friend"}, now)
	u := user(t, s, "friend", now)
	if !result.Incomplete || !u.Revoked || u.Pending != 1 {
		t.Fatal("revoked must not mean that every stored session is already closed")
	}
	if err := s.Arm(map[string]int64{"owner": 0}, "test incomplete recovery coverage", true, now); err == nil {
		t.Fatal("revoked account omitted from recovery charges")
	}
	must(t, s.Arm(map[string]int64{"owner": 0, "friend": 50}, "test reviewed revoked-session residual", true, now))
	u = user(t, s, "friend", now)
	if !u.Revoked || u.Adjustment != 50 {
		t.Fatal("recovery must retain both the tombstone and reviewed usage")
	}
}

func TestArmRejectsChangedExpectedBillingMonthBeforeMutation(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	healthyGuard(t, cfg, now, false)
	c := &Collector{Store: s, API: &fakeAPI{}, Generation: func() (string, error) { return "", errors.New("test core stopped") }}
	d := NewDaemon(s, c, cfg, secrets)
	result := d.Admin(context.Background(), AdminRequest{Action: "arm", Charges: map[string]int64{"owner": GiB, "friend": 0}, Reason: "test prior month recovery plan", AcceptGap: true, ExpectedMonth: "2026-09"}, now)
	if result.OK || !strings.Contains(result.Error, "billing month changed") || user(t, s, "owner", now).Adjustment != 0 {
		t.Fatal("old-month recovery plan reached the ledger mutation")
	}
}

func TestHealthControlLockIsBoundedPrivateAndStable(t *testing.T) {
	_, cfg, _, _ := testStore(t)
	first, err := LockHealthControl(cfg, 0)
	must(t, err)
	defer first.Close()
	if _, err := LockHealthControl(cfg, 0); !errors.Is(err, ErrHealthControlBusy) {
		t.Fatalf("concurrent control did not fail closed: %v", err)
	}
	st, err := first.Stat()
	must(t, err)
	if st.Mode().Perm() != 0600 {
		t.Fatal("lock is not private")
	}
	must(t, first.Close())
	second, err := LockHealthControl(cfg, 0)
	must(t, err)
	defer second.Close()
	st2, err := second.Stat()
	must(t, err)
	if !os.SameFile(st, st2) {
		t.Fatal("release replaced the inode and could split waiting processes")
	}
}

func TestHealthControlRejectsUnsafeLockFilesAndConfigCollisions(t *testing.T) {
	for _, kind := range []string{"symlink", "directory", "fifo", "public"} {
		t.Run(kind, func(t *testing.T) {
			_, cfg, _, _ := testStore(t)
			path := cfg.HealthBlockedFile + ".lock"
			switch kind {
			case "symlink":
				target := filepath.Join(t.TempDir(), "unchanged")
				must(t, os.WriteFile(target, []byte("fixture"), 0600))
				must(t, os.Symlink(target, path))
			case "directory":
				must(t, os.Mkdir(path, 0700))
			case "fifo":
				makeFIFO(t, path)
			case "public":
				must(t, os.WriteFile(path, nil, 0644))
			}
			control, err := LockHealthControl(cfg, 0)
			if control != nil {
				control.Close()
			}
			if err == nil {
				t.Fatal("unsafe lock file accepted")
			}
		})
	}
	_, cfg, _, _ := testStore(t)
	for _, suffix := range []string{".lock", ".recovery.lock"} {
		c := cfg
		c.SecretsFile = cfg.HealthBlockedFile + suffix
		if c.Validate() == nil {
			t.Fatal("derived control path collides with a state/secret file")
		}
	}
}

func TestHealthRunCannotCarryOldDecisionAcrossArm(t *testing.T) {
	s, cfg, secrets, _ := testStore(t)
	now := time.Now()
	healthyGuard(t, cfg, now, false)
	must(t, s.Hold("test maintenance hold", now))
	c := &Collector{Store: s, API: &fakeAPI{}, Generation: func() (string, error) { return "", errors.New("test core stopped") }}
	d := NewDaemon(s, c, cfg, secrets)
	must(t, d.publish(now))
	calls := mockSystemctl(t)
	// Health reads the old witness, then waits for a controlled guard response.
	// Its control lock must already be held during that entire decision.
	workerCfg := cfg
	workerCfg.GuardStatusFile = filepath.Join(t.TempDir(), "guard.fifo")
	makeFIFO(t, workerCfg.GuardStatusFile)
	done := make(chan error, 1)
	go func() { done <- HealthCheck(workerCfg, secrets, "run") }()
	writer := openFIFOWriter(t, workerCfg.GuardStatusFile)
	defer writer.Close()
	control, lockErr := LockHealthControl(cfg, 0)
	if control != nil {
		control.Close()
	}
	guard, err := os.ReadFile(cfg.GuardStatusFile)
	must(t, err)
	_, err = writer.Write(guard)
	must(t, err)
	must(t, writer.Close())
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("old unhealthy witness must trigger the protected shutdown")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bounded health fixture did not finish")
	}
	if !errors.Is(lockErr, ErrHealthControlBusy) {
		t.Fatal("arm could acquire control while an old health decision was in flight")
	}
	control, err = LockHealthControl(cfg, 0)
	must(t, err)
	result := d.Admin(context.Background(), AdminRequest{Action: "arm", Charges: map[string]int64{"owner": 0, "friend": 0}, Reason: "test reviewed recovery after health shutdown", AcceptGap: true}, time.Now())
	if !result.OK {
		t.Fatalf("arm failed: %+v", result)
	}
	must(t, os.Remove(cfg.HealthBlockedFile)) // Root CLI's latch clear, under lock.
	must(t, control.Close())
	before, err := os.ReadFile(calls)
	must(t, err)
	must(t, HealthCheck(cfg, secrets, "gate"))
	must(t, HealthCheck(cfg, secrets, "run"))
	after, err := os.ReadFile(calls)
	must(t, err)
	if string(before) != string(after) || sharedBlocked(cfg.HealthBlockedFile) {
		t.Fatal("post-arm health reused the old decision and stopped the new generation")
	}
}

func TestHealthLockStorageFailureStillStopsHY2(t *testing.T) {
	_, cfg, secrets, _ := testStore(t)
	must(t, os.Mkdir(cfg.HealthBlockedFile+".lock", 0700))
	calls := mockSystemctl(t)
	if err := HealthCheck(cfg, secrets, "run"); err == nil {
		t.Fatal("invalid lock was silently accepted")
	}
	body, err := os.ReadFile(calls)
	must(t, err)
	if !strings.Contains(string(body), "stop hysteria-server.service") || strings.Contains(string(body), "sing-box") || !sharedBlocked(cfg.HealthBlockedFile) {
		t.Fatal("lock storage failure disabled shared-fault protection")
	}
}

func TestHealthGateDoesNotCreateOrAcquireControlLock(t *testing.T) {
	s, cfg, secrets, _ := testStore(t)
	now := time.Now()
	healthyGuard(t, cfg, now, false)
	must(t, s.Arm(map[string]int64{"owner": 0, "friend": 0}, "test empty held bootstrap", true, now))
	c := &Collector{Store: s, API: &fakeAPI{}, Generation: func() (string, error) { return "", errors.New("test core stopped") }}
	d := NewDaemon(s, c, cfg, secrets)
	must(t, d.publish(now))
	must(t, HealthCheck(cfg, secrets, "gate"))
	if _, err := os.Lstat(cfg.HealthBlockedFile + ".lock"); !os.IsNotExist(err) {
		t.Fatal("read-only gate created a control lock")
	}
	control, err := LockHealthControl(cfg, 0)
	must(t, err)
	defer control.Close()
	must(t, HealthCheck(cfg, secrets, "gate")) // No deadlock with systemd jobs.
}
