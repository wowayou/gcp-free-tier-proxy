package account

import (
	"container/heap"
	"database/sql"
	"time"
)

const sessionRetention = 15 * time.Minute

// One indexed entry per offline ID, including never-seen authentications.
// nextCheck is separate from last inactivity evidence: nonprunable heads must
// yield to other candidates without pretending to have observed new activity.
type offlineSession struct {
	id        string
	since     time.Time
	nextCheck time.Time
	index     int
}
type offlineHeap []*offlineSession

func (h offlineHeap) Len() int           { return len(h) }
func (h offlineHeap) Less(i, j int) bool { return h[i].nextCheck.Before(h[j].nextCheck) }
func (h offlineHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i]; h[i].index = i; h[j].index = j }
func (h *offlineHeap) Push(v any)        { e := v.(*offlineSession); e.index = len(*h); *h = append(*h, e) }
func (h *offlineHeap) Pop() any {
	old := *h
	e := old[len(old)-1]
	old[len(old)-1] = nil
	*h = old[:len(old)-1]
	e.index = -1
	return e
}

func (s *Store) initRetention(now time.Time) error {
	s.offlineSince = make(map[string]*offlineSession)
	m, err := readMeta(s.db)
	if err != nil {
		return err
	}
	// Neither a restart nor a backwards wall clock can shorten the grace.
	if m.LastSample > now.UnixNano() {
		now = time.Unix(0, m.LastSample)
	}
	rows, err := s.db.Query(`SELECT id FROM sessions WHERE generation=? AND online=0`, m.Generation)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		s.trackOffline(id, true, now)
	}
	return rows.Err()
}

func (s *Store) trackOffline(id string, offline bool, now time.Time) {
	e := s.offlineSince[id]
	if !offline {
		if e != nil {
			heap.Remove(&s.offlineQueue, e.index)
			delete(s.offlineSince, id)
		}
	} else if e == nil {
		e = &offlineSession{id: id, since: now, nextCheck: now.Add(sessionRetention)}
		s.offlineSince[id] = e
		heap.Push(&s.offlineQueue, e)
	}
}

type prunePlan struct {
	deleted []string
	scanned []string
	cursor  string
}

// All changes to the retention index/cursor are published only after commit.
// Temporarily popped heap entries are restored unchanged even on rollback.
func (s *Store) pruneSessions(tx *sql.Tx, gen string, now time.Time, activity map[string]bool) (prunePlan, error) {
	plan := prunePlan{cursor: s.pruneCursor}
	var candidates []*offlineSession
	defer func() {
		for _, e := range candidates {
			heap.Push(&s.offlineQueue, e)
		}
	}()
	deleteID := func(id string) error {
		if activity[id] {
			return nil
		}
		// Exhausted real tasks stay visibly failed until explicit retry-ban.
		result, err := tx.Exec(`DELETE FROM sessions WHERE id=? AND generation=? AND online=0 AND (blocked=0 OR kick_state='offline' OR (kick_state='failed' AND attempts<3))`, id, gen)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if n > 0 {
			plan.deleted = append(plan.deleted, id)
		}
		return err
	}
	for len(candidates) < 256 && len(s.offlineQueue) > 0 && !s.offlineQueue[0].nextCheck.After(now) {
		e := heap.Pop(&s.offlineQueue).(*offlineSession)
		candidates = append(candidates, e)
		plan.scanned = append(plan.scanned, e.id)
		if err := deleteID(e.id); err != nil {
			return plan, err
		}
	}
	// Bounded discovery for rows not yet indexed (e.g. imported old state).
	// Normal auth and reopen already index every offline row. A cursor prevents
	// retained/nonprunable candidates from monopolizing this discovery budget.
	rows, err := tx.Query(`SELECT id FROM sessions WHERE id>? AND generation=? AND online=0 AND seen_online=0 AND created<=? AND (blocked=0 OR kick_state='offline' OR (kick_state='failed' AND attempts<3)) ORDER BY id LIMIT ?`, s.pruneCursor, gen, now.Add(-sessionRetention).UnixNano(), 256-len(candidates))
	if err != nil {
		return plan, err
	}
	var discovered []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return plan, err
		}
		discovered = append(discovered, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return plan, err
	}
	if len(candidates) < 256 {
		plan.cursor = ""
	}
	for _, id := range discovered {
		plan.cursor = id
		if _, tracked := s.offlineSince[id]; tracked {
			continue // Its restart/activity grace is authoritative.
		}
		if err := deleteID(id); err != nil {
			return plan, err
		}
	}
	return plan, nil
}

func (s *Store) finishPrune(plan prunePlan, now time.Time) {
	s.pruneCursor = plan.cursor
	for _, id := range plan.scanned {
		if e := s.offlineSince[id]; e != nil {
			e.nextCheck = now.Add(sessionRetention)
			heap.Fix(&s.offlineQueue, e.index)
		}
	}
	for _, id := range plan.deleted {
		s.trackOffline(id, false, now)
	}
}
