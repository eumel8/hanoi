package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalStorageChecks(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	results := filepath.Join(dir, "results")
	checks := Local(blocked, results)
	if checks[0].Status != "FAIL" || checks[1].Status != "FAIL" || checks[2].Status != "OK" {
		t.Fatalf("unexpected checks: %+v", checks)
	}
	files, err := os.ReadDir(results)
	if err != nil || len(files) != 0 {
		t.Fatalf("probe files not cleaned up: %v, %v", files, err)
	}
}
