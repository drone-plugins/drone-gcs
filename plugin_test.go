// Copyright 2015 Google Inc
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"cloud.google.com/go/storage"
	"golang.org/x/net/context"
	"google.golang.org/api/option"
)

var plugin Plugin

type fakeTransport struct {
	f func(*http.Request) (*http.Response, error)
}

func (ft *fakeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return ft.f(r)
}

func gunzip(t *testing.T, bz []byte) []byte {
	r, err := gzip.NewReader(bytes.NewReader(bz))
	if err != nil {
		t.Errorf("gunzip NewReader: %v", err)
		return bz
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Errorf("gunzip read: %v", err)
		return bz
	}
	return b
}

func mkdirs(t *testing.T, path ...string) {
	p := filepath.Join(path...)
	if err := os.MkdirAll(p, 0755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", p, err)
	}
}

func writeFile(t *testing.T, dir, name string, b []byte) {
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0644); err != nil {
		t.Fatalf("WriteFile(%q): %v", p, err)
	}
}

func TestUploadFile(t *testing.T) {
	wdir, err := os.MkdirTemp("", "drone-gcs-test")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, wdir, "file", []byte("test"))
	plugin.Config.Source = wdir

	tests := []struct {
		name                 string
		retries, failOnRetry int
		expectOk             bool
	}{
		{"zero retries, fail on 1. ok", 0, 1, true},
		{"2 retries, fail on 1. NOT ok", 2, 1, false},
	}
	for i, test := range tests {
		var numberOfRetries int

		rt := &fakeTransport{func(r *http.Request) (*http.Response, error) {
			_, mp, _ := mime.ParseMediaType(r.Header.Get("content-type"))
			mr := multipart.NewReader(r.Body, mp["boundary"])
			_, _ = mr.NextPart() // skip metadata
			p, _ := mr.NextPart()
			b, _ := io.ReadAll(p)
			// verify the body is always sent with correct content
			if v := string(b); v != "test" {
				t.Errorf("%d/%d: b = %q; want 'test'", i, numberOfRetries, b)
			}

			res := &http.Response{
				Body:       io.NopCloser(strings.NewReader(`{"name": "fake"}`)),
				Proto:      "HTTP/1.0",
				ProtoMajor: 1,
				ProtoMinor: 0,
				StatusCode: http.StatusServiceUnavailable,
			}

			// The storage.Client does not retry on 404s
			// https://godoc.org/cloud.google.com/go/storage
			// https://cloud.google.com/storage/docs/exponential-backoff
			if numberOfRetries >= test.failOnRetry {
				res.StatusCode = http.StatusNotFound
			}

			if numberOfRetries >= test.retries {
				res.StatusCode = http.StatusOK
			}

			numberOfRetries++
			return res, nil
		}}
		hc := &http.Client{Transport: rt}
		client, _ := storage.NewClient(context.Background(), option.WithHTTPClient(hc))
		plugin.bucket = client.Bucket("bucket")

		err := plugin.uploadFile("file", filepath.Join(wdir, "file"))

		switch {
		case test.expectOk && err != nil:
			t.Errorf("'%s'%d: %v", test.name, i, err)
		case !test.expectOk && err == nil:
			t.Errorf("%d: wanted error", i)
		}
	}
}

