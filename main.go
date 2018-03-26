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
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"cloud.google.com/go/storage"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"

	_ "github.com/joho/godotenv/autoload"
	"github.com/urfave/cli"
)

var (
	buildCommit string
	plugin      Plugin
)

func main() {
	fmt.Printf("Drone Google Cloud Storage Plugin built from %s\n", buildCommit)

	app := cli.NewApp()
	app.Name = "gcs artifact plugin"
	app.Usage = "gcs artifact plugin"
	app.Action = run
	app.Version = buildCommit
	app.Flags = []cli.Flag{

		cli.StringFlag{
			Name:   "auth-key",
			Usage:  "google auth key",
			EnvVar: "PLUGIN_AUTH_KEY, GOOGLE_CREDENTIALS",
		},
		cli.StringSliceFlag{
			Name:   "acl",
			Usage:  "a list of access rules applied to the uploaded files, in a form of entity:role",
			EnvVar: "PLUGIN_ACL",
		},
		cli.StringFlag{
			Name:   "source",
			Usage:  "location of files to upload",
			EnvVar: "PLUGIN_SOURCE",
		},
		cli.StringFlag{
			Name:   "ignore",
			Usage:  "skip files matching this pattern, relative to source",
			EnvVar: "PLUGIN_IGNORE",
		},
		cli.StringFlag{
			Name:   "target",
			Usage:  "destination to copy files to, including bucket name",
			EnvVar: "PLUGIN_TARGET",
		},
		cli.StringSliceFlag{
			Name:   "gzip",
			Usage:  `files with the specified extensions will be gzipped and uploaded with "gzip" Content-Encoding header`,
			EnvVar: "PLUGIN_GZIP",
		},
		cli.StringFlag{
			Name:   "cache-control",
			Usage:  "Cache-Control header",
			EnvVar: "PLUGIN_CACHE_CONTROL",
		},
		cli.StringFlag{
			Name:   "metadata",
			Usage:  "an arbitrary dictionary with custom metadata applied to all objects",
			EnvVar: "PLUGIN_METADATA",
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}

func run(c *cli.Context) error {

	var authKey, source, target string

	// Check for required fields
	if authKey = c.String("auth-key"); authKey == "" {
		return fmt.Errorf("Missing auth key")
	}

	if source = c.String("source"); source == "" {
		return fmt.Errorf("Missing source")
	}

	if target = c.String("target"); target == "" {
		return fmt.Errorf("Missing target")
	}

	plugin = Plugin{
		AuthKey:      authKey,
		ACL:          c.StringSlice("acl"),
		Source:       source,
		Target:       target,
		Ignore:       c.String("ignore"),
		Gzip:         c.StringSlice("gzip"),
		CacheControl: c.String("cache-control"),
	}

	m := c.String("metadata")

	if m != "" {
		var metadata map[string]string
		err := json.Unmarshal([]byte(c.String("metadata")), &metadata)
		if err != nil {
			log.Fatalf("metadata parsing error: %v", err)
		}
		plugin.Metadata = metadata
	}

	// Prepare Google Storage client
	auth, err := google.JWTConfigFromJSON([]byte(plugin.AuthKey), storage.ScopeFullControl)
	if err != nil {
		log.Fatalf("auth: %v", err)
	}
	ctx := context.Background()
	client, err := storage.NewClient(ctx, option.WithTokenSource(auth.TokenSource(ctx)))
	if err != nil {
		log.Fatalf("storage client: %v", err)
	}

	return plugin.Exec(client)
}
