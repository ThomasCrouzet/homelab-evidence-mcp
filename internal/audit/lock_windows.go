//go:build windows

package audit

import "os"

// Windows does not take the sidecar lock here. The documentation states that
// shared audit ownership is supported on Unix only.
func lockAuditPath(string) (*os.File, error) { return nil, nil }

func unlockAuditPath(*os.File) {}
