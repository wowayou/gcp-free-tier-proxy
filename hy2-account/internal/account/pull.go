package account

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const interruptedPull = "interrupted delta pull: cleared core counters may be uncommitted"

// Delta (clear=1) accounting is only valid for a core generation whose empty
// baseline was committed by this accounting mode. A binary swap against an
// already-running cumulative core, or a rollback, must never be read as deltas.
const deltaGateReason = "core generation not baselined for delta accounting: stop the core and recover into a new generation"

type deltaBaseline struct {
	Generation string `json:"generation"`
}

func deltaBaselinePath(c Config) string { return c.Database + ".delta" }

func readDeltaBaseline(c Config) string {
	f, err := os.Open(deltaBaselinePath(c))
	if err != nil {
		return ""
	}
	defer f.Close()
	var b deltaBaseline
	if DecodeJSON(f, &b, 1024) != nil {
		return "" // Unknown is never treated as baselined.
	}
	return b.Generation
}

func (s *Store) deltaBaselinedLocked(gen string) bool { return gen != "" && s.deltaGen == gen }

func syncPullDir(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func createPullMarker(path string) error {
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), path)
	err = f.Sync()
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return syncPullDir(path)
}

func removePullMarker(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncPullDir(path)
}

// interrupted records the loss before removing its crash evidence. If SQLite
// or its witness cannot commit the review, the marker must survive for restart.
func (c *Collector) interrupted(gen string, now time.Time) error {
	// Meta may contain an in-memory startup gate. It is not proof that the
	// evidence has been durably recorded; always commit before unlinking.
	if err := c.recordPullReview(gen, now); err != nil {
		return err
	}
	if err := c.clearPullMarker(); err != nil {
		return err
	}
	return ErrReview
}

func (c *Collector) recordPullReview(gen string, now time.Time) error {
	c.Store.mu.Lock()
	defer c.Store.mu.Unlock()
	c.Store.pullUnresolved = true
	// Failed review writes remain fail-closed but reviewable. Existing fatal
	// witness/storage errors still take precedence and are never cleared here.
	return c.Store.markReviewLocked(interruptedPull, gen, now)
}

func (c *Collector) clearPullMarker() error {
	if err := removePullMarker(c.Store.cfg.Database + ".pull"); err != nil {
		return err
	}
	c.Store.mu.Lock()
	c.Store.pullUnresolved = false
	c.Store.mu.Unlock()
	return nil
}

// pullLocked is shared by normal and final held samples. Collector.mu spans
// generation checks, the destructive read, the durable commit, and marker cleanup.
func (c *Collector) pullLocked(ctx context.Context, now time.Time, maintenance bool) error {
	path := c.Store.cfg.Database + ".pull"
	_, markerErr := os.Lstat(path)
	if markerErr != nil && !os.IsNotExist(markerErr) {
		return markerErr
	}
	gen, err := c.Generation()
	if markerErr == nil {
		m, metaErr := c.Store.Meta()
		if metaErr != nil {
			return metaErr
		}
		// Only an explicitly reviewed, still-armed NEW generation can discard
		// old-generation crash evidence. Apply still requires an empty baseline.
		if err == nil && gen != "" && gen != m.Generation && m.Hold && !m.Review && m.LastSample == 0 && m.ArmUntil >= now.Unix() {
			// Reuse the durable marker until the empty baseline is validated and
			// committed. A failed/nonempty baseline still needs durable review.
		} else {
			return c.interrupted(gen, now)
		}
	}
	if err != nil {
		return errors.New("core identity unavailable")
	}
	// Under review the sample cannot be committed. Never issue the destructive
	// clear then: core counters stay intact for the reviewed stop/recovery.
	m, err := c.Store.Meta()
	if err != nil {
		return err
	} else if m.Review && gen == m.Generation {
		return ErrReview
	}
	// Even an armed hold cannot make a same-generation cumulative core safe to
	// clear: record a durable review instead of overcharging old totals.
	c.Store.mu.Lock()
	baselined := c.Store.deltaBaselinedLocked(gen)
	c.Store.mu.Unlock()
	if gen == m.Generation && !baselined {
		if err := c.Store.markReview(deltaGateReason, gen, now); err != nil {
			return err
		}
		return ErrReview
	}
	online, err := c.API.ReadOnline(ctx)
	if err != nil {
		return err
	} // No destructive request has been made.
	if markerErr != nil {
		if err := createPullMarker(path); err != nil {
			return err
		}
	}
	traffic, err := c.API.ReadTrafficDelta(ctx)
	if errors.Is(err, ErrNotSent) {
		// A refused new request does not resolve an older orphan.
		if markerErr != nil {
			if e := c.clearPullMarker(); e != nil {
				return e
			}
		}
		return err
	}
	if c.Clock != nil {
		now = c.Clock()
	}
	if err != nil {
		return c.interrupted(gen, now)
	}
	after, err := c.Generation()
	if err != nil || after != gen {
		return c.interrupted(gen, now)
	}
	if err := c.Store.apply(gen, Snapshot{Traffic: traffic, Online: online}, now, maintenance); err != nil {
		return c.interrupted(gen, now)
	}
	if err := c.clearPullMarker(); err != nil {
		// Usage may already be committed; never retry this delta. Preserve a
		// review hold even if only directory fsync failed after unlink.
		if e := c.recordPullReview(gen, now); e != nil {
			return e
		}
		return ErrReview
	}
	c.lastFailure = ""
	return nil
}
