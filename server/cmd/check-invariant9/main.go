package main

import (
	"fmt"
	"os"

	"github.com/halpworld/halpcheers/server/internal/obs"
)

func main() {
	rootDir := "."
	if len(os.Args) > 1 {
		rootDir = os.Args[1]
	}

	violations, err := obs.CheckInvariant9(rootDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error checking invariant 9: %v\n", err)
		os.Exit(2)
	}

	if len(violations) > 0 {
		fmt.Fprintf(os.Stderr, "========================================================\n")
		fmt.Fprintf(os.Stderr, "FAILED: Found %d AGENTS.md Invariant 9 violation(s)!\n", len(violations))
		fmt.Fprintf(os.Stderr, "========================================================\n")
		for _, v := range violations {
			fmt.Fprintf(os.Stderr, "%s\n", v)
		}
		fmt.Fprintf(os.Stderr, "\nReview AGENTS.md Invariant 9: No identifiers in logs or metrics.\n")
		os.Exit(1)
	}

	fmt.Println("Invariant 9 check passed: zero identifier leaks detected in logs/formatters.")
}