func TestRun(t *testing.T) {
	wdir, err := os.MkdirTemp("", "drone-gcs-test")
	if err != nil {
		t.Fatal(err)
	}
	updir := filepath.Join(wdir, "upload")
	subdir := filepath.Join(updir, "sub")
	mkdirs(t, subdir)
	writeFile(t, updir, "file.txt", []byte("text"))
	writeFile(t, updir, "file.js", []byte("javascript"))
	writeFile(t, subdir, "file.css", []byte("sub style"))
	writeFile(t, subdir, "file.bin", []byte("rubbish"))

	files := map[string]*struct {
		ctype string
		body  []byte
		gzip  bool
	}{
		"dir/file.txt":     {"text/plain", []byte("text"), false},
		"dir/file.js":      {"text/javascript", []byte("javascript"), true},
		"dir/sub/file.css": {"text/css", []byte("sub style"), false},
	}

	plugin.Config.Source = wdir + "/upload"
	plugin.Config.Target = "bucket/dir/"
	plugin.Config.Ignore = "sub/*.bin"
	plugin.Config.Gzip = []string{"js"}
	plugin.Config.CacheControl = "public,max-age=10"
	plugin.Config.Metadata = map[string]string{"x-foo": "bar"}
	acls := []storage.ACLRule{{Entity: "allUsers", Role: "READER"}}
	plugin.Config.ACL = []string{fmt.Sprintf("%s:%s", acls[0].Entity, acls[0].Role)}

	var seenMu sync.Mutex // guards seen
	seen := make(map[string]struct{}, len(files))

	rt := &fakeTransport{func(r *http.Request) (resp *http.Response, e error) {
		resp = &http.Response{
			Body:       io.NopCloser(strings.NewReader(`{"name": "fake"}`)),
			Proto:      "HTTP/1.0",
			ProtoMajor: 1,
			ProtoMinor: 0,
			StatusCode: http.StatusOK,
		}

		if !strings.HasSuffix(r.URL.Path, "/bucket/o") {
			t.Errorf("r.URL.Path = %q; want /bucket/o suffix", r.URL.Path)
		}
		_, mp, err := mime.ParseMediaType(r.Header.Get("content-type"))
		if err != nil {
			t.Errorf("ParseMediaType: %v", err)
			return
		}
		mr := multipart.NewReader(r.Body, mp["boundary"])

		// metadata
		p, err := mr.NextPart()
		if err != nil {
			t.Errorf("meta NextPart: %v", err)
			return
		}
		var attrs storage.ObjectAttrs
		if err := json.NewDecoder(p).Decode(&attrs); err != nil {
			t.Errorf("meta json: %v", err)
			return
		}
		seenMu.Lock()
		seen[attrs.Name] = struct{}{}
		seenMu.Unlock()
		obj := files[attrs.Name]
		if obj == nil {
			t.Errorf("unexpected obj: %+v", attrs)
			return
		}

		if attrs.Bucket != "bucket" {
			t.Errorf("attrs.Bucket = %q; want bucket", attrs.Bucket)
		}
		if attrs.CacheControl != plugin.Config.CacheControl {
			t.Errorf("attrs.CacheControl = %q; want %q", attrs.CacheControl, plugin.Config.CacheControl)
		}
		if obj.gzip && attrs.ContentEncoding != "gzip" {
			t.Errorf("attrs.ContentEncoding = %q; want gzip", attrs.ContentEncoding)
		}
		if !strings.HasPrefix(attrs.ContentType, obj.ctype) {
			t.Errorf("attrs.ContentType = %q; want %q", attrs.ContentType, obj.ctype)
		}
		if !reflect.DeepEqual(attrs.ACL, acls) {
			t.Errorf("attrs.ACL = %v; want %v", attrs.ACL, acls)
		}
		if !reflect.DeepEqual(attrs.Metadata, plugin.Config.Metadata) {
			t.Errorf("attrs.Metadata = %+v; want %+v", attrs.Metadata, plugin.Config.Metadata)
		}

		// media
		p, err = mr.NextPart()
		if err != nil {
			t.Errorf("media NextPart: %v", err)
			return
		}
		b, _ := io.ReadAll(p)
		if attrs.ContentEncoding == "gzip" {
			b = gunzip(t, b)
		}
		if !bytes.Equal(b, obj.body) {
			t.Errorf("media b = %q; want %q", b, obj.body)
		}
		return
	}}

	hc := &http.Client{Transport: rt}
	client, err := storage.NewClient(context.Background(), option.WithHTTPClient(hc))
	if err != nil {
		t.Fatal(err)
	}
	_ = plugin.Exec(client)
	for k := range files {
		if _, ok := seen[k]; !ok {
			t.Errorf("%s didn't get uploaded", k)
		}
	}
}

