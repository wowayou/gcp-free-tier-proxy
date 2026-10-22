package account

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Counter struct {
	Tx int64 `json:"tx"`
	Rx int64 `json:"rx"`
}

func (n *Counter) UnmarshalJSON(data []byte) error {
	var raw struct {
		Tx *int64 `json:"tx"`
		Rx *int64 `json:"rx"`
	}
	if err := DecodeJSON(strings.NewReader(string(data)), &raw, MaxBody); err != nil {
		return err
	}
	if raw.Tx == nil || raw.Rx == nil || *raw.Tx < 0 || *raw.Rx < 0 {
		return errors.New("both nonnegative cumulative tx/rx counters required")
	}
	n.Tx, n.Rx = *raw.Tx, *raw.Rx
	return nil
}

type Snapshot struct {
	Traffic map[string]Counter
	Online  map[string]int
}

type CoreAPI interface {
	Read(context.Context) (Snapshot, error)
	Kick(context.Context, []string) error
}

type HTTPAPI struct {
	Origin, Secret string
	Client         *http.Client
}

func NewHTTPAPI(origin, secret string) *HTTPAPI {
	return &HTTPAPI{origin, secret, &http.Client{
		Timeout:       2 * time.Second,
		Transport:     &http.Transport{Proxy: nil, MaxConnsPerHost: 2, MaxIdleConnsPerHost: 2, ResponseHeaderTimeout: 2 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("local API redirect rejected") },
	}}
}
func (a *HTTPAPI) request(ctx context.Context, method, path string, data []byte, target any) error {
	r, err := http.NewRequestWithContext(ctx, method, a.Origin+path, strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", a.Secret)
	r.Header.Set("Content-Type", "application/json")
	resp, err := a.Client.Do(r)
	if err != nil {
		return errors.New("local core API unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("local core API returned HTTP %d", resp.StatusCode)
	}
	if target != nil {
		return DecodeJSON(resp.Body, target, MaxStatsBody)
	}
	return nil
}
func (a *HTTPAPI) Read(ctx context.Context) (Snapshot, error) {
	var s Snapshot
	// Never use clear=1: cursors and usage commit together, counters stay in core.
	if err := a.request(ctx, "GET", "/traffic", nil, &s.Traffic); err != nil {
		return s, err
	}
	if err := a.request(ctx, "GET", "/online", nil, &s.Online); err != nil {
		return s, err
	}
	if s.Traffic == nil || s.Online == nil {
		return s, errors.New("null statistics rejected")
	}
	return s, nil
}
func (a *HTTPAPI) Kick(ctx context.Context, ids []string) error {
	b, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	return a.request(ctx, "POST", "/kick", b, nil)
}

// /proc identity (boot UUID + PID + kernel start ticks) comes from the core
// launcher. A UUID file alone is insufficient: stale files are not trusted.
type ProcessGeneration struct {
	ID         string `json:"id"`
	BootID     string `json:"boot_id"`
	PID        int    `json:"pid"`
	StartTicks string `json:"start_ticks"`
}

func ProcessTicks(pid int) (string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	end := strings.LastIndex(string(b), ")")
	if end < 0 {
		return "", errors.New("invalid process identity")
	}
	fields := strings.Fields(string(b)[end+1:])
	if len(fields) < 20 {
		return "", errors.New("invalid process identity")
	}
	if fields[0] == "Z" {
		return "", errors.New("core process exited")
	}
	return fields[19], nil
}
func ReadGeneration(path string) (string, error) {
	var g ProcessGeneration
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := DecodeJSON(f, &g, 2048); err != nil {
		return "", err
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	ticks, err := ProcessTicks(g.PID)
	if err != nil || strings.TrimSpace(string(boot)) != g.BootID || ticks != g.StartTicks || len(g.ID) < 24 {
		return "", errors.New("core process generation is not live")
	}
	return g.BootID + ":" + strconv.Itoa(g.PID) + ":" + g.StartTicks + ":" + g.ID, nil
}

type GuardStatus struct {
	Month    string   `json:"month"`
	Used     int64    `json:"used_bytes"`
	Limit    int64    `json:"limit_bytes"`
	Warn     int64    `json:"warn_bytes"`
	Blocked  bool     `json:"blocked"`
	Healthy  bool     `json:"healthy"`
	Reason   string   `json:"reason"`
	Updated  int64    `json:"updated"`
	Services []string `json:"services"`
}

func ReadGuard(path string, now time.Time) (GuardStatus, error) {
	var g GuardStatus
	f, err := os.Open(path)
	if err != nil {
		return g, errors.New("VM guard status missing")
	}
	defer f.Close()
	if err := DecodeJSON(f, &g, 4096); err != nil {
		return g, err
	}
	month, _ := billingTime(now)
	if g.Month != month || g.Used < 0 || g.Limit <= 0 || g.Limit > 180*GiB || g.Warn < 0 || g.Warn >= g.Limit || now.Unix()-g.Updated > 180 || g.Updated > now.Unix()+5 {
		return g, errors.New("VM guard status is stale/invalid")
	}
	covered := map[string]bool{}
	for _, u := range g.Services {
		covered[strings.TrimSuffix(u, ".service")] = true
	}
	if !covered["sing-box"] || !covered["hysteria-server"] {
		return g, errors.New("VM guard does not cover both proxies")
	}
	if !g.Healthy || g.Blocked || g.Used >= g.Limit {
		return g, errors.New("VM guard is blocked or unhealthy")
	}
	return g, nil
}

type Collector struct {
	mu         sync.Mutex
	Store      *Store
	API        CoreAPI
	Generation func() (string, error)
	// Runtime samples use the end time, including time spent waiting on the
	// collector lock/API. Nil retains explicit timestamps in deterministic tests.
	Clock       func() time.Time
	lastFailure string
}

func (c *Collector) Failure() string { c.mu.Lock(); defer c.mu.Unlock(); return c.lastFailure }
func (c *Collector) sampleLocked(ctx context.Context, now time.Time) error {
	gen, err := c.Generation()
	if err != nil {
		return errors.New("core identity unavailable")
	}
	snapshot, err := c.API.Read(ctx)
	if err != nil {
		return err
	}
	after, err := c.Generation()
	if err != nil || after != gen {
		return errors.New("core restarted during collection")
	}
	if c.Clock != nil {
		now = c.Clock()
	}
	if err := c.Store.Apply(gen, snapshot, now); err != nil {
		return err
	}
	c.lastFailure = ""
	return nil
}
func (c *Collector) Collect(ctx context.Context, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.sampleLocked(ctx, now); err != nil {
		c.lastFailure = err.Error()
		return err
	}
	return nil
}
func (c *Collector) Tick(ctx context.Context, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.sampleLocked(ctx, now)
	if err != nil {
		c.lastFailure = err.Error()
		return err
	}
	// One batch per tick, at most 128 sessions. Failures get at most two retries
	// at later ticks, not an unbounded loop or a global restart fallback.
	if err := c.kickLocked(ctx, now, ""); err != nil {
		return err
	}
	return nil
}
func (c *Collector) Enforce(ctx context.Context, now time.Time) error {
	return c.EnforceUser(ctx, now, "")
}
func (c *Collector) EnforceUser(ctx context.Context, now time.Time, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.kickLocked(ctx, now, name)
}
func (c *Collector) kickLocked(ctx context.Context, now time.Time, name string) error {
	gen, err := c.Generation()
	if err != nil {
		return errors.New("ban incomplete: core identity unavailable")
	}
	ids, err := c.Store.pendingFor(gen, name, now, 128)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	// Persist an attempt *before* sending. A crash cannot reset the retry limit.
	if err := c.Store.MarkAttempt(ids, now); err != nil {
		return err
	}
	err = c.API.Kick(ctx, ids)
	if after, e := c.Generation(); e != nil || after != gen {
		err = errors.New("core changed during ban delivery")
	}
	state := "issued"
	if err != nil {
		state = "pending"
	}
	if e := c.Store.KickResult(ids, state); e != nil {
		return e
	}
	if err != nil {
		return errors.New("ban incomplete: /kick failed; persistent task retained, no service restart")
	}
	return nil
}

func (s *Store) markReview(reason, gen string, now time.Time) error {
	return s.mutate(func(tx *sql.Tx, m Meta) error {
		if m.Review && m.Reason == reason {
			return nil
		}
		if _, err := tx.Exec(`UPDATE meta SET review=1,hold=1,reason=?,arm_until=0 WHERE singleton=1`, reason); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO gaps(at,generation,reason) VALUES(?,?,?)`, now.Unix(), gen, reason)
		return err
	})
}

func (s *Store) Apply(gen string, snapshot Snapshot, now time.Time) error {
	return s.apply(gen, snapshot, now, false)
}

func (s *Store) apply(gen string, snapshot Snapshot, now time.Time, maintenance bool) error {
	if gen == "" || snapshot.Traffic == nil || snapshot.Online == nil {
		if err := s.markReview("invalid/null statistics baseline", gen, now); err != nil {
			return err
		}
		return ErrReview
	}
	if len(snapshot.Traffic) > s.cfg.MaxSessionsTotal || len(snapshot.Online) > s.cfg.MaxSessionsTotal {
		if err := s.markReview("statistics capacity exceeded", gen, now); err != nil {
			return err
		}
		return ErrReview
	}
	var anomaly string
	err := s.mutate(func(tx *sql.Tx, m Meta) error {
		if gen != m.Generation {
			if m.ArmUntil < now.Unix() || m.Review || !m.Hold {
				anomaly = "unplanned core generation: final in-memory counters may be lost"
				return ErrReview
			}
			if len(snapshot.Traffic) != 0 || len(snapshot.Online) != 0 {
				anomaly = "new core baseline is not empty"
				return ErrReview
			}
			if _, err := tx.Exec(`UPDATE sessions SET online=0,kick_state=CASE WHEN blocked=1 THEN 'offline' ELSE kick_state END WHERE generation<>?`, gen); err != nil {
				return err
			}
			// Previous-generation cursors are no longer needed; usage/tombstones remain.
			if _, err := tx.Exec(`DELETE FROM sessions WHERE generation<>?`, gen); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE meta SET generation=?,last_sample=?,arm_until=0 WHERE singleton=1`, gen, now.UnixNano()); err != nil {
				return err
			}
			return nil
		}
		if m.Review {
			return ErrReview
		}
		if maintenance && !m.Hold {
			return errors.New("maintenance sample requires authentication hold")
		}
		if m.LastSample > now.UnixNano() {
			anomaly = "clock moved backwards"
			return ErrReview
		}
		type session struct {
			id, account        string
			tx, rx, created    int64
			has, seen, blocked bool
		}
		rows, err := tx.Query(`SELECT id,account_id,tx,rx,created,has_counter,seen_online,blocked FROM sessions WHERE generation=?`, gen)
		if err != nil {
			return err
		}
		known := map[string]session{}
		for rows.Next() {
			var v session
			if err := rows.Scan(&v.id, &v.account, &v.tx, &v.rx, &v.created, &v.has, &v.seen, &v.blocked); err != nil {
				rows.Close()
				return err
			}
			known[v.id] = v
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for id, n := range snapshot.Online {
			if _, ok := known[id]; !ok || n != 1 {
				anomaly = "unknown/reused online session ID"
				return ErrReview
			}
		}
		for id, n := range snapshot.Traffic {
			v, ok := known[id]
			if !ok {
				anomaly = "unknown session counter"
				return ErrReview
			}
			if n.Tx < 0 || n.Rx < 0 || n.Tx < v.tx || n.Rx < v.rx || n.Tx > math.MaxInt64-n.Rx {
				anomaly = "counter rollback/overflow"
				return ErrReview
			}
		}
		month, day := billingTime(now)
		if _, err := tx.Exec(`INSERT OR IGNORE INTO months(account_id,month) SELECT id,? FROM accounts`, month); err != nil {
			return err
		}
		var total int64
		if err := tx.QueryRow(`SELECT COALESCE(SUM(tx+rx+adjustment),0) FROM months WHERE month=?`, month).Scan(&total); err != nil {
			return err
		}
		for id, v := range known {
			n, has := snapshot.Traffic[id]
			if v.has && !has {
				anomaly = "cumulative counter disappeared (possible clear/restore)"
				return ErrReview
			}
			if has {
				dtx, drx := n.Tx-v.tx, n.Rx-v.rx
				var used int64
				if err := tx.QueryRow(`SELECT tx+rx+adjustment FROM months WHERE account_id=? AND month=?`, v.account, month).Scan(&used); err != nil {
					return err
				}
				if dtx > math.MaxInt64-used || drx > math.MaxInt64-used-dtx || dtx > math.MaxInt64-total || drx > math.MaxInt64-total-dtx {
					anomaly = "usage overflow"
					return ErrReview
				}
				total += dtx + drx
				if _, err := tx.Exec(`UPDATE months SET tx=tx+?,rx=rx+? WHERE account_id=? AND month=?`, dtx, drx, v.account, month); err != nil {
					return err
				}
				if dtx+drx > 0 {
					if _, err := tx.Exec(`INSERT INTO days(account_id,day,tx,rx) VALUES(?,?,?,?) ON CONFLICT(account_id,day) DO UPDATE SET tx=tx+excluded.tx,rx=rx+excluded.rx`, v.account, day, dtx, drx); err != nil {
						return err
					}
				}
				if _, err := tx.Exec(`UPDATE sessions SET tx=?,rx=?,has_counter=1 WHERE id=?`, n.Tx, n.Rx, id); err != nil {
					return err
				}
			}
			online := snapshot.Online[id]
			seen := v.seen || online == 1
			state := ""
			if v.blocked && online == 0 && seen {
				state = "offline"
			}
			// An unobserved auth response can arrive later: never call it offline
			// merely because one /online snapshot has no entry.
			if _, err := tx.Exec(`UPDATE sessions SET online=?,seen_online=?,kick_state=CASE WHEN ?<>'' THEN ? ELSE kick_state END WHERE id=?`, online, seen, state, state, id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`UPDATE months SET exhausted=(tx+rx+adjustment >= (SELECT quota FROM accounts WHERE id=account_id)) WHERE month=?`, month); err != nil {
			return err
		}
		rows, err = tx.Query(`SELECT a.id FROM accounts a JOIN months m ON m.account_id=a.id AND m.month=? WHERE a.disabled=1 OR a.revoked=1 OR m.exhausted=1`, month)
		if err != nil {
			return err
		}
		var blocked []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			blocked = append(blocked, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, id := range blocked {
			if err := queueUser(tx, id, gen); err != nil {
				return err
			}
		}
		cutMonth, _ := billingTime(now.AddDate(-2, 0, 0))
		_, cutDay := billingTime(now.AddDate(0, 0, -35))
		if _, err := tx.Exec(`UPDATE meta SET last_sample=? WHERE singleton=1;
UPDATE sessions SET kick_state='failed' WHERE blocked=1 AND kick_state='pending' AND attempts>=3;
DELETE FROM days WHERE day<?; DELETE FROM months WHERE month<?;`, now.UnixNano(), cutDay, cutMonth); err != nil {
			return err
		}
		return nil
	})
	if anomaly != "" {
		if e := s.markReview(anomaly, gen, now); e != nil {
			return e
		}
		return ErrReview
	}
	return err
}

func (s *Store) Pending(gen string, now time.Time, limit int) ([]string, error) {
	return s.pendingFor(gen, "", now, limit)
}
func (s *Store) pendingFor(gen, name string, now time.Time, limit int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT s.id FROM sessions s JOIN accounts a ON a.id=s.account_id
 WHERE s.generation=? AND s.blocked=1 AND s.kick_state='pending' AND s.attempts<3
 AND s.last_attempt<=? AND (?='' OR a.name=?) ORDER BY s.created LIMIT ?`, gen, now.Add(-5*time.Second).UnixNano(), name, name, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (s *Store) MarkAttempt(ids []string, now time.Time) error {
	return s.mutate(func(tx *sql.Tx, m Meta) error {
		for _, id := range ids {
			if _, err := tx.Exec(`UPDATE sessions SET attempts=attempts+1,last_attempt=? WHERE id=? AND kick_state='pending' AND attempts<3`, now.UnixNano(), id); err != nil {
				return err
			}
		}
		return nil
	})
}
func (s *Store) KickResult(ids []string, state string) error {
	return s.mutate(func(tx *sql.Tx, m Meta) error {
		for _, id := range ids {
			if _, err := tx.Exec(`UPDATE sessions SET kick_state=CASE WHEN ?='pending' AND attempts>=3 THEN 'failed' ELSE ? END WHERE id=? AND kick_state<>'offline'`, state, state, id); err != nil {
				return err
			}
		}
		return nil
	})
}
func (s *Store) RetryBan(name string, now time.Time) error {
	return s.mutate(func(tx *sql.Tx, m Meta) error {
		var id string
		if err := tx.QueryRow(`SELECT id FROM accounts WHERE name=?`, name).Scan(&id); err != nil {
			return errors.New("account not found")
		}
		if _, err := tx.Exec(`UPDATE sessions SET attempts=0,last_attempt=0,kick_state='pending' WHERE account_id=? AND generation=? AND blocked=1 AND kick_state='failed'`, id, m.Generation); err != nil {
			return err
		}
		return audit(tx, now, "user.retry-ban", name, "bounded new retry budget")
	})
}
