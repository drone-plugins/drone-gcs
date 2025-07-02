package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestContainsGlobCharacters verifies detection of glob patterns
func TestContainsGlobCharacters(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		{"/path/to/file.txt", false},
		{"/path/to/*.txt", true},
		{"/path/to/file[0-9].txt", true},
		{"/path/to/file?.txt", true},
		{"/path/{one,two}/file.txt", true},
		{"C:\\path\\to\\file.txt", false},
		{"C:\\path\\to\\*.txt", true},
	}

	for _, test := range tests {
		result := containsGlobCharacters(test.path)
		if result != test.expected {
			t.Errorf("containsGlobCharacters(%q) = %v; want %v", test.path, result, test.expected)
		}
	}
}

// TestGlobMatching verifies glob pattern matching with various patterns
func TestGlobMatching(t *testing.T) {
	// Create a temporary test directory structure
	tempDir, err := os.MkdirTemp("", "glob-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create test directory structure
	subDir1 := filepath.Join(tempDir, "subdir1")
	subDir2 := filepath.Join(tempDir, "subdir2")
	nestedDir := filepath.Join(subDir1, "nested")

	// Create directories
	dirs := []string{subDir1, subDir2, nestedDir}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("Failed to create directory %s: %v", dir, err)
		}
	}

	// Create test files
	testFiles := map[string][]byte{
		filepath.Join(tempDir, "file1.txt"):     []byte("test1"),
		filepath.Join(tempDir, "file2.jpg"):     []byte("test2"),
		filepath.Join(subDir1, "file3.txt"):     []byte("test3"),
		filepath.Join(subDir2, "file4.txt"):     []byte("test4"),
		filepath.Join(nestedDir, "file5.txt"):   []byte("test5"),
		filepath.Join(nestedDir, "file6.log"):   []byte("test6"),
		filepath.Join(tempDir, "prefix_1.txt"):  []byte("prefix1"),
		filepath.Join(tempDir, "prefix_2.txt"):  []byte("prefix2"),
		filepath.Join(tempDir, "special#.txt"):  []byte("special"),
		filepath.Join(tempDir, ".hidden.txt"):   []byte("hidden"),
		filepath.Join(tempDir, "ignore_me.tmp"): []byte("ignore"),
	}

	for path, content := range testFiles {
		if err := os.WriteFile(path, content, 0644); err != nil {
			t.Fatalf("Failed to create file %s: %v", path, err)
		}
	}

	// Test cases
	tests := []struct {
		name            string
		pattern         string
		exclude         string
		expectedMatches int
		expectError     bool
	}{
		{"Single file match", filepath.Join(tempDir, "file1.txt"), "", 1, false},
		{"All text files in root", filepath.Join(tempDir, "*.txt"), "", 5, false}, // Includes hidden files in our implementation
		{"Nested glob pattern", filepath.Join(tempDir, "**", "*.txt"), "", 8, false}, // All .txt files including nested ones
		{"Multiple extensions", filepath.Join(tempDir, "*.{txt,jpg}"), "", 6, false}, // All .txt and .jpg files in root including hidden
		{"File range pattern", filepath.Join(tempDir, "file[1-2].*"), "", 2, false}, // file1.txt, file2.jpg
		{"Prefix pattern", filepath.Join(tempDir, "prefix_*.txt"), "", 2, false}, // prefix_1.txt, prefix_2.txt
		{"With exclusion", filepath.Join(tempDir, "*.txt"), filepath.Join(tempDir, "prefix_*.txt"), 3, false}, // Our implementation includes hidden files
		{"Hidden files", filepath.Join(tempDir, ".*.txt"), "", 1, false}, // .hidden.txt
		{"No matches pattern", filepath.Join(tempDir, "nonexistent*.txt"), "", 0, false},
		{"Error case - invalid pattern", filepath.Join(tempDir, "[[.txt"), "", 0, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			matches, err := matches(test.pattern, test.exclude)

			// Check error expectation
			if test.expectError && err == nil {
				t.Errorf("Expected error but got nil")
				return
			}

			if !test.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			// Skip further checks if we expected an error
			if test.expectError {
				return
			}

			if len(matches) != test.expectedMatches {
				t.Errorf("matches(%q, %q) returned %d matches; want %d", 
					test.pattern, test.exclude, len(matches), test.expectedMatches)
				t.Logf("Matches found: %v", matches)
			}

			// Verify all matches are files, not directories
			for _, match := range matches {
				info, err := os.Stat(match)
				if err != nil {
					t.Errorf("Failed to stat matched file %s: %v", match, err)
					continue
				}
				if info.IsDir() {
					t.Errorf("Match %s is a directory, expected only files", match)
				}
			}
		})
	}
}

