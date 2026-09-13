package store

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// AcquireDaemonLock takes a non-blocking exclusive flock on LockPath and
// holds it on an open file handle for the process lifetime. It returns
// ErrDaemonRunning if another process already holds it.
//
// This is deliberately not a PID file: the kernel releases an flock
// automatically when the holding process dies for any reason, including
// SIGKILL, so a crashed daemon can never leave a stale lock that blocks a
// legitimate restart. A PID file has no such guarantee and would risk a
// second daemon starting up and operating on another (dead) daemon's live
// runs while believing it owns the store.
//
// The returned release function drops the lock and closes the handle. It
// deliberately does not remove the lock file: doing so would race a second
// process that has just opened (but not yet flocked) the same path, letting
// it flock a now-unlinked inode while a third process creates and locks a
// fresh file at the same name — two "holders" of what looks like one lock.
func (s *Store) AcquireDaemonLock() (release func() error, err error) {
	path := s.LockPath()

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("store: open lock file %s: %w", path, err)
	}

	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrDaemonRunning
		}
		return nil, fmt.Errorf("store: flock %s: %w", path, err)
	}

	released := false
	release = func() error {
		if released {
			return nil
		}
		released = true
		if err := unix.Flock(int(f.Fd()), unix.LOCK_UN); err != nil {
			_ = f.Close()
			return fmt.Errorf("store: unlock %s: %w", path, err)
		}
		return f.Close()
	}

	return release, nil
}
