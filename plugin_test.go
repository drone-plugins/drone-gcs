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
	"io/ioutil"
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
	b, err := ioutil.ReadAll(r)
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
	if err := ioutil.WriteFile(p, b, 0644); err != nil {
		t.Fatalf("WriteFile(%q): %v", p, err)
	}
}

func TestUploadFile(t *testing.T) {
	wdir, err := ioutil.TempDir("", "drone-gcs-test")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, wdir, "file", []byte("test"))
	plugin.Config.Source = wdir

	tests := []struct {
		retries, failOnRetry int
		ok                   bool
	}{
		{0, 1, true},
		{3, 4, true},
		{2, 1, false},
	}
	for i, test := range tests {
		var n int

		rt := &fakeTransport{func(r *http.Request) (*http.Response, error) {
			_, mp, _ := mime.ParseMediaType(r.Header.Get("content-type"))
			mr := multipart.NewReader(r.Body, mp["boundary"])
			mr.NextPart() // skip metadata
			p, _ := mr.NextPart()
			b, _ := ioutil.ReadAll(p)
			// verify the body is always sent with correct content
			if v := string(b); v != "test" {
				t.Errorf("%d/%d: b = %q; want 'test'", i, n, b)
			}

			res := &http.Response{
				Body:       ioutil.NopCloser(strings.NewReader(`{"name": "fake"}`)),
				Proto:      "HTTP/1.0",
				ProtoMajor: 1,
				ProtoMinor: 0,
				StatusCode: http.StatusServiceUnavailable,
			}

			// The storage.Client does not retry on 404s
			// https://godoc.org/cloud.google.com/go/storage
			// https://cloud.google.com/storage/docs/exponential-backoff
			if n >= test.failOnRetry {
				res.StatusCode = http.StatusNotFound
			}

			if n >= test.retries {
				res.StatusCode = http.StatusOK
			}

			n++
			return res, nil
		}}
		hc := &http.Client{Transport: rt}
		client, _ := storage.NewClient(context.Background(), option.WithHTTPClient(hc))
		plugin.bucket = client.Bucket("bucket")

		err = plugin.uploadFile(filepath.Join(wdir, "file"))

		switch {
		case test.ok && err != nil:
			t.Errorf("%d: %v", i, err)
		case !test.ok && err == nil:
			t.Errorf("%d: wanted error", i)
		}
	}
}

func TestRun(t *testing.T) {
	wdir, err := ioutil.TempDir("", "drone-gcs-test")
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

	plugin.Config.Source = wdir + "/upload"
	plugin.Config.Ignore = "sub/*.bin"
	plugin.Config.Gzip = []string{"js"}
	plugin.Config.CacheControl = "public,max-age=10"
	plugin.Config.Metadata = map[string]string{"x-foo": "bar"}
	acls := []storage.ACLRule{storage.ACLRule{Entity: "allUsers", Role: "READER"}}
	plugin.Config.ACL = []string{fmt.Sprintf("%s:%s", acls[0].Entity, acls[0].Role)}

	type filesMapEntry struct {
		ctype string
		body  []byte
		gzip  bool
	}

	testCases := []struct {
		name         string
		files        map[string]*filesMapEntry
		pluginTarget string
		pluginFolder string
	}{
		{
			name: "target with bucket only",
			files: map[string]*filesMapEntry{
				"file.txt":     {"text/plain", []byte("text"), false},
				"file.js":      {"application/javascript", []byte("javascript"), true},
				"sub/file.css": {"text/css", []byte("sub style"), false},
			},
			pluginTarget: "bucket",
		},
		{
			name: "target with bucket and subdir",
			files: map[string]*filesMapEntry{
				"dir/file.txt":     {"text/plain", []byte("text"), false},
				"dir/file.js":      {"application/javascript", []byte("javascript"), true},
				"dir/sub/file.css": {"text/css", []byte("sub style"), false},
			},
			pluginTarget: "bucket/dir",
		},
		{
			name: "target with bucket and folder",
			files: map[string]*filesMapEntry{
				"folderpath/file.txt":     {"text/plain", []byte("text"), false},
				"folderpath/file.js":      {"application/javascript", []byte("javascript"), true},
				"folderpath/sub/file.css": {"text/css", []byte("sub style"), false},
			},
			pluginTarget: "bucket",
			pluginFolder: "folderpath",
		},
		{
			name: "target with bucket, subdir and folder",
			files: map[string]*filesMapEntry{
				"dir/folderpath/foldersub/file.txt":     {"text/plain", []byte("text"), false},
				"dir/folderpath/foldersub/file.js":      {"application/javascript", []byte("javascript"), true},
				"dir/folderpath/foldersub/sub/file.css": {"text/css", []byte("sub style"), false},
			},
			pluginTarget: "bucket/dir",
			pluginFolder: "folderpath/foldersub",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var seenMu sync.Mutex // guards seen
			seen := make(map[string]struct{}, len(testCase.files))

			rt := &fakeTransport{func(r *http.Request) (resp *http.Response, e error) {
				resp = &http.Response{
					Body:       ioutil.NopCloser(strings.NewReader(`{"name": "fake"}`)),
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
				obj := testCase.files[attrs.Name]
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
				b, _ := ioutil.ReadAll(p)
				if attrs.ContentEncoding == "gzip" {
					b = gunzip(t, b)
				}
				if bytes.Compare(b, obj.body) != 0 {
					t.Errorf("media b = %q; want %q", b, obj.body)
				}
				return
			}}

			hc := &http.Client{Transport: rt}
			client, err := storage.NewClient(context.Background(), option.WithHTTPClient(hc))
			if err != nil {
				t.Fatal(err)
			}

			plugin.Config.Target = testCase.pluginTarget
			plugin.Config.Folder = testCase.pluginFolder

			plugin.Exec(client)

			for k := range testCase.files {
				if _, ok := seen[k]; !ok {
					t.Errorf("%s didn't get uploaded", k)
				}
			}
		})
	}
}
