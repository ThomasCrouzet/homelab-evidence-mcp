package audit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRotatingFile_WriteAndRotate(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "audit.jsonl")
	rf, err := OpenRotating(p, 32, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rf.Close() }()
	if _, err := rf.Write([]byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := rf.Write([]byte("bbbb\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
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
