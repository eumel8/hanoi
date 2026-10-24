package tools

import (
	"os"
	"path/filepath"
)

func WorkDir() string {
	return filepath.Join(os.TempDir(), "hanoi")
}

func ResultsDir() string {
	return filepath.Join(WorkDir(), "tool-results")
}
