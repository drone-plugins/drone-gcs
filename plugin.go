package main

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"math/rand"
	"mime"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"github.com/pkg/errors"
	"google.golang.org/api/iterator"
)

type (
	Config struct {
		Token string

		// Indicates the files ACL's to apply
		ACL []string

		// Copies the files from the specified directory.
		Source string

		// Destination to copy files to, including bucket name
		Target string

		// if true, plugin is set to download mode, which means `source` from the bucket will be downloaded
		Download bool

		// Exclude files matching this pattern.
		Ignore string

		Gzip         []string
		CacheControl string
		Metadata     map[string]string

		// OIDC Config
		workloadPoolId      string
		providerId          string
		gcpProjectId        string
		serviceAccountEmail string
		OidcIdToken         string
		EnableProxy         bool
	}

	Plugin struct {
		Config Config

		bucket *storage.BucketHandle

		printf func(string, ...interface{})
		fatalf func(string, ...interface{})

		ecodeMu sync.Mutex
		ecode   int
	}
)

const (
	maxConcurrent     = 100 // maxConcurrent is the highest upload concurrency. It cannot be 0.
	harnessHTTPProxy  = "HARNESS_HTTP_PROXY"
	harnessHTTPSProxy = "HARNESS_HTTPS_PROXY"
	harnessNoProxy    = "HARNESS_NO_PROXY"
	httpProxy         = "HTTP_PROXY"
	httpsProxy        = "HTTPS_PROXY"
	noProxy           = "NO_PROXY"
)

// Exec executes the plugin
func (p *Plugin) Exec(client *storage.Client) error {

	if p.Config.EnableProxy {
		log.Printf("setting proxy config for upload")
		setSecureConnectProxies()
	}

	sort.Strings(p.Config.Gzip)
	rand.Seed(time.Now().UnixNano()) //nolint: staticcheck

	p.printf = log.Printf
	p.fatalf = log.Fatalf

	// extract bucket name from the target path
	tgt := strings.SplitN(p.Config.Target, "/", 2)
	bname := tgt[0]

	if len(tgt) == 1 {
		p.Config.Target = ""
	} else {
		p.Config.Target = tgt[1]
	}

	p.bucket = client.Bucket(strings.Trim(bname, "/"))

	// If in download mode, call the Download method
	if p.Config.Download {
		bname, remainingPath := extractBucketName(p.Config.Source)
		p.Config.Source = remainingPath

		p.bucket = client.Bucket(strings.Trim(bname, "/"))

		log.Println("Downloading objects from bucket: ", bname, " using path: ", remainingPath)

		ctx := context.Background()
		query := &storage.Query{Prefix: p.Config.Source}

		return p.downloadObjects(ctx, query)
	}

	// create a list of files to upload using glob pattern expansion
	p.printf("expanding source patterns: %s", p.Config.Source)

	// Expand glob patterns into actual source paths
	expandedSources, err := p.expandGlobPatterns(p.Config.Source)
	if err != nil {
		return errors.Wrap(err, "failed to expand source patterns")
	}

	p.printf("found %d source paths after expansion", len(expandedSources))

	// Walk all expanded sources to collect files with their source directories
	fileToSourceMap, err := p.walkGlobFilesWithSources(expandedSources)
	if err != nil {
		p.fatalf("failed to collect files from source patterns: %v", err)
	}

	// Extract just the file list for compatibility
	src := make([]string, 0, len(fileToSourceMap))
	for file := range fileToSourceMap {
		src = append(src, file)
	}

	// result contains upload result of a single file
	type result struct {
		name string
		err  error
	}

	// upload all files in a goroutine, maxConcurrent at a time
	buf := make(chan struct{}, maxConcurrent)
	res := make(chan *result, len(src))

	for _, f := range src {
		buf <- struct{}{} // alloc one slot

		go func(f string) {
			// Get the correct source directory for this file
			sourceDir := fileToSourceMap[f]
			rel, err := filepath.Rel(sourceDir, f)

			if err != nil {
				res <- &result{f, err}
				return
			}

			err = p.uploadFile(path.Join(p.Config.Target, rel), f)
			res <- &result{rel, err}

			<-buf // free up
		}(f)
	}

	// wait for all files to be uploaded or stop at first error
	for range src {
		r := <-res

		if r.err != nil {
			p.fatalf("%s: %v", r.name, r.err)
		}

		p.printf(r.name)
	}

	return nil
}

