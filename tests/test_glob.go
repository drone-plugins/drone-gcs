package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattn/go-zglob"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run test_glob.go <glob_pattern>")
		os.Exit(1)
	}

	pattern := os.Args[1]
	fmt.Printf("Testing glob pattern: %s\n", pattern)

	// Test if the pattern contains glob characters
	hasGlob := strings.ContainsAny(pattern, "*?[]{}")
	fmt.Printf("Contains glob characters: %v\n", hasGlob)

	// Try with standard filepath.Glob first for comparison
	stdMatches, err := filepath.Glob(pattern)
	if err != nil {
		fmt.Printf("Standard filepath.Glob error: %v\n", err)
	} else {
		fmt.Printf("Standard filepath.Glob matched %d files\n", len(stdMatches))
		for i, match := range stdMatches {
			if i < 5 {
				fmt.Printf(" - %s\n", match)
			} else if i == 5 {
				fmt.Println(" - ...")
				break
			}
		}
	}

	// Now try with zglob
	zglobMatches, err := zglob.Glob(pattern)
	if err != nil {
		fmt.Printf("zglob.Glob error: %v\n", err)
	} else {
		fmt.Printf("zglob.Glob matched %d files\n", len(zglobMatches))
		for i, match := range zglobMatches {
			if i < 5 {
				fmt.Printf(" - %s\n", match)
			} else if i == 5 {
				fmt.Println(" - ...")
				break
			}
		}
	}
}
