# drone-gcs

[![Build Status](http://beta.drone.io/api/badges/drone-plugins/drone-gcs/status.svg)](http://beta.drone.io/drone-plugins/drone-gcs)
[![Join the discussion at https://discourse.drone.io](https://img.shields.io/badge/discourse-forum-orange.svg)](https://discourse.drone.io)
[![Drone questions at https://stackoverflow.com](https://img.shields.io/badge/drone-stackoverflow-orange.svg)](https://stackoverflow.com/questions/tagged/drone.io)
[![Go Doc](https://godoc.org/github.com/drone-plugins/drone-gcs?status.svg)](http://godoc.org/github.com/drone-plugins/drone-gcs)
[![Go Report](https://goreportcard.com/badge/github.com/drone-plugins/drone-gcs)](https://goreportcard.com/report/github.com/drone-plugins/drone-gcs)
[![](https://images.microbadger.com/badges/image/plugins/gcs.svg)](https://microbadger.com/images/plugins/gcs "Get your own image badge on microbadger.com")

Drone plugin to publish files and artifacts to Google Cloud Storage. For the usage information and a listing of the available options please take a look at [the docs](DOCS.md).

## Build

Build the binary with the following commands:

```
go build
```

## Docker

Build the Docker image with the following commands:

```
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -a -tags netgo -o release/linux/amd64/drone-gcs
docker build --rm -t plugins/gcs .
```

### Usage

```
docker run --rm \
  -e PLUGIN_SOURCE="dist" \
  -e PLUGIN_TARGET="bucket/dir/" \
  -e PLUGIN_IGNORE="bin/*" \
  -e PLUGIN_ACL="allUsers:READER,user@domain.com:OWNER" \
  -e PLUGIN_GZIP="js,css,html" \
  -e PLUGIN_CACHE_CONTROL="public,max-age=3600" \
  -e PLUGIN_METADATA='{"x-goog-meta-foo":"bar"}' \
  -v $(pwd):$(pwd) \
  -w $(pwd) \
  plugins/gcs
```