// errorf sets exit code to a non-zero value and outputs using printf.
func (p *Plugin) errorf(format string, args ...interface{}) {
	p.ecodeMu.Lock()
	p.ecode = 1
	p.ecodeMu.Unlock()
	p.printf(format, args...)
}

// uploadFile uploads the file to dst using global bucket.
// To get a more robust upload use retryUpload instead.
func (p *Plugin) uploadFile(dst, file string) error {
	r, gz, err := p.gzipper(file)

	if err != nil {
		return err
	}

	defer r.Close()

	w := p.bucket.Object(dst).NewWriter(context.Background())
	w.CacheControl = p.Config.CacheControl
	w.Metadata = p.Config.Metadata

	for _, s := range p.Config.ACL {
		a := strings.SplitN(s, ":", 2)

		if len(a) != 2 {
			return fmt.Errorf("%s: invalid ACL %q", dst, s)
		}

		w.ACL = append(w.ACL, storage.ACLRule{
			Entity: storage.ACLEntity(a[0]),
			Role:   storage.ACLRole(a[1]),
		})
	}

	w.ContentType = mime.TypeByExtension(filepath.Ext(file))

	if w.ContentType == "" {
		w.ContentType = "application/octet-stream"
	}

	if gz {
		w.ContentEncoding = "gzip"
	}

	if _, err := io.Copy(w, r); err != nil {
		return err
	}

	return w.Close()
}

// gzipper returns a stream of file and a boolean indicating
// whether the stream is gzip-compressed.
//
// The stream is compressed if p.Gzip contains file extension.
func (p *Plugin) gzipper(file string) (io.ReadCloser, bool, error) {
	r, err := os.Open(file)

	if err != nil || !p.matchGzip(file) {
		return r, false, err
	}

	pr, pw := io.Pipe()
	w := gzip.NewWriter(pw)

	go func() {
		_, err := io.Copy(w, r)

		if err != nil {
			p.errorf("%s: io.Copy: %v", file, err)
		}

		if err := w.Close(); err != nil {
			p.errorf("%s: gzip: %v", file, err)
		}

		if err := pw.Close(); err != nil {
			p.errorf("%s: pipe: %v", file, err)
		}

		r.Close()
	}()
	return pr, true, nil
}

// matchGzip reports whether the file should be gzip-compressed during upload.
// Compressed files should be uploaded with "gzip" content-encoding.
func (p *Plugin) matchGzip(file string) bool {
	ext := filepath.Ext(file)

	if ext == "" {
		return false
	}

	ext = ext[1:]
	i := sort.SearchStrings(p.Config.Gzip, ext)

	return i < len(p.Config.Gzip) && p.Config.Gzip[i] == ext
}

// isGlobPattern checks if a path contains glob pattern characters
func isGlobPattern(path string) bool {
	return strings.ContainsAny(path, "*?[]") || strings.Contains(path, "**")
}

// expandGlobPatterns expands glob patterns and comma-separated paths into a list of actual paths
func (p *Plugin) expandGlobPatterns(patterns string) ([]string, error) {
	if patterns == "" {
		return nil, fmt.Errorf("source pattern cannot be empty")
	}

	// Split by comma to support multiple patterns
	patternList := strings.Split(patterns, ",")
	var allPaths []string

	for _, pattern := range patternList {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}

		paths, err := p.expandSinglePattern(pattern)
		if err != nil {
			return nil, err
		}

		if len(paths) == 0 {
			return nil, fmt.Errorf("glob pattern '%s' matched no files or directories", pattern)
		}

		allPaths = append(allPaths, paths...)
	}

	// Remove duplicates while preserving order
	return p.removeDuplicatePaths(allPaths), nil
}

// expandSinglePattern expands a single glob pattern or returns the path as-is if not a glob
func (p *Plugin) expandSinglePattern(pattern string) ([]string, error) {
	// Convert to absolute path if relative
	if !filepath.IsAbs(pattern) {
		pwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("failed to get working directory: %w", err)
		}
		pattern = filepath.Join(pwd, pattern)
	}

	// If not a glob pattern, check if path exists and return as-is
	if !isGlobPattern(pattern) {
		if _, err := os.Stat(pattern); err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("source path '%s' does not exist", pattern)
			}
			if os.IsPermission(err) {
				return nil, fmt.Errorf("permission denied accessing '%s': %w", pattern, err)
			}
			return nil, fmt.Errorf("error accessing '%s': %w", pattern, err)
		}
		return []string{pattern}, nil
	}

	// Handle double-star (**) patterns for recursive matching
	if strings.Contains(pattern, "**") {
		return p.expandDoubleStarPattern(pattern)
	}

	// Use standard filepath.Glob for simple patterns
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid glob pattern '%s': %w", pattern, err)
	}

	return matches, nil
}

