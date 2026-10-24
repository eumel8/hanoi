// Package doctor provides local runtime checks without invoking tools or models.
package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type Check struct {
	Name   string
	Status string
	Detail string
}

// Local checks Bash availability and verifies storage using temporary files.
// Missing directories are created; probe files are removed immediately.
func Local(workDir, resultsDir string) []Check {
	checks := []Check{}
	path, err := exec.LookPath("bash")
	if err != nil {
		checks = append(checks, Check{"Bash", "FAIL", "executable not found in PATH"})
	} else {
		checks = append(checks, Check{"Bash", "OK", path})
	}
	for _, dir := range []struct{ name, path string }{{"Working directory", workDir}, {"Tool results", resultsDir}} {
		check := Check{dir.name, "OK", dir.path + " (writable)"}
		if err := writable(dir.path); err != nil {
			check.Status, check.Detail = "FAIL", dir.path+": "+err.Error()
		}
		checks = append(checks, check)
	}
	return checks
}

func writable(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".doctor-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.WriteString("hanoi doctor\n")
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func Render(checks []Check) string {
	var out strings.Builder
	for _, check := range checks {
		fmt.Fprintf(&out, "[%s] %s\n  %s\n\n", check.Status, check.Name, check.Detail)
	}
	return strings.TrimSpace(out.String())
}