func TestExtractBucketName(t *testing.T) {
	tests := []struct {
		input        string
		expectedName string
		expectedPath string
	}{
		{
			input:        "bucket-name/object/file.txt",
			expectedName: "bucket-name",
			expectedPath: "object/file.txt",
		},
		{
			input:        "only-bucket",
			expectedName: "only-bucket",
			expectedPath: "",
		},
		{
			input:        "nested/bucket/path/to/object",
			expectedName: "nested",
			expectedPath: "bucket/path/to/object",
		},
		{
			input:        "root/bucket/",
			expectedName: "root",
			expectedPath: "bucket/",
		},
		{
			input:        "object/file.jpg",
			expectedName: "object",
			expectedPath: "file.jpg",
		},
		{
			input:        "no-slash",
			expectedName: "no-slash",
			expectedPath: "",
		},
	}

	for _, tc := range tests {
		name, path := extractBucketName(tc.input)
		if name != tc.expectedName || path != tc.expectedPath {
			t.Errorf("Expected: %s, %s; Got: %s, %s", tc.expectedName, tc.expectedPath, name, path)
		}
	}
}

// TestIsGlobPattern tests the glob pattern detection
func TestIsGlobPattern(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		{"simple/path", false},
		{"path/with/dirs", false},
		{"path/*", true},
		{"path/file?.txt", true},
		{"path/[abc].txt", true},
		{"path/**/file", true},
		{"normal-file.txt", false},
		{"/absolute/path", false},
		{"./relative/path", false},
	}

	for _, tc := range tests {
		result := isGlobPattern(tc.path)
		if result != tc.expected {
			t.Errorf("isGlobPattern(%q) = %v; want %v", tc.path, result, tc.expected)
		}
	}
}

// TestExpandGlobPatterns tests glob pattern expansion
func TestExpandGlobPatterns(t *testing.T) {
	// Create temporary directory structure for testing
	tmpDir, err := os.MkdirTemp("", "drone-gcs-glob-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test structure
	testDir := filepath.Join(tmpDir, "test")
	subDir := filepath.Join(testDir, "sub")
	mkdirs(t, subDir)
	writeFile(t, testDir, "file1.txt", []byte("content1"))
	writeFile(t, testDir, "file2.js", []byte("content2"))
	writeFile(t, subDir, "file3.css", []byte("content3"))

	plugin := &Plugin{
		Config: Config{},
		printf: t.Logf,
	}

	tests := []struct {
		name        string
		pattern     string
		expectedMin int // minimum expected matches
		wantErr     bool
	}{
		{
			name:        "single directory",
			pattern:     testDir,
			expectedMin: 1,
			wantErr:     false,
		},
		{
			name:        "simple glob",
			pattern:     filepath.Join(testDir, "*.txt"),
			expectedMin: 1,
			wantErr:     false,
		},
		{
			name:        "recursive glob",
			pattern:     filepath.Join(testDir, "**"),
			expectedMin: 1,
			wantErr:     false,
		},
		{
			name:        "multiple patterns",
			pattern:     fmt.Sprintf("%s,%s", filepath.Join(testDir, "*.txt"), filepath.Join(testDir, "*.js")),
			expectedMin: 2,
			wantErr:     false,
		},
		{
			name:    "empty pattern",
			pattern: "",
			wantErr: true,
		},
		{
			name:    "non-existent path",
			pattern: "/non/existent/path",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := plugin.expandGlobPatterns(tc.pattern)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got none")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if len(result) < tc.expectedMin {
				t.Errorf("expected at least %d matches, got %d: %v", tc.expectedMin, len(result), result)
			}
		})
	}
}

// TestWalkGlobFiles tests file collection from multiple sources
func TestWalkGlobFiles(t *testing.T) {
	// Create temporary directory structure
	tmpDir, err := os.MkdirTemp("", "drone-gcs-walk-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test structure
	dir1 := filepath.Join(tmpDir, "dir1")
	dir2 := filepath.Join(tmpDir, "dir2")
	mkdirs(t, dir1)
	mkdirs(t, dir2)
	writeFile(t, dir1, "file1.txt", []byte("content1"))
	writeFile(t, dir2, "file2.txt", []byte("content2"))

	plugin := &Plugin{
		Config: Config{},
		printf: t.Logf,
	}

	sources := []string{dir1, dir2}
	files, err := plugin.walkGlobFiles(sources)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}

	if len(files) != 2 {
		t.Errorf("expected 2 files, got %d: %v", len(files), files)
	}
}