// expandDoubleStarPattern handles ** (recursive) glob patterns
func (p *Plugin) expandDoubleStarPattern(pattern string) ([]string, error) {
	// Split pattern at ** to get base path and suffix pattern
	parts := strings.Split(pattern, "**")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid double-star pattern '%s': only one ** is supported", pattern)
	}

	basePath := strings.TrimSuffix(parts[0], string(filepath.Separator))
	suffixPattern := strings.TrimPrefix(parts[1], string(filepath.Separator))

	// Ensure base path exists
	if _, err := os.Stat(basePath); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("base path '%s' does not exist", basePath)
		}
		return nil, fmt.Errorf("error accessing base path '%s': %w", basePath, err)
	}

	var matches []string
	err := filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Log permission errors but continue
			if os.IsPermission(err) {
				p.printf("Warning: permission denied accessing '%s', skipping", path)
				return nil
			}
			return err
		}

		// Skip if it's the base path itself
		if path == basePath {
			return nil
		}

		// Get relative path from base
		rel, err := filepath.Rel(basePath, path)
		if err != nil {
			return err
		}

		// Match against suffix pattern
		matched := true
		if suffixPattern != "" {
			matched, err = filepath.Match(suffixPattern, rel)
			if err != nil {
				return fmt.Errorf("invalid suffix pattern '%s': %w", suffixPattern, err)
			}
		}

		if matched {
			matches = append(matches, path)
		}

		return nil
	})

	return matches, err
}

// removeDuplicatePaths removes duplicate paths while preserving order
func (p *Plugin) removeDuplicatePaths(paths []string) []string {
	seen := make(map[string]bool)
	var unique []string

	for _, path := range paths {
		if !seen[path] {
			seen[path] = true
			unique = append(unique, path)
		}
	}

	return unique
}

// walkGlobFiles creates a complete set of files to upload from multiple source paths
// It supports glob patterns and maintains the ignore pattern behavior for each source
func (p *Plugin) walkGlobFiles(sources []string) ([]string, error) {
	fileToSourceMap, err := p.walkGlobFilesWithSources(sources)
	if err != nil {
		return nil, err
	}

	// Extract just the file list
	files := make([]string, 0, len(fileToSourceMap))
	for file := range fileToSourceMap {
		files = append(files, file)
	}

	// Remove duplicates that might occur from overlapping glob patterns
	return p.removeDuplicatePaths(files), nil
}

// walkGlobFilesWithSources creates a map of files to their source directories
// This is needed to calculate correct relative paths for upload
func (p *Plugin) walkGlobFilesWithSources(sources []string) (map[string]string, error) {
	fileToSourceMap := make(map[string]string)

	// Get current working directory for root-level patterns
	pwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get working directory: %w", err)
	}

	for _, source := range sources {
		files, err := p.walkSingleSource(source)
		if err != nil {
			return nil, fmt.Errorf("error processing source '%s': %w", source, err)
		}

		// Determine the base directory for relative path calculation
		var baseDir string

		// Ensure source is absolute first
		absSource := source
		if !filepath.IsAbs(source) {
			var err error
			absSource, err = filepath.Abs(source)
			if err != nil {
				return nil, fmt.Errorf("failed to get absolute path for '%s': %w", source, err)
			}
		}

		if info, err := os.Stat(absSource); err == nil && !info.IsDir() {
			// If source is a file (from glob expansion), use its directory as base
			baseDir = filepath.Dir(absSource)
		} else {
			// If source is a directory, use it as base
			baseDir = absSource
		}

		// Handle edge cases for base directory
		if baseDir == "." || baseDir == "" {
			// For current directory references, use absolute path
			baseDir = pwd
		}

		// Ensure baseDir is absolute for consistent relative path calculation
		if !filepath.IsAbs(baseDir) {
			baseDir = filepath.Join(pwd, baseDir)
		}

		// Map each file to its source base directory
		for _, file := range files {
			// Only add if not already present (avoid duplicates from overlapping patterns)
			if _, exists := fileToSourceMap[file]; !exists {
				fileToSourceMap[file] = baseDir
			}
		}
	}

	return fileToSourceMap, nil
}

