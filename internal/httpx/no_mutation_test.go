package httpx_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAdapterSourcesHaveNoMutationHTTP(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "adapters"))
	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no adapter files")
	}
	forbidden := []string{
		"http.MethodPost", "http.MethodPut", "http.MethodPatch", "http.MethodDelete",
		"MethodPost", "MethodPut", "MethodPatch", "MethodDelete",
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var code []string
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			code = append(code, line)
		}
		body := strings.Join(code, "\n")
		for _, bad := range forbidden {
			if strings.Contains(body, bad) {
				t.Errorf("%s contains %q", f, bad)
			}
		}
		// No Post or Put form method calls.
		for _, bad := range []string{".Post(", ".Put(", ".Patch(", ".Delete("} {
			if strings.Contains(body, bad) {
				t.Errorf("%s contains %q", f, bad)
			}
		}
	}
}
