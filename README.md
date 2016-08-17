# drone-google-cloudstorage

[![Build Status](http://beta.drone.io/api/badges/drone-plugins/drone-google-cloudstorage/status.svg)](http://beta.drone.io/drone-plugins/drone-google-cloudstorage)
[![Coverage Status](https://aircover.co/badges/drone-plugins/drone-google-cloudstorage/coverage.svg)](https://aircover.co/drone-plugins/drone-google-cloudstorage)
[![](https://badge.imagelayers.io/plugins/drone-gcs:latest.svg)](https://imagelayers.io/?images=plugins/drone-gcs:latest 'Get your own badge on imagelayers.io')

Drone plugin to publish files and artifacts to Google Cloud Storage. For the usage information and a listing of the available options please take a look at [the docs](DOCS.md).

## Binary

Build the binary using `make`:

```
make deps build
```

### Usage

```sh
drone-google-cloudstorage               \
  --auth-key <auth_key>                 \
  --source "bin/"                       \
  --target "bucket/path/"               \
  --ignore "*.tmp"                      \
  --acl    "allUsers:READER"            \
  --acl    "user@domain.com:OWNER"      \
  --gzip   "js"                         \
  --gzip   "css"                       \
  --cache-control "public,max-age=3600" \
  --metadata '{"x-goog-meta-foo":"bar"}'
```

## Docker

Build the container using `make`:

```
make deps docker
```

### Usage

```sh
docker run --rm -i \
  -e PLUGIN_SOURCE="dist" \
  -e PLUGIN_TARGET="bucket/dir/" \
  -e PLUGIN_IGNORE="bin/*" \
  -e PLUGIN_ACL="allUsers:READER,user@domain.com:OWNER" \
  -e PLUGIN_GZIP= "js,css,html" \
  -e PLUGIN_CACHE_CONTROL="public,max-age=3600" \
  -e PLUGIN_METADATA='{"x-goog-meta-foo":"bar"}' \
  -v $(pwd):$(pwd) \
  -w $(pwd) \
  plugins/gcs

```