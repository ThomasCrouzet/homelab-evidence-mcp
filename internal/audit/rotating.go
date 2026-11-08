package audit

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// ErrStorageUnavailable reports a recoverable audit storage failure.
// The error has no path or record content. A later Write retries the open.
var ErrStorageUnavailable = errors.New("audit storage unavailable")

// ErrStorageInUse reports a second process that owns the same audit path.
var ErrStorageInUse = errors.New("audit file is in use by another process")

// RotatingFile sets a maximum size and does numbered rotation.
// Callers must not pass secrets to it.
type RotatingFile struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	maxFiles int
	size     int64
	f        *os.File
	lock     *os.File
	closed   bool
}

// OpenRotating opens the path for appends and rotation. It makes the path if necessary.
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
	lock, err := lockAuditPath(path)
	if err != nil {
		return nil, err
	}
	f, err := openRegularAuditFile(path, true)
	if err != nil {
		unlockAuditPath(lock)
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		unlockAuditPath(lock)
		return nil, err
	}
	size, err := repairTornTail(f, st.Size())
	if err != nil {
		_ = f.Close()
		unlockAuditPath(lock)
		return nil, ErrStorageUnavailable
	}
	return &RotatingFile{path: path, maxBytes: maxBytes, maxFiles: maxFiles, size: size, f: f, lock: lock}, nil
}

// Write is the io.Writer implementation.
func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	if int64(len(p)) > r.maxBytes {
		return 0, fmt.Errorf("audit record exceeds max_bytes")
	}
	// A failed rotation closes the file. Reopen before the next complete record.
	if r.f == nil {
		if err := r.reopenLocked(); err != nil {
			return 0, err
		}
	}
	if r.size+int64(len(p)) > r.maxBytes {
		if err := r.rotateLocked(); err != nil {
			return 0, err
		}
	}
	start := r.size
	n, err := r.f.Write(p)
	if n == len(p) && err == nil {
		r.size += int64(n)
		return n, nil
	}
	// Roll back a partial append so the next JSONL record cannot join a torn
	// record. If rollback fails, close the file; the next open repairs the tail.
	if rollbackErr := r.f.Truncate(start); rollbackErr != nil {
		_ = r.f.Close()
		r.f = nil
		r.size = 0
	} else {
		r.size = start
	}
	if err == nil {
		err = io.ErrShortWrite
	}
	return n, err
}

// Close closes the output file. Later writes cannot reopen it.
func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	var err error
	if r.f != nil {
		err = r.f.Close()
		r.f = nil
	}
	unlockAuditPath(r.lock)
	r.lock = nil
	return err
}

func (r *RotatingFile) rotateLocked() error {
	if r.f != nil {
		if err := r.f.Close(); err != nil {
			r.f = nil
			return ErrStorageUnavailable
		}
		r.f = nil
	}
	// The rotation moves older files before it makes the primary file again.
	for i := r.maxFiles - 1; i >= 1; i-- {
		from := r.path
		if i > 1 {
			from = fmt.Sprintf("%s.%d", r.path, i-1)
		}
		to := fmt.Sprintf("%s.%d", r.path, i)
		if err := os.Remove(to); err != nil && !os.IsNotExist(err) {
			return ErrStorageUnavailable
		}
		if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
			return ErrStorageUnavailable
		}
	}
	f, err := openRegularAuditFile(r.path, false)
	if err != nil {
		return ErrStorageUnavailable
	}
	r.f = f
	r.size = 0
	return nil
}

// reopenLocked opens the primary file for append after a storage failure.
func (r *RotatingFile) reopenLocked() error {
	f, err := openRegularAuditFile(r.path, true)
	if err != nil {
		return ErrStorageUnavailable
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return ErrStorageUnavailable
	}
	size, err := repairTornTail(f, st.Size())
	if err != nil {
		_ = f.Close()
		return ErrStorageUnavailable
	}
	r.f = f
	r.size = size
	return nil
}

// repairTornTail truncates an incomplete final JSONL record. It keeps every
// complete prior record.
func repairTornTail(f *os.File, size int64) (int64, error) {
	if size == 0 {
		return 0, nil
	}
	last := make([]byte, 1)
	if _, err := f.ReadAt(last, size-1); err != nil {
		return size, err
	}
	if last[0] == '\n' {
		return size, nil
	}
	const chunk = 4096
	position := size
	for position > 0 {
		start := position - chunk
		if start < 0 {
			start = 0
		}
		buf := make([]byte, position-start)
		if _, err := f.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
			return size, err
		}
		if index := bytes.LastIndexByte(buf, '\n'); index >= 0 {
			cut := start + int64(index) + 1
			if err := f.Truncate(cut); err != nil {
				return size, err
			}
			return cut, nil
		}
		position = start
	}
	if err := f.Truncate(0); err != nil {
		return size, err
	}
	return 0, nil
}

func openRegularAuditFile(path string, appendMode bool) (*os.File, error) {
	flags := os.O_CREATE | os.O_RDWR
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
		return closeOnError(fmt.Errorf("audit file changed during the open operation"))
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

// MultiWriter copies each write to every writer. A failure in one destination
// does not stop the others. It reports an error only when every destination
// fails, because one successful destination retained the event.
func MultiWriter(writers ...io.Writer) io.Writer {
	return &fanoutWriter{writers: writers}
}

type fanoutWriter struct {
	writers []io.Writer
}

func (f *fanoutWriter) Write(p []byte) (int, error) {
	if len(f.writers) == 0 {
		return 0, io.ErrClosedPipe
	}
	maxWritten := 0
	var firstErr error
	successes := 0
	for _, writer := range f.writers {
		written, err := writer.Write(p)
		if written > maxWritten {
			maxWritten = written
		}
		if err == nil && written == len(p) {
			successes++
		} else if firstErr == nil {
			if err != nil {
				firstErr = err
			} else {
				firstErr = io.ErrShortWrite
			}
		}
	}
	if successes > 0 {
		return maxWritten, nil
	}
	return maxWritten, firstErr
}
