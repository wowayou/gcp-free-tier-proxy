package account

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
		return errors.New("both nonnegative delta tx/rx counters required")
	}
	n.Tx, n.Rx = *raw.Tx, *raw.Rx
	return nil
}

type Snapshot struct {
	// Traffic contains deltas since the previous atomic core clear, not totals.
	Traffic map[string]Counter
	Online  map[string]int
}

type CoreAPI interface {
	ReadOnline(context.Context) (map[string]int, error)
	ReadTrafficDelta(context.Context) (map[string]Counter, error)
	Kick(context.Context, []string) error
}

type HTTPAPI struct {
	Origin, Secret string
	Client         *http.Client
}

func NewHTTPAPI(origin, secret string) *HTTPAPI {
	return &HTTPAPI{origin, secret, &http.Client{
		// Per-request deadlines are set in request(); this is only the ceiling.
		Timeout: destructiveReadTimeout,
		// Fresh connections prevent net/http from transparently replaying the
		// destructive GET on a reused connection after an ambiguous read error.
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxConnsPerHost: 2, ResponseHeaderTimeout: destructiveReadTimeout},
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("local API redirect rejected") },
	}}
}

// Ordinary loopback calls stay short. The destructive clear read gets a longer
// budget: an ambiguous timeout forces manual review, so a brief e2-micro CPU
// stall must not become a maintenance event. It still stays well inside the
// 30 s authentication freshness window.
const (
	ordinaryRequestTimeout = 2 * time.Second
	destructiveReadTimeout = 10 * time.Second
)

