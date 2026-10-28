package account

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

var ErrReview = errors.New("accounting requires explicit verification")
var ErrDenied = errors.New("authentication denied")

const schema = `
CREATE TABLE meta (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1), db_id TEXT NOT NULL,
 revision INTEGER NOT NULL, generation TEXT NOT NULL DEFAULT '',
 last_sample INTEGER NOT NULL DEFAULT 0, started INTEGER NOT NULL,
 review INTEGER NOT NULL DEFAULT 1, reason TEXT NOT NULL DEFAULT 'initial baseline not approved',
 hold INTEGER NOT NULL DEFAULT 1, arm_until INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE accounts (
 id TEXT PRIMARY KEY, name TEXT UNIQUE NOT NULL, digest BLOB UNIQUE NOT NULL,
 credential BLOB NOT NULL, quota INTEGER NOT NULL CHECK(quota>0),
 disabled INTEGER NOT NULL DEFAULT 0, revoked INTEGER NOT NULL DEFAULT 0, created INTEGER NOT NULL
);
CREATE TABLE names (name TEXT PRIMARY KEY, account_id TEXT NOT NULL REFERENCES accounts(id));
CREATE TABLE credentials (digest BLOB PRIMARY KEY, account_id TEXT NOT NULL REFERENCES accounts(id));
CREATE TABLE months (
 account_id TEXT NOT NULL REFERENCES accounts(id), month TEXT NOT NULL,
 tx INTEGER NOT NULL DEFAULT 0 CHECK(tx>=0), rx INTEGER NOT NULL DEFAULT 0 CHECK(rx>=0),
 adjustment INTEGER NOT NULL DEFAULT 0 CHECK(adjustment>=0), exhausted INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(account_id,month)
);
CREATE TABLE days (
 account_id TEXT NOT NULL REFERENCES accounts(id), day TEXT NOT NULL,
 tx INTEGER NOT NULL DEFAULT 0, rx INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(account_id,day)
);
CREATE TABLE sessions (
 id TEXT PRIMARY KEY, account_id TEXT NOT NULL REFERENCES accounts(id), generation TEXT NOT NULL,
 created INTEGER NOT NULL, tx INTEGER NOT NULL DEFAULT 0, rx INTEGER NOT NULL DEFAULT 0,
 has_counter INTEGER NOT NULL DEFAULT 0, online INTEGER NOT NULL DEFAULT 0,
 seen_online INTEGER NOT NULL DEFAULT 0, blocked INTEGER NOT NULL DEFAULT 0,
 kick_state TEXT NOT NULL DEFAULT '', attempts INTEGER NOT NULL DEFAULT 0,
 last_attempt INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX session_account_generation ON sessions(account_id,generation);
CREATE INDEX session_generation ON sessions(generation);
CREATE TABLE audit (id INTEGER PRIMARY KEY, at INTEGER NOT NULL, action TEXT NOT NULL, account TEXT NOT NULL, detail TEXT NOT NULL);
CREATE TABLE gaps (id INTEGER PRIMARY KEY, at INTEGER NOT NULL, generation TEXT NOT NULL, reason TEXT NOT NULL);
PRAGMA user_version=2;
` + sessionTotalsSchema

// Exactly one counter row per account; never pruned with individual sessions.
const sessionTotalsSchema = `
CREATE TABLE session_totals (
 account_id TEXT PRIMARY KEY REFERENCES accounts(id), generation TEXT NOT NULL,
 issued INTEGER NOT NULL CHECK(typeof(issued)='integer' AND issued>=0)
);
`

type Meta struct {
	DBID       string `json:"-"`
	Revision   int64  `json:"-"`
	Generation string `json:"generation"`
	LastSample int64  `json:"last_sample"`
	Started    int64  `json:"started"`
	Review     bool   `json:"review"`
	Reason     string `json:"reason"`
	Hold       bool   `json:"hold"`
	ArmUntil   int64  `json:"arm_until"`
}

type Witness struct {
	DBID     string `json:"db_id"`
	Revision int64  `json:"revision"`
}

