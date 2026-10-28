package account

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSessionTotalsLegacyJSONSurvivesPruningAndReopen(t *testing.T) {
	s, cfg, secrets, now := testStore(t)
	for i := 0; i < 12; i++ {
		at := now.Add(time.Duration(i) * time.Minute)
		must(t, s.Apply("generation-1", emptySnapshot(), at))
		id := session(t, s, "owner", at)
		must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{id: {Tx: 1}}, Online: map[string]int{id: 1}}, at.Add(time.Second)))
		must(t, s.Apply("generation-1", emptySnapshot(), at.Add(2*time.Second)))
	}
	at := now.Add(time.Hour)
	must(t, s.Apply("generation-1", emptySnapshot(), at))
	var rows int
	must(t, s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&rows))
	if rows != 0 {
		t.Fatal("test did not reclaim all dead rows")
	}
	must(t, s.Close())
	s2, err := OpenStore(cfg, secrets, false, at)
	must(t, err)
	defer s2.Close()
	u := user(t, s2, "owner", at)
	if u.Sessions != 12 || u.ActiveSessions != 0 || u.Used != 12 {
		t.Fatalf("cumulative/active/usage confused: %+v", u)
	}
	v, err := s2.View(at)
	must(t, err)
	b, err := json.Marshal(v)
	must(t, err)
	for _, want := range []string{`"sessions_generation":12`, `"active_sessions":0`, `"session_capacity_mode":"active"`} {
		if !bytes.Contains(b, []byte(want)) {
			t.Fatalf("missing compatible JSON: %s", want)
		}
	}
	// Renaming and month rollover must not reset the core-generation counter.
	must(t, s2.ChangeUser("rename", "owner", "renamed", 0, at))
	later := at.AddDate(0, 1, 0)
	must(t, s2.Apply("generation-1", emptySnapshot(), later))
	if user(t, s2, "renamed", later).Sessions != 12 {
		t.Fatal("rename/month rollover reset cumulative issued sessions")
	}
	must(t, s2.Hold("new reviewed test core generation", later))
	must(t, s2.Arm(map[string]int64{"renamed": 0, "friend": 0}, "new reviewed test core generation", true, later))
	must(t, s2.Apply("generation-2", emptySnapshot(), later))
	if user(t, s2, "renamed", later).Sessions != 0 {
		t.Fatal("new generation did not reset cumulative count")
	}
}

func TestSessionTotalsAuthCounterAndSessionCommitTogether(t *testing.T) {
	s, _, _, now := testStore(t)
	s.beforeCommit = func() error { return errors.New("fixture transaction rollback") }
	if _, err := s.Authenticate(strings.Repeat("o", 32), "generation-1", now); err == nil {
		t.Fatal("fixture did not fail")
	}
	s.beforeCommit = nil
	if u := user(t, s, "owner", now); u.Sessions != 0 || u.ActiveSessions != 0 {
		t.Fatal("rollback persisted only one half of issuance")
	}
	_ = session(t, s, "owner", now)
	if u := user(t, s, "owner", now); u.Sessions != 1 || u.ActiveSessions != 1 {
		t.Fatal("committed auth did not increment both counts")
	}
	must(t, s.mutate(func(tx *sql.Tx, _ Meta) error {
		_, err := tx.Exec(`UPDATE session_totals SET issued=? WHERE account_id=(SELECT id FROM accounts WHERE name='owner')`, int64(math.MaxInt64))
		return err
	}))
	if _, err := s.Authenticate(strings.Repeat("o", 32), "generation-1", now); err == nil {
		t.Fatal("issued counter overflow allowed")
	}
	var rows int
	must(t, s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&rows))
	if rows != 1 {
		t.Fatal("counter failure leaked a newly signed session")
	}
}

// This fixture reconstructs the exact v1 schema, not a production database.
func legacyCounterFixture(t *testing.T) (Config, Secrets, time.Time) {
	t.Helper()
	s, cfg, secrets, now := testStore(t)
	id := session(t, s, "owner", now)
	must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{id: {Tx: 7, Rx: 9}}, Online: map[string]int{}}, now))
	_ = session(t, s, "owner", now)
	must(t, s.ChangeUser("disable", "friend", "", 0, now))
	must(t, s.Hold("planned fixture stop before schema upgrade", now))
	must(t, s.mutate(func(tx *sql.Tx, _ Meta) error {
		_, err := tx.Exec(`DROP TABLE session_totals; PRAGMA user_version=1;`)
		return err
	}))
	must(t, s.Close())
	must(t, os.Remove(deltaBaselinePath(cfg)))
	return cfg, secrets, now
}

