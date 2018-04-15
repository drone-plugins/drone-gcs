# GCS plugin

Use this plugin to upload files and build artifacts to the [Google Cloud Storage (GCS)](https://cloud.google.com/storage/) bucket.

You will need to a [Service Account](https://developers.google.com/console/help/new/#serviceaccounts) to authenticate to the GCS.

The following parameters are used to configure this plugin:

* `google_credentials` or `token` - (Required. Secret) GCP service account JSON auth key.
* `source` - (Required) location of files to upload
* `target` - (Required) destination to copy files to, including bucket name
* `ignore` - (Optional) skip files matching this [pattern](https://golang.org/pkg/path/filepath/#Match), relative to `source`
* `acl` - (Optional) list of access rules applied to the uploaded files, in a form of `entity:role`
* `gzip` - (Optional) files with the specified extensions will be gzipped and uploaded with "gzip" Content-Encoding header
* `cache_control` - (Optional) Cache-Control header value
* `metadata` - (Optional) arbitrary dictionary with custom metadata applied to all objects

The following is a sample configuration in your .drone.yml file:

```yaml
pipeline:
  gcs:
    image: plugins/gcs
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
    secrets:
      - source: google_auth_key
        target: GOOGLE_CREDENTIALS
```