type Store struct {
	mu       sync.Mutex
	db       *sql.DB
	cfg      Config
	authKey  []byte
	aead     cipher.AEAD
	expected Witness
	fatal    error
	// Only orphaned/ambiguous pulls close this gate, not live transient markers.
	pullUnresolved bool
	// Generation whose empty delta baseline was committed (persisted sidecar).
	deltaGen string
	lockFile *os.File
	identity os.FileInfo
	// Offline timestamps are conservative and intentionally not persisted.
	offlineSince map[string]*offlineSession
	offlineQueue offlineHeap
	pruneCursor  string
	// Only tests inject a failure inside the real SQLite transaction.
	beforeCommit func() error
}

func OpenStore(c Config, secrets Secrets, initialize bool, now time.Time) (*Store, error) {
	return openStore(c, secrets, initialize, nil, now)
}

func openStore(c Config, secrets Secrets, initialize bool, migrateCounts map[string]int64, now time.Time) (*Store, error) {
	lock, err := os.OpenFile(c.Database+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("another accountd instance holds the database lock")
	}
	owned := false
	defer func() {
		if !owned {
			lock.Close()
		}
	}()
	if _, err := os.Lstat(c.Database); err == nil && initialize {
		return nil, errors.New("database already exists; never initialize over state")
	}
	if !initialize {
		if st, err := os.Lstat(c.Database); err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
			return nil, errors.New("private database missing; restore/verification required, never auto-create")
		}
	} else {
		if _, err := os.Stat(c.Anchor); !os.IsNotExist(err) {
			return nil, errors.New("restore witness already exists or cannot be checked")
		}
		f, err := os.OpenFile(c.Database, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	}
	key, err := base64.StdEncoding.DecodeString(secrets.AuthKey)
	if err != nil || len(key) != 32 {
		return nil, errors.New("invalid auth key")
	}
	encKey, err := base64.StdEncoding.DecodeString(secrets.CredentialKey)
	if err != nil || len(encKey) != 32 {
		return nil, errors.New("invalid credential key")
	}
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: c.Database}
	q := url.Values{"mode": {"rw"}, "_pragma": {"foreign_keys(1)", "busy_timeout(2000)"}}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	identity, err := os.Stat(c.Database)
	if err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db, cfg: c, authKey: key, aead: aead, lockFile: lock, identity: identity}
	fail := func(e error) (*Store, error) { db.Close(); return nil, e }
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA wal_autocheckpoint=128; PRAGMA journal_size_limit=4194304;`); err != nil {
		return fail(err)
	}
	if initialize {
		id, err := randomToken(24)
		if err != nil {
			return fail(err)
		}
		tx, err := db.Begin()
		if err != nil {
			return fail(err)
		}
		if _, err = tx.Exec(schema); err == nil {
			_, err = tx.Exec(`INSERT INTO meta(singleton,db_id,revision,started) VALUES(1,?,1,?)`, id, now.Unix())
		}
		if err != nil {
			tx.Rollback()
			return fail(err)
		}
		if err := tx.Commit(); err != nil {
			return fail(err)
		}
		s.expected = Witness{id, 1}
		if err := writeJSON(c.Anchor, s.expected, 0600); err != nil {
			return fail(err)
		}
	} else {
		var version int
		if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || (version != 2 && !(version == 1 && migrateCounts != nil)) {
			return fail(errors.New("unsupported database schema: v1 requires explicit offline migrate-sessions"))
		}
		if migrateCounts != nil && version != 1 {
			return fail(errors.New("migration requires schema v1; inspect prior result, never replay migration"))
		}
		var check string
		if err := db.QueryRow(`PRAGMA quick_check`).Scan(&check); err != nil || check != "ok" {
			return fail(errors.New("SQLite integrity check failed"))
		}
		m, err := readMeta(db)
		if err != nil {
			return fail(err)
		}
		s.expected = Witness{m.DBID, m.Revision}
		w, err := readWitness(c.Anchor)
		if err != nil || w != s.expected {
			// Keep the service/page available but never authenticate or overwrite the witness.
			s.fatal = errors.New("database/witness mismatch: possible restore or interrupted durable commit")
		}
	}
	if migrateCounts != nil {
		if err := s.migrateSessionTotals(migrateCounts, now); err != nil {
			return fail(err)
		}
	}
	if _, err := os.Lstat(c.Database + ".pull"); !os.IsNotExist(err) {
		s.pullUnresolved = true
	}
	s.deltaGen = readDeltaBaseline(c)
	if err := s.initRetention(now); err != nil {
		return fail(err)
	}
	owned = true
	return s, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.db.Close()
	lockErr := s.lockFile.Close()
	if err != nil {
		return err
	}
	return lockErr
}

type queryRower interface{ QueryRow(string, ...any) *sql.Row }

func readMeta(q queryRower) (Meta, error) {
	var m Meta
	err := q.QueryRow(`SELECT db_id,revision,generation,last_sample,started,review,reason,hold,arm_until FROM meta WHERE singleton=1`).Scan(&m.DBID, &m.Revision, &m.Generation, &m.LastSample, &m.Started, &m.Review, &m.Reason, &m.Hold, &m.ArmUntil)
	return m, err
}
func readWitness(path string) (Witness, error) {
	var w Witness
	f, err := os.Open(path)
	if err != nil {
		return w, err
	}
	defer f.Close()
	err = DecodeJSON(f, &w, 1024)
	return w, err
}

func (s *Store) Meta() (Meta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := readMeta(s.db)
	if s.fatal != nil {
		m.Review = true
		m.Reason = s.fatal.Error()
		return m, s.fatal
	}
	return s.pullMeta(m), err
}

// An orphan is reviewable evidence, not a witness/storage fatal error. Keep an
// explicitly armed hold usable across restart; it still cannot authenticate and
// only the collector can validate a different, empty generation and clear it.
func (s *Store) pullMeta(m Meta) Meta {
	armed := m.Hold && !m.Review && m.ArmUntil > 0 && m.LastSample == 0
	if s.pullUnresolved && !armed {
		m.Review, m.Hold, m.Reason = true, true, interruptedPull
	} else if m.Generation != "" && !s.deltaBaselinedLocked(m.Generation) && !armed {
		m.Review, m.Hold, m.Reason = true, true, deltaGateReason
	}
	return m
}

func (s *Store) mutate(fn func(*sql.Tx, Meta) error) error { return s.commit(fn, false) }

func (s *Store) commit(fn func(*sql.Tx, Meta) error, repair bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commitLocked(fn, repair)
}

func (s *Store) commitLocked(fn func(*sql.Tx, Meta) error, repair bool) error {
	if s.fatal != nil && !repair {
		return s.fatal
	}
	st, statErr := os.Lstat(s.cfg.Database)
	if statErr != nil || !st.Mode().IsRegular() || !os.SameFile(s.identity, st) {
		s.fatal = errors.New("database file disappeared or was replaced; stop HY2 and reopen accountd before verification")
		return s.fatal
	}
	for _, p := range []string{s.cfg.Database, s.cfg.Database + "-wal"} {
		if st, err := os.Stat(p); err == nil && st.Size() > 128<<20 {
			s.fatal = errors.New("database storage capacity exceeded")
			return s.fatal
		}
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	m, err := readMeta(tx)
	if err != nil {
		return err
	}
	w, err := readWitness(s.cfg.Anchor)
	if !repair && (err != nil || w != s.expected || m.DBID != s.expected.DBID || m.Revision != s.expected.Revision) {
		s.fatal = errors.New("database/restore witness changed unexpectedly")
		return s.fatal
	}
	if err := fn(tx, m); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE meta SET revision=revision+1 WHERE singleton=1;
DELETE FROM audit WHERE id NOT IN (SELECT id FROM audit ORDER BY id DESC LIMIT 1000);
DELETE FROM gaps WHERE id NOT IN (SELECT id FROM gaps ORDER BY id DESC LIMIT 100);`); err != nil {
		return err
	}
	if s.beforeCommit != nil {
		if err := s.beforeCommit(); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.expected = Witness{m.DBID, m.Revision + 1}
	if err := writeJSON(s.cfg.Anchor, s.expected, 0600); err != nil {
		s.fatal = errors.New("durable commit succeeded but restore witness could not be saved")
		return s.fatal
	}
	if repair {
		s.fatal = nil
	}
	return nil
}

