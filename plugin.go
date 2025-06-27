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
	"github.com/mattn/go-zglob"
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

	// create a list of files to upload
	if !strings.HasPrefix(p.Config.Source, "/") {
		pwd, err := os.Getwd()

		if err != nil {
			return errors.Wrap(err, "failed to get working dir")
		}

		p.printf("source path relative to %s", pwd)
		p.Config.Source = filepath.Join(pwd, p.Config.Source)
	}

	// Find files using glob pattern match
	src, err := p.walkFiles()

	if err != nil {
		p.fatalf("local files: %v", err)
	}
	
	// Check if we found any files
	if len(src) == 0 {
		p.printf("No files matched the pattern: %s", p.Config.Source)
		return nil  // Not an error, just nothing to do
	}
	
	// Log how many files matched the pattern
	p.printf("Pattern %s matched %d files", p.Config.Source, len(src))

	// result contains upload result of a single file
	type result struct {
		name string
		err  error
	}

	// Process files in chunks if there are many
	return p.processFiles(src)
}

// processFiles handles files in chunks to manage memory usage for large file sets
func (p *Plugin) processFiles(files []string) error {
	const chunkSize = 500
	
	for i := 0; i < len(files); i += chunkSize {
		end := i + chunkSize
		if end > len(files) {
			end = len(files)
		}
		
		chunk := files[i:end]
		if err := p.uploadChunk(chunk); err != nil {
			return err
		}
	}
	
	return nil
}

// uploadChunk uploads a subset of files
func (p *Plugin) uploadChunk(src []string) error {
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
			rel, err := filepath.Rel(p.Config.Source, f)

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
	rel, err := filepath.Rel(p.Config.Source, file)

	if err != nil {
		return err
	}

	name := path.Join(p.Config.Target, rel)
	w := p.bucket.Object(name).NewWriter(context.Background())
	w.CacheControl = p.Config.CacheControl
	w.Metadata = p.Config.Metadata

	for _, s := range p.Config.ACL {
		a := strings.SplitN(s, ":", 2)

		if len(a) != 2 {
			return fmt.Errorf("%s: invalid ACL %q", name, s)
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

// walkFiles creates a complete set of files to upload
// using glob pattern matching for the source path.
// 
// It handles glob patterns in the p.Source path and excludes files matching p.Ignore pattern.
func (p *Plugin) walkFiles() ([]string, error) {
	// Store original source for later use
	originalSource := p.Config.Source
	
	// Convert Windows paths to forward slashes if needed
	p.Config.Source = filepath.ToSlash(p.Config.Source)
	
	// Get matching files using glob pattern
	files, err := matches(p.Config.Source, p.Config.Ignore)
	
	// Restore original source value
	p.Config.Source = originalSource
	
	if err != nil {
		return nil, err
	}
	
	// Filter out directories and check permissions
	var items []string
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			if os.IsPermission(err) {
				// Log permission errors but continue with other files
				p.errorf("permission denied: %s", file)
				continue
			}
			return nil, err
		}
		
		if !info.IsDir() {
			// Verify file is readable before adding to upload list
			if f, err := os.Open(file); err == nil {
				f.Close()
				items = append(items, file)
			} else if os.IsPermission(err) {
				p.errorf("permission denied: %s", file)
			} else {
				return nil, err
			}
		}
	}
	
	return items, nil
}

// matches returns a list of files that match the glob pattern while respecting exclusion patterns
func matches(include string, exclude string) ([]string, error) {
	// Check if the include path is a direct path without glob characters
	// This ensures backward compatibility
	if !containsGlobCharacters(include) {
		// Check if the path exists
		info, err := os.Stat(include)
		if err != nil {
			return nil, err
		}

		// If it's a regular file, just return it
		if !info.IsDir() {
			return []string{include}, nil
		}
		
		// If it's a directory, walk it recursively
		var files []string
		err = filepath.Walk(include, func(path string, fi os.FileInfo, err error) error {
			if err != nil || fi.IsDir() {
				return err
			}

			// Check if the file should be excluded
			if exclude != "" {
				rel, err := filepath.Rel(include, path)
				if err != nil {
					return err
				}
				
				match, err := filepath.Match(exclude, rel)
				if err != nil || match {
					return err
				}
			}
			
			files = append(files, path)
			return nil
		})
		
		return files, err
	}

	// Handle path separators consistently
	normalizedInclude := filepath.ToSlash(include)
	
	// Use zglob for pattern matching
	matches, err := zglob.Glob(normalizedInclude)
	if err != nil {
		return nil, err
	}
	
	// Empty results check - not an error, just return empty slice
	if len(matches) == 0 {
		return []string{}, nil
	}

	// Handle exclusions if specified
	if exclude == "" {
		return matches, nil
	}
	
	excludeMatches, err := filepath.Glob(exclude)
	if err != nil {
		return nil, err
	}

	// Create a map of excluded files for faster lookup
	excludeMap := make(map[string]struct{}, len(excludeMatches))
	for _, f := range excludeMatches {
		excludeMap[f] = struct{}{}
	}

	// Create a new slice without the excluded files
	filtered := make([]string, 0, len(matches))
	for _, f := range matches {
		if _, excluded := excludeMap[f]; !excluded {
			filtered = append(filtered, f)
		}
	}

	return filtered, nil
}

// containsGlobCharacters checks if a path contains glob pattern characters
func containsGlobCharacters(path string) bool {
	return strings.ContainsAny(path, "*?[]{}")
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
