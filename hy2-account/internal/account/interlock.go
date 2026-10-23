package account

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// RecoveryProtocol identifies the status/CLI contract needed by the manual
// recovery helper. It is not a database schema version.
const RecoveryProtocol = 1

var ErrHealthControlBusy = errors.New("health/arm control lock busy")

// LockHealthControl serializes the privileged arm CLI (including latch clear
// and daemon-reload) with the complete independent health decision/shutdown.
// Take it BEFORE reading health, not just before writing the latch. Gate is
// read-only and must not take this lock: systemctl may invoke gate while a
// controlling process is waiting for a systemd job to finish.
//
// The lock stays root-only next to the root-owned latch. Never unlink it on
// release: waiters and new callers must keep locking the same inode.
func LockHealthControl(cfg Config, wait time.Duration) (*os.File, error) {
	f, err := os.OpenFile(cfg.HealthBlockedFile+".lock", os.O_CREATE|os.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		f.Close()
		return nil, errors.New("health control lock must be an owned private regular file")
	}
	deadline := time.Now().Add(wait)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			f.Close()
			return nil, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			f.Close()
			return nil, ErrHealthControlBusy
		}
		time.Sleep(min(25*time.Millisecond, remaining))
	}
}
