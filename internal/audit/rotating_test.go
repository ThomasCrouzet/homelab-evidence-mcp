package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRotatingFile_WriteAndRotate(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "audit.jsonl")
	firstEvent := Event{
		Time:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Action: "tool", Tool: "first1", Status: "ok",
	}
	secondEvent := Event{
		Time:   time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC),
		Action: "tool", Tool: "second", Status: "ok",
	}
	first, err := json.Marshal(firstEvent)
	if err != nil {
		t.Fatal(err)
	}
	first = append(first, '\n')
	second, err := json.Marshal(secondEvent)
	if err != nil {
		t.Fatal(err)
	}
	second = append(second, '\n')
	if len(first) != len(second) {
		t.Fatalf("fixture sizes differ: %d and %d", len(first), len(second))
	}
	rf, err := OpenRotating(p, int64(len(second)), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rf.Close() }()
	logger := New(rf)
	logger.Log(firstEvent)
	logger.Log(secondEvent)
	for path, want := range map[string][]byte{p: second, p + ".1": first} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(content, want) {
			t.Fatalf("%s=%q, want=%q", path, content, want)
		}
		if !json.Valid(bytes.TrimSpace(content)) {
			t.Fatalf("%s does not contain complete JSON", path)
		}
	}
}

func TestRotatingFile_RecoversAfterRotationFailure(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "audit.jsonl")
	rf, err := OpenRotating(p, 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rf.Close() }()
	if _, err := rf.Write([]byte("one\n")); err != nil {
		t.Fatal(err)
	}
	// Make the backup path an obstacle that is not empty.
	if err := os.Mkdir(p+".1", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p+".1", "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := rf.Write([]byte("two\n")); !errors.Is(err, ErrStorageUnavailable) {
		t.Fatalf("err=%v, want ErrStorageUnavailable", err)
	}
	if err := os.RemoveAll(p + ".1"); err != nil {
		t.Fatal(err)
	}
	if _, err := rf.Write([]byte("two\n")); err != nil {
		t.Fatalf("write after recovery: %v", err)
	}
	content, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "two\n" {
		t.Fatalf("primary=%q", content)
	}
	backup, err := os.ReadFile(p + ".1")
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != "one\n" {
		t.Fatalf("backup=%q", backup)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", st.Mode().Perm())
	}
}

func TestRotatingFile_CloseStopsRecovery(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "audit.jsonl")
	rf, err := OpenRotating(p, 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rf.Write([]byte("one\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p+".1", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p+".1", "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := rf.Write([]byte("two\n")); !errors.Is(err, ErrStorageUnavailable) {
		t.Fatalf("err=%v, want ErrStorageUnavailable", err)
	}
	if err := os.RemoveAll(p + ".1"); err != nil {
		t.Fatal(err)
	}
	if err := rf.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := rf.Write([]byte("two\n")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("err=%v, want io.ErrClosedPipe", err)
	}
}

func TestRotatingFile_RejectsRecordLargerThanLimit(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "audit.jsonl")
	if err := os.WriteFile(p, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rf, err := OpenRotating(p, 8, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rf.Close() }()

	if n, err := rf.Write([]byte("123456789")); err == nil || n != 0 {
		t.Fatalf("oversized write=(%d, %v), want=(0, error)", n, err)
	}
	content, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "old\n" {
		t.Fatalf("primary changed after rejected write: %q", content)
	}
	if _, err := os.Stat(p + ".1"); !os.IsNotExist(err) {
		t.Fatalf("rejected write created a backup: %v", err)
	}
}

func TestOpenRotating_TightensExistingPermissions(t *testing.T) {
	p := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := os.WriteFile(p, []byte("existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rf, err := OpenRotating(p, 1024, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := rf.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", st.Mode().Perm())
	}
}

func TestOpenRotating_RejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "audit.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRotating(link, 1024, 2); err == nil {
		t.Fatal("expected symlink rejection")
	}
	st, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o644 {
		t.Fatalf("symlink target was modified: mode=%o", st.Mode().Perm())
	}
}

type failingWriter struct{ err error }

func (f failingWriter) Write([]byte) (int, error) { return 0, f.err }

func TestMultiWriter_ContinuesAfterOneFailure(t *testing.T) {
	var buf bytes.Buffer
	writer := MultiWriter(failingWriter{err: io.ErrClosedPipe}, &buf)
	if _, err := writer.Write([]byte("event\n")); err == nil {
		t.Fatal("a failed destination must be reported")
	}
	if buf.String() != "event\n" {
		t.Fatalf("healthy destination got %q", buf.String())
	}
	allFail := MultiWriter(failingWriter{err: io.ErrClosedPipe}, failingWriter{err: io.ErrShortWrite})
	if _, err := allFail.Write([]byte("event\n")); err == nil {
		t.Fatal("all destinations failed without an error")
	}
}

func TestLogger_PersistentFileSurvivesFailingStderr(t *testing.T) {
	var file bytes.Buffer
	logger := New(MultiWriter(failingWriter{err: io.ErrClosedPipe}, &file), failingWriter{err: io.ErrClosedPipe})
	logger.Log(Event{Action: "tool", Tool: "service_status", Status: "ok"})
	line := file.String()
	if !strings.HasSuffix(line, "\n") || !strings.Contains(line, `"status":"ok"`) {
		t.Fatalf("file did not receive a complete record: %q", line)
	}
	var event Event
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		t.Fatalf("torn JSONL record: %v", err)
	}
}

func TestOpenRotating_RepairsTornTail(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "audit.jsonl")
	complete := `{"action":"boot","status":"ok"}` + "\n"
	if err := os.WriteFile(p, []byte(complete+`{"action":"tool","sta`), 0o600); err != nil {
		t.Fatal(err)
	}
	rf, err := OpenRotating(p, 1<<20, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rf.Write([]byte(`{"action":"tool","status":"error"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := rf.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("repaired file has %d lines: %q", len(lines), raw)
	}
	for _, line := range lines {
		var event Event
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("invalid JSONL after repair: %v in %q", err, line)
		}
	}
}

func TestOpenRotating_RejectsSecondProcess(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "audit.jsonl")
	first, err := OpenRotating(p, 1<<20, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRotating(p, 1<<20, 3); !errors.Is(err, ErrStorageInUse) {
		t.Fatalf("second owner err=%v, want ErrStorageInUse", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := OpenRotating(p, 1<<20, 3)
	if err != nil {
		t.Fatalf("lock was not released: %v", err)
	}
	_ = second.Close()
}