func (a *HTTPAPI) request(ctx context.Context, method, path string, data []byte, target any) error {
	return a.requestWithin(ctx, ordinaryRequestTimeout, method, path, data, target)
}
func (a *HTTPAPI) requestWithin(ctx context.Context, limit time.Duration, method, path string, data []byte, target any) error {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	// A dial failure is only proof of no send if no connection was obtained,
	// including any transparent retry by net/http on a reused connection.
	var gotConn atomic.Bool
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { gotConn.Store(true) }})
	r, err := http.NewRequestWithContext(ctx, method, a.Origin+path, strings.NewReader(string(data)))
	if err != nil {
		return fmt.Errorf("%w: invalid local request", ErrNotSent)
	}
	r.Header.Set("Authorization", a.Secret)
	r.Header.Set("Content-Type", "application/json")
	resp, err := a.Client.Do(r)
	if err != nil {
		var op *net.OpError
		if !gotConn.Load() && errors.As(err, &op) && op.Op == "dial" {
			return fmt.Errorf("%w: local core API dial failed", ErrNotSent)
		}
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

// ErrNotSent proves that no request reached the core. All other delta read
// failures are ambiguous and must retain the durable pull marker until review.
var ErrNotSent = errors.New("local core request not sent")

func (a *HTTPAPI) ReadOnline(ctx context.Context) (map[string]int, error) {
	var online map[string]int
	err := a.request(ctx, "GET", "/online", nil, &online)
	if err == nil && online == nil {
		err = errors.New("null online statistics rejected")
	}
	return online, err
}
func (a *HTTPAPI) ReadTrafficDelta(ctx context.Context) (map[string]Counter, error) {
	var traffic map[string]Counter
	// The pinned core atomically marshals and clears its map. Deltas must be
	// committed once; the collector brackets this destructive read with a marker.
	err := a.requestWithin(ctx, destructiveReadTimeout, "GET", "/traffic?clear=1", nil, &traffic)
	if err == nil && traffic == nil {
		err = errors.New("null traffic statistics rejected")
	}
	return traffic, err
}
func (a *HTTPAPI) Read(ctx context.Context) (Snapshot, error) {
	var s Snapshot
	var err error
	s.Online, err = a.ReadOnline(ctx)
	if err == nil {
		s.Traffic, err = a.ReadTrafficDelta(ctx)
	}
	return s, err
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
	return c.pullLocked(ctx, now, false)
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
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.markReviewLocked(reason, gen, now)
}
func (s *Store) markReviewLocked(reason, gen string, now time.Time) error {
	return s.commitLocked(func(tx *sql.Tx, m Meta) error {
		if m.Review && m.Reason == reason {
			return nil
		}
		if _, err := tx.Exec(`UPDATE meta SET review=1,hold=1,reason=?,arm_until=0 WHERE singleton=1`, reason); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO gaps(at,generation,reason) VALUES(?,?,?)`, now.Unix(), gen, reason)
		return err
	}, false)
}

func (s *Store) Apply(gen string, snapshot Snapshot, now time.Time) error {
	return s.apply(gen, snapshot, now, false)
}

func (s *Store) apply(gen string, snapshot Snapshot, now time.Time, maintenance bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen == "" || snapshot.Traffic == nil || snapshot.Online == nil {
		if err := s.markReviewLocked("invalid/null statistics baseline", gen, now); err != nil {
			return err
		}
		return ErrReview
	}
	if len(snapshot.Traffic) > 2*s.cfg.MaxSessionsTotal || len(snapshot.Online) > s.cfg.MaxSessionsTotal {
		if err := s.markReviewLocked("statistics capacity exceeded", gen, now); err != nil {
			return err
		}
		return ErrReview
	}
	var anomaly string
	var resetRetention bool
	offline := map[string]bool{}
	var pruning prunePlan
	activity := map[string]bool{}
	err := s.commitLocked(func(tx *sql.Tx, m Meta) error {
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
			// Previous-generation sessions are no longer needed; usage/tombstones remain.
			resetRetention = true
			if _, err := tx.Exec(`DELETE FROM sessions WHERE generation<>?`, gen); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE session_totals SET generation=?,issued=0`, gen); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE meta SET generation=?,last_sample=?,arm_until=0 WHERE singleton=1`, gen, now.UnixNano()); err != nil {
				return err
			}
			// Persist before commit: a crash in between leaves a sidecar naming
			// a generation meta does not have yet, which stays gated (fail closed).
			return writeJSON(deltaBaselinePath(s.cfg), deltaBaseline{Generation: gen}, 0600)
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
			id, account   string
			tx, rx        int64
			seen, blocked bool
		}
		// Only load current online rows and IDs present in this delta/online
		// snapshot, never all retained rows of a long-running generation.
		ids := make(map[string]bool, len(snapshot.Traffic)+len(snapshot.Online))
		for id := range snapshot.Traffic {
			ids[id] = true
		}
		for id := range snapshot.Online {
			ids[id] = true
		}
		activity = ids
		rows, err := tx.Query(`SELECT id,account_id,tx,rx,seen_online,blocked FROM sessions WHERE generation=? AND online=1`, gen)
		if err != nil {
			return err
		}
		known := map[string]session{}
		for rows.Next() {
			var v session
			if err := rows.Scan(&v.id, &v.account, &v.tx, &v.rx, &v.seen, &v.blocked); err != nil {
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
		for id := range ids {
			if _, ok := known[id]; ok {
				continue
			}
			var v session
			err := tx.QueryRow(`SELECT id,account_id,tx,rx,seen_online,blocked FROM sessions WHERE id=? AND generation=?`, id, gen).Scan(&v.id, &v.account, &v.tx, &v.rx, &v.seen, &v.blocked)
			if err == sql.ErrNoRows {
				continue
			}
			if err != nil {
				return err
			}
			known[id] = v
		}
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
			if n.Tx < 0 || n.Rx < 0 || n.Tx > math.MaxInt64-n.Rx || n.Tx > math.MaxInt64-v.tx || n.Rx > math.MaxInt64-v.rx || v.tx+n.Tx > math.MaxInt64-v.rx-n.Rx {
				anomaly = "delta/session counter overflow"
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
			if has {
				dtx, drx := n.Tx, n.Rx
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
				if _, err := tx.Exec(`UPDATE sessions SET tx=tx+?,rx=rx+?,has_counter=1 WHERE id=?`, n.Tx, n.Rx, id); err != nil {
					return err
				}
			}
			online := snapshot.Online[id]
			seen := v.seen || online == 1
			offline[id] = online == 0
			// Traffic may follow an older /online snapshot. It is activity even
			// for a never-seen auth, and cannot confirm kick consumption/death.
			// As in the accepted r3 baseline, an observed close resolves any
			// pending/failed task; reappearance below restores the old budget.
			if _, err := tx.Exec(`UPDATE sessions SET online=?,seen_online=?,kick_state=CASE WHEN blocked=1 AND ? THEN 'offline' ELSE kick_state END WHERE id=?`, online, seen, !activity[id] && seen, id); err != nil {
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
			if err := queueUser(tx, id, gen, now); err != nil {
				return err
			}
		}
		// Reconcile durable blocked IDs, not just currently disabled accounts:
		// enable/rotation must never resurrect old IDs. Preserve issued kicks
		// (this snapshot may precede delivery) and the cumulative retry budget.
		for id := range activity {
			if _, err := tx.Exec(`UPDATE sessions SET kick_state=CASE WHEN attempts>=3 THEN 'failed' ELSE 'pending' END WHERE id=? AND blocked=1 AND kick_state='offline'`, id); err != nil {
				return err
			}
		}
		pruning, err = s.pruneSessions(tx, gen, now, activity)
		if err != nil {
			return err
		}
		cutMonth, _ := billingTime(now.AddDate(-2, 0, 0))
		_, cutDay := billingTime(now.AddDate(0, 0, -35))
		if _, err := tx.Exec(`UPDATE meta SET last_sample=? WHERE singleton=1;
UPDATE sessions SET kick_state='failed' WHERE blocked=1 AND kick_state='pending' AND attempts>=3;
DELETE FROM days WHERE day<?; DELETE FROM months WHERE month<?;`, now.UnixNano(), cutDay, cutMonth); err != nil {
			return err
		}
		return nil
	}, false)
	if err == nil {
		if resetRetention {
			s.offlineSince = make(map[string]*offlineSession)
			s.offlineQueue = nil
			s.pruneCursor = ""
			s.deltaGen = gen
		}
		s.finishPrune(pruning, now)
		for id, dead := range offline {
			if activity[id] {
				// Refresh the full inactivity window only after durable commit.
				s.trackOffline(id, false, now)
			}
			s.trackOffline(id, dead, now)
		}
	}
	if anomaly != "" {
		if e := s.markReviewLocked(anomaly, gen, now); e != nil {
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
