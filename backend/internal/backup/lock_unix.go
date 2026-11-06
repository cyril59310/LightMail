//go:build unix

package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// LockDataDir takes an exclusive advisory lock on the data directory so
// that maintenance commands (restore, rotate-key) cannot run while the
// server is running, and two servers cannot share a data directory.
func LockDataDir(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrPermission) {
		// Typical with a bind-mounted host directory: unlike a named volume,
		// it keeps its host owner, and the container runs unprivileged.
		return nil, fmt.Errorf("%w: the data directory %s must be writable by uid %d, the user LightMail runs as (for a host directory: sudo chown -R %d:%d <directory>)",
			err, dir, os.Getuid(), os.Getuid(), os.Getgid())
	}
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("the data directory is in use (is LightMail running? stop it first)")
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