// walkSingleSource walks a single source path and collects files
// This replaces the logic from the original walkFiles function
func (p *Plugin) walkSingleSource(sourcePath string) ([]string, error) {
	var items []string

	// Check if source is a file or directory
	info, err := os.Stat(sourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("source path '%s' does not exist", sourcePath)
		}
		if os.IsPermission(err) {
			return nil, fmt.Errorf("permission denied accessing '%s': %w", sourcePath, err)
		}
		return nil, fmt.Errorf("error accessing '%s': %w", sourcePath, err)
	}

	// If it's a file, add it directly (after checking ignore pattern)
	if !info.IsDir() {
		// For files, use the directory as the source path for ignore pattern matching
		sourceDir := filepath.Dir(sourcePath)
		if p.shouldIgnoreFile(sourceDir, sourcePath) {
			return items, nil // Return empty list if file should be ignored
		}
		return []string{sourcePath}, nil
	}

	// If it's a directory, walk it recursively
	err = filepath.Walk(sourcePath, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			// Log permission errors but continue
			if os.IsPermission(err) {
				p.printf("Warning: permission denied accessing '%s', skipping", path)
				return nil
			}
			return err
		}

		if fi.IsDir() {
			return nil
		}

		if p.shouldIgnoreFile(sourcePath, path) {
			return nil
		}

		items = append(items, path)
		return nil
	})

	return items, err
}

// shouldIgnoreFile checks if a file should be ignored based on the ignore pattern
// It maintains backward compatibility with the original ignore logic
func (p *Plugin) shouldIgnoreFile(sourcePath string, filePath string) bool {
	if p.Config.Ignore == "" {
		return false
	}

	// Get relative path from source for ignore pattern matching
	rel, err := filepath.Rel(sourcePath, filePath)
	if err != nil {
		p.printf("Warning: failed to get relative path for '%s': %v", filePath, err)
		return false
	}

	// Support multiple ignore patterns separated by comma
	ignorePatterns := strings.Split(p.Config.Ignore, ",")
	for _, pattern := range ignorePatterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}

		matched, err := filepath.Match(pattern, rel)
		if err != nil {
			p.printf("Warning: invalid ignore pattern '%s': %v", pattern, err)
			continue
		}

		if matched {
			return true
		}
	}

	return false
}

// extractBucketName extracts the bucket name from the target path.
func extractBucketName(source string) (string, string) {
	src := strings.SplitN(source, "/", 2)
	if len(src) == 1 {
		return src[0], ""
	}
	return src[0], src[1]
}

// downloadObject downloads a single object from GCS
func (p *Plugin) downloadObject(ctx context.Context, objAttrs *storage.ObjectAttrs) error {
	// Create the destination file path
	destination := filepath.Join(p.Config.Target, objAttrs.Name)
	log.Println("Destination: ", destination)

	// Extract the directory from the destination path
	dir := filepath.Dir(destination)

	// Create the directory and any necessary parent directories
	if err := os.MkdirAll(dir, os.ModePerm); err != nil {
		return errors.Wrap(err, "error creating directories")
	}

	// Create a file to write the downloaded object
	file, err := os.Create(destination)
	if err != nil {
		return errors.Wrap(err, "error creating destination file")
	}
	defer file.Close()

	// Open the GCS object for reading
	reader, err := p.bucket.Object(objAttrs.Name).NewReader(ctx)
	if err != nil {
		return errors.Wrap(err, "error opening GCS object for reading")
	}
	defer reader.Close()

	// Copy the contents of the GCS object to the local file
	_, err = io.Copy(file, reader)
	if err != nil {
		return errors.Wrap(err, "error copying GCS object contents to local file")
	}

	return nil
}

// downloadObjects downloads all objects in the specified GCS bucket path
func (p *Plugin) downloadObjects(ctx context.Context, query *storage.Query) error {
	// List the objects in the specified GCS bucket path
	it := p.bucket.Objects(ctx, query)

	for {
		objAttrs, err := it.Next()

		if err == iterator.Done {
			break
		}

		if err != nil {
			return errors.Wrap(err, "error while iterating through GCS objects")
		}

		if err := p.downloadObject(ctx, objAttrs); err != nil {
			return err
		}
	}

	return nil
}

func setSecureConnectProxies() {
	copyEnvVariableIfExists(harnessHTTPProxy, httpProxy)
	copyEnvVariableIfExists(harnessHTTPSProxy, httpsProxy)
	copyEnvVariableIfExists(harnessNoProxy, noProxy)
}

func copyEnvVariableIfExists(src string, dest string) {
	srcValue := os.Getenv(src)
	if srcValue == "" {
		return
	}
	err := os.Setenv(dest, srcValue)
	if err != nil {
		log.Printf("Failed to copy env variable from %s to %s with error %v", src, dest, err)
	}
}
