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

	"golang.org/x/net/context"
	"google.golang.org/cloud"
	"google.golang.org/cloud/storage"
)

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

	files := map[string]*struct {
		ctype string
		body  []byte
		gzip  bool
	}{
		"dir/file.txt":     {"text/plain", []byte("text"), false},
		"dir/file.js":      {"application/javascript", []byte("javascript"), true},
		"dir/sub/file.css": {"text/css", []byte("sub style"), false},
	}

	workspace.Path = wdir
	vargs.Source = "upload"
	vargs.Target = "bucket/dir/"
	vargs.Ignore = "sub/*.bin"
	vargs.Gzip = []string{"js"}
	vargs.CacheControl = "public,max-age=10"
	vargs.Metadata = map[string]string{"x-foo": "bar"}
	acls := []storage.ACLRule{storage.ACLRule{Entity: "allUsers", Role: "READER"}}
	vargs.ACL = []string{fmt.Sprintf("%s:%s", acls[0].Entity, acls[0].Role)}

	var seenMu sync.Mutex // guards seen
	seen := make(map[string]struct{}, len(files))

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
		obj := files[attrs.Name]
		if obj == nil {
			t.Errorf("unexpected obj: %+v", attrs)
			return
		}

		if attrs.Bucket != "bucket" {
			t.Errorf("attrs.Bucket = %q; want bucket", attrs.Bucket)
		}
		if attrs.CacheControl != vargs.CacheControl {
			t.Errorf("attrs.CacheControl = %q; want %q", attrs.CacheControl, vargs.CacheControl)
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
		if !reflect.DeepEqual(attrs.Metadata, vargs.Metadata) {
			t.Errorf("attrs.Metadata = %+v; want %+v", attrs.Metadata, vargs.Metadata)
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
	client, err := storage.NewClient(context.Background(), cloud.WithBaseHTTP(hc))
	if err != nil {
		t.Fatal(err)
	}
	run(client)
	for k := range files {
		if _, ok := seen[k]; !ok {
			t.Errorf("%s didn't get uploaded", k)
		}
	}
}