func audit(tx *sql.Tx, now time.Time, action, account, detail string) error {
	_, err := tx.Exec(`INSERT INTO audit(at,action,account,detail) VALUES(?,?,?,?)`, now.Unix(), action, account, detail)
	return err
}

func (s *Store) digest(password string) []byte {
	h := hmac.New(sha256.New, s.authKey)
	h.Write([]byte(password))
	return h.Sum(nil)
}
func (s *Store) encrypt(id, password string) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, []byte(password), []byte(id)), nil
}
func (s *Store) decrypt(id string, data []byte) (string, error) {
	n := s.aead.NonceSize()
	if len(data) < n {
		return "", errors.New("corrupt credential")
	}
	b, err := s.aead.Open(nil, data[:n], data[n:], []byte(id))
	if err != nil {
		return "", errors.New("credential verification failed")
	}
	return string(b), nil
}

func allocatedPool(tx *sql.Tx, month, exceptID string) (int64, error) {
	// Already-consumed bytes cannot be reallocated by revoking an account or
	// lowering its quota. This is a conservative allocation pool, not a bill.
	var n int64
	err := tx.QueryRow(`SELECT COALESCE(SUM(CASE WHEN a.revoked=1
 THEN COALESCE(m.tx+m.rx+m.adjustment,0)
 ELSE MAX(a.quota,COALESCE(m.tx+m.rx+m.adjustment,0)) END),0)
 FROM accounts a LEFT JOIN months m ON m.account_id=a.id AND m.month=? WHERE a.id<>?`, month, exceptID).Scan(&n)
	return n, err
}

