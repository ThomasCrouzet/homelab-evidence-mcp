package audit

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// RotatingFile bounds size and performs numbered rotation.
// Callers must never pass secrets to it.
type RotatingFile struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	maxFiles int
	size     int64
	f        *os.File
}

// OpenRotating opens or creates path for append with rotation.
func OpenRotating(path string, maxBytes int64, maxFiles int) (*RotatingFile, error) {
	if path == "" {
		return nil, fmt.Errorf("audit file path required")
	}
	if maxBytes <= 0 {
		maxBytes = 10 << 20
	}
	if maxFiles <= 0 {
		maxFiles = 3
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil && filepath.Dir(path) != "." {
		return nil, err
	}
	f, err := openRegularAuditFile(path, true)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &RotatingFile{path: path, maxBytes: maxBytes, maxFiles: maxFiles, size: st.Size(), f: f}, nil
}

// Write implements io.Writer.
func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return 0, io.ErrClosedPipe
	}
	if r.size+int64(len(p)) > r.maxBytes {
		if err := r.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// Close closes the underlying file.
func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

func (r *RotatingFile) rotateLocked() error {
	if r.f != nil {
		err := r.f.Close()
		r.f = nil
		if err != nil {
			return err
		}
	}
	// Shift older files before recreating the main file.
	for i := r.maxFiles - 1; i >= 1; i-- {
		from := r.path
		if i > 1 {
			from = fmt.Sprintf("%s.%d", r.path, i-1)
		}
		to := fmt.Sprintf("%s.%d", r.path, i)
		if err := os.Remove(to); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	f, err := openRegularAuditFile(r.path, false)
	if err != nil {
		return err
	}
	r.f = f
	r.size = 0
	return nil
}

func openRegularAuditFile(path string, appendMode bool) (*os.File, error) {
	flags := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flags |= os.O_APPEND
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, err
	}
	closeOnError := func(err error) (*os.File, error) {
		_ = f.Close()
		return nil, err
	}

	pathInfo, err := os.Lstat(path)
	if err != nil {
		return closeOnError(err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 {
		return closeOnError(fmt.Errorf("audit file must not be a symlink"))
	}
	if !pathInfo.Mode().IsRegular() {
		return closeOnError(fmt.Errorf("audit path is not a regular file"))
	}
	fileInfo, err := f.Stat()
	if err != nil {
		return closeOnError(err)
	}
	if !os.SameFile(pathInfo, fileInfo) {
		return closeOnError(fmt.Errorf("audit file changed while opening"))
	}
	if err := f.Chmod(0o600); err != nil {
		return closeOnError(err)
	}
	if !appendMode {
		if err := f.Truncate(0); err != nil {
			return closeOnError(err)
		}
	}
	return f, nil
}

// MultiWriter duplicates writes to all outputs.
func MultiWriter(writers ...io.Writer) io.Writer {
	return io.MultiWriter(writers...)
}