func TestSessionTotalsMigrationRequiresExplicitReviewAndPreservesLedger(t *testing.T) {
	cfg, secrets, now := legacyCounterFixture(t)
	if s, err := OpenStore(cfg, secrets, false, now); err == nil {
		s.Close()
		t.Fatal("daemon silently migrated v1")
	}
	before, err := os.ReadFile(cfg.Anchor)
	must(t, err)
	for _, counts := range []map[string]int64{nil, {"owner": 2}, {"owner": 1, "friend": 0}, {"owner": 2, "friend": 0, "extra": 0}, {"owner": 2, "friend": -1}} {
		if err := MigrateSessionCounters(cfg, secrets, counts, now); err == nil {
			t.Fatal("incomplete/mismatched counts accepted")
		}
		after, err := os.ReadFile(cfg.Anchor)
		must(t, err)
		if !bytes.Equal(before, after) {
			t.Fatal("rejected migration mutated the witness")
		}
	}
	must(t, MigrateSessionCounters(cfg, secrets, map[string]int64{"owner": 2, "friend": 0}, now))
	s, err := OpenStore(cfg, secrets, false, now)
	must(t, err)
	u := user(t, s, "owner", now)
	f := user(t, s, "friend", now)
	if u.Sessions != 2 || u.Tx != 7 || u.Rx != 9 || u.Adjustment != 0 || !f.Disabled || f.Sessions != 0 {
		t.Fatal("schema migration changed ledger, flags or counts")
	}
	password, err := s.Export("owner")
	must(t, err)
	if password != strings.Repeat("o", 32) {
		t.Fatal("schema migration changed the credential")
	}
	var version int
	must(t, s.db.QueryRow(`PRAGMA user_version`).Scan(&version))
	if version != 2 {
		t.Fatal("migration did not set explicit schema version")
	}
	must(t, s.Close())
	if err := MigrateSessionCounters(cfg, secrets, map[string]int64{"owner": 2, "friend": 0}, now); err == nil {
		t.Fatal("migration silently replayed")
	}
}

func TestSessionTotalsMigrationRejectsDeltaOrInterruptedEvidence(t *testing.T) {
	for _, suffix := range []string{".pull", ".delta"} {
		cfg, secrets, now := legacyCounterFixture(t)
		must(t, os.WriteFile(cfg.Database+suffix, []byte("fixture"), 0600))
		if err := MigrateSessionCounters(cfg, secrets, map[string]int64{"owner": 2, "friend": 0}, now); err == nil {
			t.Fatal("potentially pruned/interrupted v1 accepted as complete history")
		}
	}
}

func TestSessionTotalsMigrationRollbackAndWitnessFailure(t *testing.T) {
	for _, kind := range []string{"transaction", "witness"} {
		t.Run(kind, func(t *testing.T) {
			cfg, secrets, now := legacyCounterFixture(t)
			if kind == "transaction" {
				db, err := sql.Open("sqlite", cfg.Database)
				must(t, err)
				_, err = db.Exec(`CREATE TRIGGER migration_abort BEFORE INSERT ON audit WHEN NEW.action='schema.session-totals' BEGIN SELECT RAISE(ABORT,'fixture migration rollback'); END;`)
				must(t, err)
				must(t, db.Close())
			} else {
				must(t, os.WriteFile(cfg.Anchor, []byte(`{"db_id":"wrong","revision":1}`), 0600))
			}
			witness, err := os.ReadFile(cfg.Anchor)
			must(t, err)
			if err := MigrateSessionCounters(cfg, secrets, map[string]int64{"owner": 2, "friend": 0}, now); err == nil {
				t.Fatal("migration failure was ignored")
			}
			after, err := os.ReadFile(cfg.Anchor)
			must(t, err)
			if !bytes.Equal(witness, after) {
				t.Fatal("failed migration changed witness")
			}
			db, err := sql.Open("sqlite", cfg.Database)
			must(t, err)
			defer db.Close()
			var version, tables int
			must(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
			must(t, db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='session_totals'`).Scan(&tables))
			if version != 1 || tables != 0 {
				t.Fatal("failed migration partially committed schema changes")
			}
		})
	}
}

func TestSessionTotalsMissingRowIsUnknownNotZero(t *testing.T) {
	s, _, _, now := testStore(t)
	must(t, s.mutate(func(tx *sql.Tx, _ Meta) error {
		_, err := tx.Exec(`DELETE FROM session_totals WHERE account_id=(SELECT id FROM accounts WHERE name='owner')`)
		return err
	}))
	if _, err := s.View(now); err == nil {
		t.Fatal("missing cumulative history was presented as zero")
	}
	if _, err := s.Authenticate(strings.Repeat("o", 32), "generation-1", now); err == nil {
		t.Fatal("auth succeeded without the persistent counter row")
	}
}