// A revoked name is a permanent tombstone; no delete/re-add quota bypass exists.
type NewUser struct {
	Name     string `json:"name"`
	Password string `json:"password,omitempty"`
	Quota    int64  `json:"quota_bytes"`
}

func (s *Store) AddUsers(users []NewUser, now time.Time) error {
	if len(users) == 0 || len(users) > s.cfg.MaxAccounts {
		return errors.New("invalid import size")
	}
	for i := range users {
		if err := requireName(users[i].Name); err != nil {
			return err
		}
		if users[i].Quota <= 0 || users[i].Quota > s.cfg.QuotaPoolBytes {
			return errors.New("explicit positive quota within pool required")
		}
		if users[i].Password == "" {
			p, err := randomToken(32)
			if err != nil {
				return err
			}
			users[i].Password = p
		}
		if len(users[i].Password) < 24 || len(users[i].Password) > 256 {
			return errors.New("credential must contain 24..256 bytes")
		}
	}
	return s.mutate(func(tx *sql.Tx, m Meta) error {
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&count); err != nil {
			return err
		}
		month, _ := billingTime(now)
		pool, err := allocatedPool(tx, month, "")
		if err != nil {
			return err
		}
		if count+len(users) > s.cfg.MaxAccounts {
			return errors.New("account capacity reached (including revoked tombstones)")
		}
		for _, u := range users {
			if pool > s.cfg.QuotaPoolBytes-u.Quota {
				return errors.New("allocated quotas plus consumed retired-account usage exceed explicit pool")
			}
			pool += u.Quota
		}
		for _, u := range users {
			var existing int
			if err := tx.QueryRow(`SELECT (SELECT COUNT(*) FROM names WHERE name=?)+(SELECT COUNT(*) FROM credentials WHERE digest=?)`, u.Name, s.digest(u.Password)).Scan(&existing); err != nil {
				return err
			}
			if existing != 0 {
				return errors.New("name or credential already exists; use quota/enable/export, never recreate")
			}
			id, err := randomToken(24)
			if err != nil {
				return err
			}
			enc, err := s.encrypt(id, u.Password)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO accounts(id,name,digest,credential,quota,created) VALUES(?,?,?,?,?,?)`, id, u.Name, s.digest(u.Password), enc, u.Quota, now.UnixNano()); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO names(name,account_id) VALUES(?,?)`, u.Name, id); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO credentials(digest,account_id) VALUES(?,?)`, s.digest(u.Password), id); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO months(account_id,month) VALUES(?,?)`, id, month); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO session_totals(account_id,generation,issued) VALUES(?,?,0)`, id, m.Generation); err != nil {
				return err
			}
			if err := audit(tx, now, "user.add", u.Name, fmt.Sprintf("quota_bytes=%d", u.Quota)); err != nil {
				return err
			}
		}
		return nil
	})
}

func queueUser(tx *sql.Tx, id, generation string, now time.Time) error {
	_, err := tx.Exec(`UPDATE sessions SET blocked=1,
 kick_state=CASE WHEN online=0 AND (seen_online=1 OR (has_counter=0 AND created<?)) THEN 'offline' ELSE 'pending' END,
 attempts=0,last_attempt=0 WHERE account_id=? AND generation=? AND blocked=0`, now.Add(-2*time.Minute).UnixNano(), id, generation)
	if err != nil {
		return err
	}
	// Aging is not a new ban: never replenish attempts or erase a failure. An
	// issued marker may still be waiting in the core and must not be duplicated.
	_, err = tx.Exec(`UPDATE sessions SET kick_state='offline' WHERE account_id=? AND generation=? AND blocked=1 AND online=0 AND seen_online=0 AND has_counter=0 AND created<? AND kick_state='pending' AND attempts<3`, id, generation, now.Add(-2*time.Minute).UnixNano())
	return err
}

func (s *Store) Authenticate(password, generation string, now time.Time) (string, error) {
	if len(password) < 24 || len(password) > 256 {
		return "", ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var sid string
	err := s.commitLocked(func(tx *sql.Tx, m Meta) error {
		if s.pullUnresolved || !s.deltaBaselinedLocked(m.Generation) || m.Review || m.Hold || m.Generation != generation || generation == "" || m.LastSample == 0 || now.UnixNano() < m.LastSample || now.UnixNano()-m.LastSample > 30*int64(time.Second) {
			return ErrDenied
		}
		var id string
		var quota int64
		var disabled, revoked bool
		if err := tx.QueryRow(`SELECT id,quota,disabled,revoked FROM accounts WHERE digest=?`, s.digest(password)).Scan(&id, &quota, &disabled, &revoked); err == sql.ErrNoRows {
			return ErrDenied
		} else if err != nil {
			return err
		}
		if disabled || revoked {
			return ErrDenied
		}
		month, _ := billingTime(now)
		if _, err := tx.Exec(`INSERT OR IGNORE INTO months(account_id,month) VALUES(?,?)`, id, month); err != nil {
			return err
		}
		var used int64
		if err := tx.QueryRow(`SELECT tx+rx+adjustment FROM months WHERE account_id=? AND month=?`, id, month).Scan(&used); err != nil {
			return err
		}
		if used >= quota {
			return ErrDenied
		}
		var per, total, minute int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM sessions WHERE account_id=? AND generation=? AND (online=1 OR (seen_online=0 AND created>?))`, id, generation, now.Add(-2*time.Minute).UnixNano()).Scan(&per); err != nil {
			return err
		}
		if err := tx.QueryRow(`SELECT COUNT(*) FROM sessions WHERE generation=? AND (online=1 OR (seen_online=0 AND created>?))`, generation, now.Add(-2*time.Minute).UnixNano()).Scan(&total); err != nil {
			return err
		}
		if err := tx.QueryRow(`SELECT COUNT(*) FROM sessions WHERE account_id=? AND created>?`, id, now.Add(-time.Minute).UnixNano()).Scan(&minute); err != nil {
			return err
		}
		if per >= s.cfg.MaxSessionsPerUser || total >= s.cfg.MaxSessionsTotal || minute >= 8 {
			return ErrDenied
		}
		var err error
		sid, err = randomToken(24)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO sessions(id,account_id,generation,created) VALUES(?,?,?,?)`, sid, id, generation, now.UnixNano())
		if err != nil {
			return err
		}
		result, err := tx.Exec(`UPDATE session_totals SET issued=issued+1 WHERE account_id=? AND generation=? AND issued<?`, id, generation, int64(math.MaxInt64))
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil || n != 1 {
			return errors.New("session issuance counter missing or exhausted; auth transaction rolled back")
		}
		return nil
	}, false)
	if err == nil {
		s.trackOffline(sid, true, now)
	}
	return sid, err
}

func (s *Store) ChangeUser(op, name, newName string, quota int64, now time.Time) error {
	if err := requireName(name); err != nil {
		return err
	}
	if op == "rename" {
		if err := requireName(newName); err != nil {
			return err
		}
	}
	return s.mutate(func(tx *sql.Tx, m Meta) error {
		var id string
		var revoked bool
		if err := tx.QueryRow(`SELECT id,revoked FROM accounts WHERE name=?`, name).Scan(&id, &revoked); err != nil {
			return errors.New("account not found")
		}
		if revoked && op != "revoke" {
			return errors.New("revoked account cannot be enabled, renamed, exported or reused")
		}
		switch op {
		case "disable", "revoke":
			if _, err := tx.Exec(`UPDATE accounts SET disabled=1,revoked=CASE WHEN ?='revoke' THEN 1 ELSE revoked END WHERE id=?`, op, id); err != nil {
				return err
			}
			if err := queueUser(tx, id, m.Generation, now); err != nil {
				return err
			}
		case "enable":
			month, _ := billingTime(now)
			var used, limit int64
			if err := tx.QueryRow(`SELECT a.quota,COALESCE(m.tx+m.rx+m.adjustment,0) FROM accounts a LEFT JOIN months m ON m.account_id=a.id AND m.month=? WHERE a.id=?`, month, id).Scan(&limit, &used); err != nil {
				return err
			}
			if !eFresh(m, now) || m.Hold || used >= limit {
				return errors.New("cannot enable through accounting/global hold or exhausted quota")
			}
			if _, err := tx.Exec(`UPDATE accounts SET disabled=0 WHERE id=?`, id); err != nil {
				return err
			}
		case "quota":
			if quota <= 0 || quota > s.cfg.QuotaPoolBytes {
				return errors.New("quota outside explicit pool")
			}
			month, _ := billingTime(now)
			allocated, err := allocatedPool(tx, month, id)
			if err != nil {
				return err
			}
			var used int64
			if err := tx.QueryRow(`SELECT COALESCE((SELECT tx+rx+adjustment FROM months WHERE account_id=? AND month=?),0)`, id, month).Scan(&used); err != nil {
				return err
			}
			reserved := quota
			if used > reserved {
				reserved = used
			}
			if allocated > s.cfg.QuotaPoolBytes-reserved {
				return errors.New("quota pool exceeded (already consumed usage stays reserved)")
			}
			if _, err := tx.Exec(`UPDATE accounts SET quota=? WHERE id=?`, quota, id); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE months SET exhausted=(tx+rx+adjustment>=?) WHERE account_id=? AND month=?`, quota, id, month); err != nil {
				return err
			}
			if used >= quota {
				if err := queueUser(tx, id, m.Generation, now); err != nil {
					return err
				}
			}
		case "rename":
			var count int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM names WHERE account_id=?`, id).Scan(&count); err != nil {
				return err
			}
			if count >= 16 {
				return errors.New("account rename history capacity reached")
			}
			if _, err := tx.Exec(`INSERT INTO names(name,account_id) VALUES(?,?)`, newName, id); err != nil {
				return errors.New("new name already used (including historical names)")
			}
			if _, err := tx.Exec(`UPDATE accounts SET name=? WHERE id=?`, newName, id); err != nil {
				return err
			}
		case "rotate":
			var count int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM credentials WHERE account_id=?`, id).Scan(&count); err != nil {
				return err
			}
			if count >= 64 {
				return errors.New("credential rotation history capacity reached")
			}
			p, err := randomToken(32)
			if err != nil {
				return err
			}
			enc, err := s.encrypt(id, p)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO credentials(digest,account_id) VALUES(?,?)`, s.digest(p), id); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE accounts SET digest=?,credential=? WHERE id=?`, s.digest(p), enc, id); err != nil {
				return err
			}
			if err := queueUser(tx, id, m.Generation, now); err != nil {
				return err
			}
		default:
			return errors.New("unknown user operation")
		}
		return audit(tx, now, "user."+op, name, fmt.Sprintf("quota_bytes=%d new_name=%s", quota, newName))
	})
}