// TestWalkFiles tests the Plugin.walkFiles method with various patterns
func TestWalkFiles(t *testing.T) {
	// Create a temporary test directory structure
	tempDir, err := os.MkdirTemp("", "walk-files-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create test directory structure with files
	subDir := filepath.Join(tempDir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("Failed to create subdir: %v", err)
	}

	// Create test files
	files := []struct {
		path    string
		content []byte
		mode    fs.FileMode
	}{
		{filepath.Join(tempDir, "file1.txt"), []byte("test1"), 0644},
		{filepath.Join(tempDir, "file2.jpg"), []byte("test2"), 0644},
		{filepath.Join(subDir, "file3.txt"), []byte("test3"), 0644},
		{filepath.Join(subDir, "file4.bin"), []byte("test4"), 0644},
		{filepath.Join(tempDir, "no-read.txt"), []byte("no-read"), 0000}, // No read permission
	}

	for _, file := range files {
		if err := os.WriteFile(file.path, file.content, file.mode); err != nil {
			t.Fatalf("Failed to create file %s: %v", file.path, err)
		}
	}

	// Test cases for walkFiles
	tests := []struct {
		name            string
		source          string
		ignore          string
		expectedCount   int
		expectError     bool
	}{
		{"Single file", filepath.Join(tempDir, "file1.txt"), "", 1, false},
		{"Directory path", tempDir, "", 5, false}, // All files including no-read.txt in our implementation
		{"Glob pattern - literal", filepath.Join(tempDir, "file1.txt"), "", 1, false},
		{"With ignore pattern", tempDir, "*.jpg", 4, false}, // All except file2.jpg
		{"Subdirectory with ignore", subDir, "*.bin", 1, false}, // Only file3.txt
		{"Non-existent path", filepath.Join(tempDir, "nonexistent"), "", 0, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plugin := Plugin{
				Config: Config{
					Source: test.source,
					Ignore: test.ignore,
				},
				printf: t.Logf, // Use test logger
				fatalf: t.Fatalf,
			}

			files, err := plugin.walkFiles()

			// Check error expectation
			if test.expectError && err == nil {
				t.Errorf("Expected error but got nil")
				return
			}

			// For expected errors, we're done
			if test.expectError {
				return
			}

			// For non-error cases, check unexpected errors
			if !test.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			// Check file count
			if len(files) != test.expectedCount {
				t.Errorf("walkFiles() returned %d files; want %d", len(files), test.expectedCount)
				t.Logf("Files found: %v", files)
			}

			// Verify all returned paths are files, not directories
			for _, file := range files {
				info, err := os.Stat(file)
				if err != nil {
					t.Errorf("Failed to stat file %s: %v", file, err)
					continue
				}
				if info.IsDir() {
					t.Errorf("Result %s is a directory, expected only files", file)
				}
			}
		})
	}
}

// TestGlobDetection tests the containsGlobCharacters function
func TestGlobDetection(t *testing.T) {
	// Test containsGlobCharacters function
	globs := []struct {
		path     string
		expected bool
	}{
		{"path/to/*.txt", true},
		{"path/[abc]/file.txt", true},
		{"path/{one,two}.txt", true},
		{"file.txt", false},
		{"path/to/file.txt", false},
	}

	for _, test := range globs {
		result := containsGlobCharacters(test.path)
		if result != test.expected {
			t.Errorf("containsGlobCharacters(%q) = %v; want %v", test.path, result, test.expected)
		}
	}
}
