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

		// Exclude files matching this pattern.
		Ignore string

		Gzip         []string
		CacheControl string
		Metadata     map[string]string
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

// maxConcurrent is the highest upload concurrency.
// It cannot be 0.
const maxConcurrent = 100

// Exec executes the plugin
func (p *Plugin) Exec(client *storage.Client) error {
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

	// create a list of files to upload
	if !strings.HasPrefix(p.Config.Source, "/") {
		pwd, err := os.Getwd()

		if err != nil {
			return errors.Wrap(err, "failed to get working dir")
		}

		p.printf("source path relative to %s", pwd)
		p.Config.Source = filepath.Join(pwd, p.Config.Source)
	}

	src, err := p.walkFiles()

	if err != nil {
		p.fatalf("local files: %v", err)
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
// by walking p.Source recursively.
//
// It excludes files matching p.Ignore pattern.
// The ignore pattern is matched using filepath.Match against a partial
// file name, relative to p.Source.
func (p *Plugin) walkFiles() ([]string, error) {
	var items []string

	err := filepath.Walk(p.Config.Source, func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}

		rel, err := filepath.Rel(p.Config.Source, path)

		if err != nil {
			return err
		}

		var ignore bool

		if p.Config.Ignore != "" {
			ignore, err = filepath.Match(p.Config.Ignore, rel)
		}

		if err != nil || ignore {
			return err
		}

		items = append(items, path)
		return nil
	})

	return items, err
}
