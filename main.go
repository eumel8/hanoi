package main

import (
	"fmt"
	"os"

	"hanoi/internal/app"
)

func main() {
	if err := app.Run(version); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