// TestShouldIgnoreFile tests ignore pattern functionality
func TestShouldIgnoreFile(t *testing.T) {
	tests := []struct {
		name         string
		ignorePattern string
		sourcePath   string
		filePath     string
		expected     bool
	}{
		{
			name:         "no ignore pattern",
			ignorePattern: "",
			sourcePath:   "/src",
			filePath:     "/src/file.txt",
			expected:     false,
		},
		{
			name:         "simple ignore",
			ignorePattern: "*.log",
			sourcePath:   "/src",
			filePath:     "/src/debug.log",
			expected:     true,
		},
		{
			name:         "no match",
			ignorePattern: "*.log",
			sourcePath:   "/src",
			filePath:     "/src/file.txt",
			expected:     false,
		},
		{
			name:         "multiple patterns - match first",
			ignorePattern: "*.log,*.tmp",
			sourcePath:   "/src",
			filePath:     "/src/debug.log",
			expected:     true,
		},
		{
			name:         "multiple patterns - match second",
			ignorePattern: "*.log,*.tmp",
			sourcePath:   "/src",
			filePath:     "/src/cache.tmp",
			expected:     true,
		},
		{
			name:         "multiple patterns - no match",
			ignorePattern: "*.log,*.tmp",
			sourcePath:   "/src",
			filePath:     "/src/file.txt",
			expected:     false,
		},
	}

	plugin := &Plugin{
		printf: t.Logf,
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plugin.Config.Ignore = tc.ignorePattern
			result := plugin.shouldIgnoreFile(tc.sourcePath, tc.filePath)
			if result != tc.expected {
				t.Errorf("shouldIgnoreFile(%q, %q) with pattern %q = %v; want %v",
					tc.sourcePath, tc.filePath, tc.ignorePattern, result, tc.expected)
			}
		})
	}
}

