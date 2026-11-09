//go:build !windows

package audit

import (
	"errors"
	"os"
	"syscall"
)

// lockAuditPath takes a non-blocking exclusive lock on a sidecar file. Rotation
// renames the data file, so a lock on the data inode would not stay exclusive.
func lockAuditPath(path string) (*os.File, error) {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, ErrStorageUnavailable
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrStorageInUse
		}
		return nil, ErrStorageUnavailable
	}
	return f, nil
}

func unlockAuditPath(f *os.File) {
	if f == nil {
		return
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}
