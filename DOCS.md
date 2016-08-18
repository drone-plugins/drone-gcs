Use this plugin to upload files and build artifacts
to the [Google Cloud Storage (GCS)](https://cloud.google.com/storage/) bucket.

You will need to a [Service Account](https://developers.google.com/console/help/new/#serviceaccounts)
to authenticate to the GCS.

The following parameters are used to configure this plugin:

* `auth_key` - service account auth key
* `source` - location of files to upload
* `target` - destination to copy files to, including bucket name
* `ignore` - skip files matching this [pattern](https://golang.org/pkg/path/filepath/#Match), relative to `source`
* `acl` - a list of access rules applied to the uploaded files, in a form of `entity:role`
* `gzip` - files with the specified extensions will be gzipped and uploaded with "gzip" Content-Encoding header
* `cache_control` - Cache-Control header
* `metadata` - an arbitrary dictionary with custom metadata applied to all objects

The following is a sample configuration in your .drone.yml file:

```yaml
publish:
  gcs:
    auth_key: >
      $SERVICE_ACCOUNT_KEY
    source: dist
    target: bucket/dir/
    ignore: bin/*
    acl:
      - allUsers:READER
    gzip:
      - js
      - css
      - html
    cache_control: public,max-age=3600
    metadata:
      x-goog-meta-foo: bar
```

`SERVICE_ACCOUNT_KEY` would be defined in repo or org secrets:

```sh
drone secret add --image=gcs \
    octocat/hello-world SERVICE_ACCOUNT_KEY <YOUR_GOOGLE_CREDENTIALS_JSON>
```

Then sign the YAML file after all secrets are added.

```bash
drone sign octocat/hello-world
```

See [secrets](http://readme.drone.io/0.5/usage/secrets/) for additional
information on secrets
