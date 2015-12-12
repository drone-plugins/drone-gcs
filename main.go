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
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"mime"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/drone/drone-plugin-go/plugin"

	"golang.org/x/net/context"
	"golang.org/x/oauth2/google"

	"google.golang.org/cloud"
	"google.golang.org/cloud/storage"
)

// maxConcurrent is the highest upload concurrency.
const maxConcurrent = 100

var (
	// vargs are provided on stdin of the program
	// and parsed by plugin package.
	vargs struct {
		AuthKey      string            `json:"auth_key"`
		Source       string            `json:"source"`
		Ignore       string            `json:"ignore"`
		Target       string            `json:"target"`
		ACL          []string          `json:"acl"`
		Gzip         []string          `json:"gzip"`
		CacheControl string            `json:"cache_control"`
		Metadata     map[string]string `json:"metadata"`
	}

	// workspace is the repo build workspace.
	workspace plugin.Workspace

	// bucket is the GCS target bucket
	bucket *storage.BucketHandle

	// logging functions
	printf = log.Printf
	fatalf = log.Fatalf

	// program exit code
	ecodeMu sync.Mutex // guards ecode
	ecode   int
)

// errorf sets exit code to a non-zero value and outputs using printf.
func errorf(format string, args ...interface{}) {
	ecodeMu.Lock()
	ecode = 1
	ecodeMu.Unlock()
	printf(format, args...)
}

// result contains upload result of a single file
type result struct {
	name string
	err  error
}

// uploadAll uploads each file sent to channel c in a separate goroutine.
// It reports the upload result to channel r.
// The returned name is a path relative to vargs.Source.
//
// The target object path is created from vargs.Target and the file name
// relative to vargs.Source.
func uploadAll(c <-chan string, r chan<- *result) {
	for f := range c {
		go func(f string) {
			rel, err := filepath.Rel(vargs.Source, f)
			if err != nil {
				r <- &result{f, err}
				return
			}
			err = uploadFile(path.Join(vargs.Target, rel), f)
			r <- &result{rel, err}
		}(f)
	}
}

// uploadFile uploads the file to dst using global bucket.
func uploadFile(dst, file string) error {
	r, gz, err := gzipper(file)
	if err != nil {
		return err
	}
	defer r.Close()
	rel, err := filepath.Rel(vargs.Source, file)
	if err != nil {
		return err
	}
	name := path.Join(vargs.Target, rel)
	w := bucket.Object(name).NewWriter(context.Background())
	w.CacheControl = vargs.CacheControl
	w.Metadata = vargs.Metadata
	for _, s := range vargs.ACL {
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
	// TODO implement exponential backoff
	return w.Close()
}

// gzipper returns a stream of file and a boolean indicating
// whether the stream is gzip-compressed.
//
// The stream is compressed if vargs.Gzip contains file extension.
func gzipper(file string) (io.ReadCloser, bool, error) {
	r, err := os.Open(file)
	if err != nil || !matchGzip(file) {
		return r, false, err
	}
	pr, pw := io.Pipe()
	w := gzip.NewWriter(pw)
	go func() {
		_, err := io.Copy(w, r)
		if err != nil {
			errorf("%s: io.Copy: %v", file, err)
		}
		if err := w.Close(); err != nil {
			errorf("%s: gzip: %v", file, err)
		}
		if err := pw.Close(); err != nil {
			errorf("%s: pipe: %v", file, err)
		}
		r.Close()
	}()
	return pr, true, nil
}

// matchGzip reports whether the file should be gzip-compressed during upload.
// Compressed files should be uploaded with "gzip" content-encoding.
func matchGzip(file string) bool {
	ext := filepath.Ext(file)
	if ext == "" {
		return false
	}
	ext = ext[1:]
	i := sort.SearchStrings(vargs.Gzip, ext)
	return i < len(vargs.Gzip) && vargs.Gzip[i] == ext
}

// walkFiles creates a complete set of files to upload
// by walking vargs.Source recursively.
//
// It excludes files matching vargs.Ignore pattern.
// The ignore pattern is matched using filepath.Match against a partial
// file name, relative to vargs.Source.
func walkFiles() ([]string, error) {
	var items []string
	err := filepath.Walk(vargs.Source, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		rel, err := filepath.Rel(vargs.Source, p)
		if err != nil {
			return err
		}
		var ignore bool
		if vargs.Ignore != "" {
			ignore, err = filepath.Match(vargs.Ignore, rel)
		}
		if err != nil || ignore {
			return err
		}
		items = append(items, p)
		return nil
	})
	return items, err
}

// run is the actual entry point called from main.
// It expects vargs and workspace to be initialized
func run(client *storage.Client) {
	// extract bucket name from the target path
	p := strings.SplitN(vargs.Target, "/", 2)
	bname := p[0]
	if len(p) == 1 {
		vargs.Target = ""
	} else {
		vargs.Target = p[1]
	}
	bucket = client.Bucket(strings.Trim(bname, "/"))

	// create a list of files to upload
	vargs.Source = filepath.Join(workspace.Path, vargs.Source)
	src, err := walkFiles()
	if err != nil {
		fatalf("local files: %v", err)
	}

	// upload all files in a goroutine, maxConcurrent at a time
	up := make(chan string, maxConcurrent)
	res := make(chan *result, len(src))
	go uploadAll(up, res)
	for _, f := range src {
		up <- f
	}

	// wait for all files to be uploaded or stop at first error
	for _ = range src {
		r := <-res
		if r.err != nil {
			fatalf("%s: %v", r.name, r.err)
		}
		printf(r.name)
	}
}

func main() {
	log.SetFlags(0)
	plugin.Param("workspace", &workspace)
	plugin.Param("vargs", &vargs)
	plugin.MustParse()
	sort.Strings(vargs.Gzip) // need for matchGzip

	auth, err := google.JWTConfigFromJSON([]byte(vargs.AuthKey), storage.ScopeFullControl)
	if err != nil {
		fatalf("auth: %v", err)
	}
	ctx := context.Background()
	client, err := storage.NewClient(ctx, cloud.WithTokenSource(auth.TokenSource(ctx)))
	if err != nil {
		fatalf("storage client: %v", err)
	}
	run(client)
	os.Exit(ecode)
}
