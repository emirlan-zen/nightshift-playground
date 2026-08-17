package main

import (
	"fmt"
	"os"

	controlapp "nightshift/control/internal/control"
)

func main() {
	if err := controlapp.Run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "nightshift-control: %v\n", err)
		os.Exit(1)
	}
}
