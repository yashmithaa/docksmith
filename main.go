package main

import (
	"fmt"
	"os"

	"docksmith/cmd"
	"docksmith/internal/runtime"
)

func main() {
	// Check for internal child re-exec signal for container isolation.
	// This must be checked BEFORE any other processing.
	if len(os.Args) > 1 && os.Args[1] == "__isolate" {
		runtime.DispatchIsolateChild(os.Args[2:])
		return
	}

	if err := cmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
