package main

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"log"
	"os"

	"cloud.google.com/go/storage"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
)

var (
	version = "unknown"
)

func main() {
	app := cli.NewApp()
	app.Name = "gcs plugin"
	app.Usage = "gcs plugin"
	app.Action = run
	app.Version = version
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:   "token",
			Usage:  "google auth key",
			EnvVar: "PLUGIN_TOKEN,GOOGLE_CREDENTIALS,TOKEN",
		},
		cli.StringFlag{
			Name:   "json-key",
			Usage:  "google json keys",
			EnvVar: "PLUGIN_JSON_KEY",
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
			Usage:  "destination bucket to copy files to, can include folder path",
			EnvVar: "PLUGIN_TARGET",
		},
		cli.StringFlag{
			Name:   "folder",
			Usage:  "destination folder path to copy files to, will be appended to target",
			EnvVar: "PLUGIN_FOLDER",
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
	plugin := Plugin{
		Config: Config{
			Token:        c.String("token"),
			ACL:          c.StringSlice("acl"),
			Source:       c.String("source"),
			Target:       c.String("target"),
			Folder:       c.String("folder"),
			Ignore:       c.String("ignore"),
			Gzip:         c.StringSlice("gzip"),
			CacheControl: c.String("cache-control"),
		},
	}

	if m := c.String("metadata"); m != "" {
		var metadata map[string]string

		if err := json.Unmarshal([]byte(m), &metadata); err != nil {
			return errors.Wrap(err, "error parsing metadata field")
		}

		plugin.Config.Metadata = metadata
	}

	if plugin.Config.Source == "" {
		return errors.New("Missing source")
	}

	if plugin.Config.Target == "" {
		return errors.New("Missing target")
	}

	var client *storage.Client
	var err error
	if plugin.Config.Token != "" {
		client, err = gcsClientWithToken(plugin.Config.Token)
		if err != nil {
			return err
		}
	} else if c.String("json-key") != "" {
		err := os.MkdirAll(os.TempDir(), 0600)
		if err != nil {
			return errors.Wrap(err, "failed to create temporary directory")
		}

		tmpfile, err := ioutil.TempFile("", "")
		if err != nil {
			return errors.Wrap(err, "failed to create temporary file")
		}
		defer os.Remove(tmpfile.Name()) // clean up

		client, err = gcsClientWithJSONKey(c.String("json-key"), tmpfile)
		if err != nil {
			return err
		}
	} else {
		return errors.New("Either one of token or json key must be specified")
	}

	return plugin.Exec(client)
}

func gcsClientWithToken(token string) (*storage.Client, error) {
	auth, err := google.JWTConfigFromJSON([]byte(token), storage.ScopeFullControl)
	if err != nil {
		return nil, errors.Wrap(err, "failed to authenticate token")
	}

	ctx := context.Background()
	client, err := storage.NewClient(ctx, option.WithTokenSource(auth.TokenSource(ctx)))
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize storage")
	}
	return client, nil
}
func gcsClientWithJSONKey(jsonKey string, credFile *os.File) (*storage.Client, error) {
	if _, err := credFile.Write([]byte(jsonKey)); err != nil {
		return nil, errors.Wrap(err, "failed to write gcs credentials to file")
	}
	if err := credFile.Close(); err != nil {
		return nil, errors.Wrap(err, "failed to close gcs credentials file")
	}

	ctx := context.Background()
	client, err := storage.NewClient(ctx, option.WithCredentialsFile(credFile.Name()))
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize storage")
	}
	return client, nil
}
