package account

import (
	"database/sql"
	"errors"
	"os"
	"time"
)

// MigrateSessionCounters is an explicit offline v1 -> v2 migration. The caller
// must confirm the core is stopped and review cumulative counts for EVERY
// account. Daemon startup never migrates implicitly. Run as the existing state
// owner so SQLite/witness files retain their private ownership.
func MigrateSessionCounters(c Config, secrets Secrets, expected map[string]int64, now time.Time) error {
	if len(expected) == 0 {
		return errors.New("reviewed cumulative session counts required")
	}
	if _, err := ReadGeneration(c.GenerationFile); err == nil {
		return errors.New("stop the core before migrating session counters")
	}
	s, err := openStore(c, secrets, false, expected, now)
	if err != nil {
		return err
	}
	return s.Close()
}

func (s *Store) migrateSessionTotals(expected map[string]int64, now time.Time) error {
	// A v1 delta candidate may already have pruned rows. Never reconstruct a
	// claimed cumulative count from its remaining rows without provenance.
	for _, path := range []string{s.cfg.Database + ".pull", deltaBaselinePath(s.cfg)} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return errors.New("v1 pull/delta evidence present or unreadable: migration requires reconciliation")
		}
	}
	return s.mutate(func(tx *sql.Tx, m Meta) error {
		if !m.Hold || !m.Review || m.ArmUntil != 0 {
			return errors.New("migration requires a held, reviewed stop; never migrate a live/armed ledger")
		}
		rows, err := tx.Query(`SELECT a.name,COUNT(s.id) FROM accounts a LEFT JOIN sessions s ON s.account_id=a.id AND s.generation=? GROUP BY a.id`, m.Generation)
		if err != nil {
			return err
		}
		count := 0
		for rows.Next() {
			var name string
			var retained int64
			if err := rows.Scan(&name, &retained); err != nil {
				rows.Close()
				return err
			}
			n, exists := expected[name]
			if !exists || n < 0 || n != retained {
				rows.Close()
				return errors.New("reviewed cumulative counts differ from retained v1 history")
			}
			count++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if count != len(expected) {
			return errors.New("review must cover exactly every account, including revoked accounts")
		}
		if _, err := tx.Exec(sessionTotalsSchema); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO session_totals(account_id,generation,issued)
SELECT a.id,?,COUNT(s.id) FROM accounts a LEFT JOIN sessions s ON s.account_id=a.id AND s.generation=? GROUP BY a.id;
PRAGMA user_version=2;`, m.Generation, m.Generation); err != nil {
			return err
		}
		return audit(tx, now, "schema.session-totals", "", "v1->v2; reviewed cumulative counts retained; no usage or credentials changed")
	})
}
