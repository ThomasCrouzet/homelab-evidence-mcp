package audit

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