// TestRootLevelGlobPatterns tests patterns like *.txt in current directory
func TestRootLevelGlobPatterns(t *testing.T) {
	// Create temporary directory structure for testing
	tmpDir, err := os.MkdirTemp("", "root-glob-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Change to temp directory to simulate real scenario
	oldDir, _ := os.Getwd()
	defer os.Chdir(oldDir)
	os.Chdir(tmpDir)

	// Create test files in current directory
	writeFile(t, ".", "op.txt", []byte("test content"))
	writeFile(t, ".", "data.txt", []byte("data content"))
	writeFile(t, ".", "readme.md", []byte("readme content"))

	plugin := &Plugin{
		Config: Config{
			Source: "*.txt", // This is the failing pattern
		},
		printf: t.Logf,
	}

	// Test expansion
	expandedSources, err := plugin.expandGlobPatterns("*.txt")
	if err != nil {
		t.Fatalf("expandGlobPatterns failed: %v", err)
	}

	if len(expandedSources) != 2 {
		t.Fatalf("expected 2 .txt files, got %d: %v", len(expandedSources), expandedSources)
	}

	// Test file collection with source mapping
	fileToSourceMap, err := plugin.walkGlobFilesWithSources(expandedSources)
	if err != nil {
		t.Fatalf("walkGlobFilesWithSources failed: %v", err)
	}

	// Test relative path calculation - this is where the bug occurs
	for file, baseDir := range fileToSourceMap {
		rel, err := filepath.Rel(baseDir, file)
		if err != nil {
			t.Errorf("filepath.Rel(%q, %q) failed: %v - THIS IS THE BUG!", baseDir, file, err)
			continue
		}
		t.Logf("✅ Rel(%q, %q) = %q", baseDir, file, rel)
		
		// Relative path should just be the filename for root-level patterns
		if !strings.HasSuffix(rel, ".txt") {
			t.Errorf("expected relative path to end with .txt, got %q", rel)
		}
	}
}

// TestProductionScenarioReproduction reproduces the exact error from production
func TestProductionScenarioReproduction(t *testing.T) {
	// This test simulates what happens in the Exec function
	tmpDir, err := os.MkdirTemp("", "production-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Change to temp directory
	oldDir, _ := os.Getwd()
	defer os.Chdir(oldDir)
	os.Chdir(tmpDir)

	// Create test file
	writeFile(t, ".", "op.txt", []byte("test content"))

	plugin := &Plugin{
		Config: Config{
			Source: "*.txt", // Original pattern
			Target: "bucket/path",
		},
		printf: t.Logf,
	}

	// Simulate the exact flow from Exec function
	expandedSources, err := plugin.expandGlobPatterns(plugin.Config.Source)
	if err != nil {
		t.Fatalf("expandGlobPatterns failed: %v", err)
	}

	fileToSourceMap, err := plugin.walkGlobFilesWithSources(expandedSources)
	if err != nil {
		t.Fatalf("walkGlobFilesWithSources failed: %v", err)
	}

	// Now simulate the problematic code from Exec function
	for file := range fileToSourceMap {
		// This is the line that fails in production:
		// rel, err := filepath.Rel(p.Config.Source, f)
		// where p.Config.Source is "*.txt" and f is "/harness/op.txt"
		
		// Test old broken behavior (should fail)
		_, err := filepath.Rel(plugin.Config.Source, file)
		if err == nil {
			t.Errorf("Expected old behavior to fail, but it didn't")
			continue
		}
		
		// Test new fixed behavior (should work)
		baseDir := fileToSourceMap[file]
		rel, err := filepath.Rel(baseDir, file)
		if err != nil {
			t.Errorf("Fix failed: filepath.Rel(%q, %q) failed: %v", baseDir, file, err)
			continue
		}
		
		// Verify we get the expected filename
		if rel != "op.txt" {
			t.Errorf("expected 'op.txt', got %q", rel)
		}
	}
}

// TestEndToEndRootLevelGlob tests the complete flow with root-level glob patterns
func TestEndToEndRootLevelGlob(t *testing.T) {
	// Create temporary directory to simulate /harness working directory
	tmpDir, err := os.MkdirTemp("", "harness-simulation")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Change to temp directory (simulate /harness)
	oldDir, _ := os.Getwd()
	defer os.Chdir(oldDir)
	os.Chdir(tmpDir)

	// Create test files directly in current directory
	writeFile(t, ".", "op.txt", []byte("test content"))
	writeFile(t, ".", "data.txt", []byte("data content"))
	writeFile(t, ".", "readme.md", []byte("not matched"))

	plugin := &Plugin{
		Config: Config{
			Source: "*.txt", // Root-level glob pattern
			Target: "my-bucket/uploads",
		},
		printf: t.Logf,
	}

	// Execute the complete flow from Exec function
	expandedSources, err := plugin.expandGlobPatterns(plugin.Config.Source)
	if err != nil {
		t.Fatalf("expandGlobPatterns failed: %v", err)
	}
	t.Logf("✅ Expanded sources: %v", expandedSources)

	// This should find 2 .txt files
	if len(expandedSources) != 2 {
		t.Fatalf("expected 2 .txt files, got %d: %v", len(expandedSources), expandedSources)
	}

	// Collect files with source mapping
	fileToSourceMap, err := plugin.walkGlobFilesWithSources(expandedSources)
	if err != nil {
		t.Fatalf("walkGlobFilesWithSources failed: %v", err)
	}

	// Extract file list for upload simulation
	src := make([]string, 0, len(fileToSourceMap))
	for file := range fileToSourceMap {
		src = append(src, file)
	}

	// Test the relative path calculation (this is what was failing)
	for _, f := range src {
		// Get the correct source directory for this file
		sourceDir := fileToSourceMap[f]
		rel, err := filepath.Rel(sourceDir, f)
		if err != nil {
			t.Errorf("filepath.Rel(%q, %q) failed: %v", sourceDir, f, err)
			continue
		}

		t.Logf("✅ File: %s -> Relative: %s", f, rel)

		// Verify the relative path is just the filename
		if !strings.HasSuffix(rel, ".txt") {
			t.Errorf("expected relative path to be filename, got %q", rel)
		}
		if strings.Contains(rel, "/") {
			t.Errorf("relative path should not contain directory separators, got %q", rel)
		}
	}

	t.Logf("✅ End-to-end test passed - root-level glob patterns work correctly!")
}

// TestHarnessProductionScenario simulates the exact failing production scenario
func TestHarnessProductionScenario(t *testing.T) {
	// Simulate the exact scenario from your production error
	tmpDir, err := os.MkdirTemp("", "harness-prod-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Change to temp directory to simulate /harness working directory
	oldDir, _ := os.Getwd()
	defer os.Chdir(oldDir)
	os.Chdir(tmpDir)

	// Create the exact file from your error: op.txt
	writeFile(t, ".", "op.txt", []byte("production test content"))

	// Simulate the exact configuration from your Harness step
	plugin := &Plugin{
		Config: Config{
			Source: "*.txt", // sourcePath: '*.txt'
			Target: "op-gcs-bucket/path", // bucket: op-gcs-bucket
		},
		printf: t.Logf,
	}

	// Execute the exact same flow that would happen in production

	// Step 1: Expand glob patterns
	expandedSources, err := plugin.expandGlobPatterns(plugin.Config.Source)
	if err != nil {
		t.Fatalf("expandGlobPatterns failed: %v", err)
	}

	// Step 2: Collect files with source mapping  
	fileToSourceMap, err := plugin.walkGlobFilesWithSources(expandedSources)
	if err != nil {
		t.Fatalf("walkGlobFilesWithSources failed: %v", err)
	}

	// Step 3: Extract file list
	src := make([]string, 0, len(fileToSourceMap))
	for file := range fileToSourceMap {
		src = append(src, file)
	}

	// Step 4: Test the critical relative path calculation
	for _, f := range src {
		// This is the line that was failing in production
		sourceDir := fileToSourceMap[f]
		rel, err := filepath.Rel(sourceDir, f)
		if err != nil {
			t.Fatalf("filepath.Rel(%q, %q) failed: %v - production bug not fixed!", sourceDir, f, err)
		}

		// Verify we get the expected filename
		if rel != "op.txt" {
			t.Errorf("expected 'op.txt', got %q", rel)
		}
	}

	// Test passed - fix is working correctly
}

// TestBackwardCompatibility tests that existing functionality still works
func TestBackwardCompatibility(t *testing.T) {
	// Create temporary directory structure
	tmpDir, err := os.MkdirTemp("", "drone-gcs-compat-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test structure similar to original tests
	uploadDir := filepath.Join(tmpDir, "upload")
	subDir := filepath.Join(uploadDir, "sub")
	mkdirs(t, subDir)
	writeFile(t, uploadDir, "file.txt", []byte("text"))
	writeFile(t, uploadDir, "file.js", []byte("javascript"))
	writeFile(t, subDir, "file.css", []byte("sub style"))
	writeFile(t, subDir, "file.bin", []byte("rubbish"))

	plugin := &Plugin{
		Config: Config{
			Source: uploadDir, // Single directory path (backward compatible)
			Ignore: "sub/*.bin", // Ignore pattern (backward compatible)
		},
		printf: t.Logf,
	}

	// Test that single directory path still works
	expandedSources, err := plugin.expandGlobPatterns(plugin.Config.Source)
	if err != nil {
		t.Errorf("unexpected error expanding single directory: %v", err)
		return
	}

	if len(expandedSources) != 1 || expandedSources[0] != uploadDir {
		t.Errorf("expected single source %q, got %v", uploadDir, expandedSources)
		return
	}

	// Test file collection with ignore patterns
	files, err := plugin.walkGlobFiles(expandedSources)
	if err != nil {
		t.Errorf("unexpected error walking files: %v", err)
		return
	}

	// Should have 3 files (excluding the ignored .bin file)
	expectedFiles := 3
	if len(files) != expectedFiles {
		t.Errorf("expected %d files, got %d: %v", expectedFiles, len(files), files)
	}

	// Verify .bin file is excluded
	for _, file := range files {
		if strings.HasSuffix(file, ".bin") {
			t.Errorf("found .bin file %q that should have been ignored", file)
		}
	}
}
