package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"cloud.google.com/go/storage"
	"github.com/drone-plugins/drone-gcs/internal/gcp"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
	"golang.org/x/oauth2"
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
			Usage:  "destination to copy files to, including bucket name",
			EnvVar: "PLUGIN_TARGET",
		},
		cli.BoolFlag{
			Name:   "download",
			Usage:  "switch to download mode, which will fetch `source`'s files from GCS",
			EnvVar: "PLUGIN_DOWNLOAD",
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
		cli.StringFlag{
			Name:   "PLUGIN_POOL_ID",
			Usage:  "OIDC WORKLOAD POOL ID",
			EnvVar: "PLUGIN_POOL_ID",
		},
		cli.StringFlag{
			Name:   "PLUGIN_PROVIDER_ID",
			Usage:  "OIDC Provider Id",
			EnvVar: "PLUGIN_PROVIDER_ID",
		},
		cli.StringFlag{
			Name:   "PLUGIN_PROJECT_NUMBER",
			Usage:  "OIDC project Number ID",
			EnvVar: "PLUGIN_PROJECT_NUMBER",
		},
		cli.StringFlag{
			Name:   "PLUGIN_SERVICE_ACCOUNT_EMAIL",
			Usage:  "OIDC Service Account Email",
			EnvVar: "PLUGIN_SERVICE_ACCOUNT_EMAIL",
		},
		cli.StringFlag{
			Name:   "PLUGIN_OIDC_TOKEN_ID",
			Usage:  "OIDC GCP Token",
			EnvVar: "PLUGIN_OIDC_TOKEN_ID",
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}

func run(c *cli.Context) error {
	plugin := Plugin{
		Config: Config{
			Token:               c.String("token"),
			ACL:                 c.StringSlice("acl"),
			Source:              c.String("source"),
			Target:              c.String("target"),
			Download:            c.Bool("download"),
			Ignore:              c.String("ignore"),
			Gzip:                c.StringSlice("gzip"),
			CacheControl:        c.String("cache-control"),
			workloadPoolId:      c.String("PLUGIN_POOL_ID"),
			providerId:          c.String("PLUGIN_PROVIDER_ID"),
			gcpProjectId:        c.String("PLUGIN_PROJECT_NUMBER"),
			serviceAccountEmail: c.String("PLUGIN_SERVICE_ACCOUNT_EMAIL"),
			OidcIdToken:         c.String("PLUGIN_OIDC_TOKEN_ID"),
		},
	}

	if m := c.String("metadata"); m != "" {
		var metadata map[string]string

		if err := json.Unmarshal([]byte(m), &metadata); err != nil {
			return errors.Wrap(err, "error parsing metadata field")
		}

		plugin.Config.Metadata = metadata
	}

	if !plugin.Config.Download {
		if plugin.Config.Target == "" {
			return errors.New("Missing target")
		}
	}

	if plugin.Config.Source == "" {
		return errors.New("Missing source")
	}

	var client *storage.Client
	var err error
	if plugin.Config.workloadPoolId != "" && plugin.Config.gcpProjectId != "" && plugin.Config.providerId != "" && plugin.Config.OidcIdToken != "" && plugin.Config.serviceAccountEmail != "" {
		client, err = gcsClientWithOIDC(plugin.Config.workloadPoolId, plugin.Config.providerId, plugin.Config.gcpProjectId, plugin.Config.serviceAccountEmail, plugin.Config.OidcIdToken)
		if err != nil {
			return err
		}
	} else if plugin.Config.Token != "" {
		client, err = gcsClientWithToken(plugin.Config.Token)
		if err != nil {
			return err
		}
	} else if c.String("json-key") != "" {
		err := os.MkdirAll(os.TempDir(), 0600)
		if err != nil {
			return errors.Wrap(err, "failed to create temporary directory")
		}

		tmpfile, err := os.CreateTemp("", "")
		if err != nil {
			return errors.Wrap(err, "failed to create temporary file")
		}
		defer os.Remove(tmpfile.Name()) // clean up

		client, err = gcsClientWithJSONKey(c.String("json-key"), tmpfile)
		if err != nil {
			return err
		}
	} else {
		client, err = gcsClientApplicationDefaultCredentials()
		if err != nil {
			return err
		}
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

func gcsClientApplicationDefaultCredentials() (*storage.Client, error) {
	ctx := context.Background()
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize storage")
	}
	return client, nil
}

func gcsClientWithOIDC(workloadPoolId string, providerId string, gcpProjectId string, serviceAccountEmail string, OidcIdToken string) (*storage.Client, error) {

	oidcToken, err := gcp.GetFederalToken(OidcIdToken, gcpProjectId, workloadPoolId, providerId)
	if err != nil {
		return nil, fmt.Errorf("OIDC token retrieval failed: %w", err)
	}

	tokenSource := oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: oidcToken,
		TokenType:   "Bearer",
	})

	ctx := context.Background()
	client, err := storage.NewClient(ctx, option.WithTokenSource(tokenSource))
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize storage")
	}
	return client, nil
}