func (s *Store) Export(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fatal != nil {
		return "", s.fatal
	}
	var id string
	var encrypted []byte
	var revoked bool
	if err := s.db.QueryRow(`SELECT id,credential,revoked FROM accounts WHERE name=?`, name).Scan(&id, &encrypted, &revoked); err != nil {
		return "", errors.New("account not found")
	}
	if revoked {
		return "", errors.New("revoked account cannot be exported")
	}
	return s.decrypt(id, encrypted)
}

// Repair never runs implicitly. HY2 must be stopped, all accounts must have an
// explicit conservative charge (zero requires acknowledgement), and the root
// CLI supplies an explanation. Manual disable/revoke and prior usage survive.
func (s *Store) Arm(charges map[string]int64, reason string, accept bool, now time.Time) error {
	if !accept || len(reason) < 8 || len(reason) > 256 {
		return errors.New("explicit gap acknowledgement and 8..256-byte reason required")
	}
	// Validate and persist the held baseline and all charges before repairing the
	// witness. Malformed recovery input must not clear a restore failure.
	return s.commit(func(tx *sql.Tx, m Meta) error {
		rows, err := tx.Query(`SELECT id,name FROM accounts`)
		if err != nil {
			return err
		}
		type pair struct{ id, name string }
		var users []pair
		for rows.Next() {
			var u pair
			if err := rows.Scan(&u.id, &u.name); err != nil {
				rows.Close()
				return err
			}
			users = append(users, u)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(charges) != len(users) {
			return errors.New("charge file must explicitly cover every account including disabled/revoked accounts")
		}
		month, _ := billingTime(now)
		for _, u := range users {
			n, ok := charges[u.name]
			if !ok || n < 0 || n > 180*GiB {
				return errors.New("invalid/missing conservative charge")
			}
			var current int64
			if err := tx.QueryRow(`SELECT COALESCE((SELECT tx+rx+adjustment FROM months WHERE account_id=? AND month=?),0)`, u.id, month).Scan(&current); err != nil {
				return err
			}
			if current > math.MaxInt64-n {
				return errors.New("conservative charge would overflow existing usage")
			}
			if _, err := tx.Exec(`INSERT INTO months(account_id,month,adjustment) VALUES(?,?,?) ON CONFLICT(account_id,month) DO UPDATE SET adjustment=adjustment+excluded.adjustment`, u.id, month, n); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`INSERT INTO gaps(at,generation,reason) VALUES(?,?,?)`, now.Unix(), m.Generation, "explicit verification: "+reason); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE meta SET review=0,reason='',hold=1,arm_until=?,last_sample=0 WHERE singleton=1`, now.Add(45*time.Second).Unix()); err != nil {
			return err
		}
		return audit(tx, now, "system.arm", "", reason)
	}, true)
}

func (s *Store) BeginMaintenance(now time.Time) error {
	return s.mutate(func(tx *sql.Tx, m Meta) error {
		if m.Review {
			return ErrReview
		}
		_, err := tx.Exec(`UPDATE meta SET hold=1,arm_until=0 WHERE singleton=1`)
		return err
	})
}

func (s *Store) Activate(now time.Time) error {
	return s.mutate(func(tx *sql.Tx, m Meta) error {
		if s.pullUnresolved || !s.deltaBaselinedLocked(m.Generation) || !eFresh(m, now) || m.Generation == "" || m.ArmUntil != 0 {
			return errors.New("fresh verified baseline required")
		}
		if _, err := tx.Exec(`UPDATE meta SET hold=0,arm_until=0 WHERE singleton=1`); err != nil {
			return err
		}
		return audit(tx, now, "system.activate", "", "")
	})
}
func (s *Store) Hold(reason string, now time.Time) error {
	return s.mutate(func(tx *sql.Tx, m Meta) error {
		if _, err := tx.Exec(`UPDATE meta SET hold=1,review=1,reason=?,arm_until=0 WHERE singleton=1`, reason); err != nil {
			return err
		}
		return audit(tx, now, "system.prepare-stop", "", reason)
	})
}

func (s *Store) Backup(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fatal != nil {
		return s.fatal
	}
	if err := os.MkdirAll(s.cfg.BackupDir, 0700); err != nil {
		return err
	}
	path := filepath.Join(s.cfg.BackupDir, now.UTC().Format("20060102-150405")+".sqlite")
	if _, err := os.Stat(path); err == nil {
		return errors.New("backup already exists")
	}
	if _, err := s.db.Exec(`VACUUM INTO ?`, path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0600); err != nil {
		return err
	}
	entries, err := filepath.Glob(filepath.Join(s.cfg.BackupDir, "*.sqlite"))
	if err != nil {
		return err
	}
	// Glob is sorted; keep at most seven snapshots. Never back up the witness.
	for len(entries) > 7 {
		if err := os.Remove(entries[0]); err != nil {
			return err
		}
		entries = entries[1:]
	}
	return nil
}
