package main

import (
	"os"
	"path/filepath"
)

func toolWorkDir() string {
	return filepath.Join(os.TempDir(), "hanoi")
}

func toolResultsDir() string {
	return filepath.Join(toolWorkDir(), "tool-results")
}
