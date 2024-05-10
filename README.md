# drone-gcs

[![Build Status](http://cloud.drone.io/api/badges/drone-plugins/drone-gcs/status.svg)](http://cloud.drone.io/drone-plugins/drone-gcs)
[![Gitter chat](https://badges.gitter.im/drone/drone.png)](https://gitter.im/drone/drone)
[![Join the discussion at https://discourse.drone.io](https://img.shields.io/badge/discourse-forum-orange.svg)](https://discourse.drone.io)
[![Drone questions at https://stackoverflow.com](https://img.shields.io/badge/drone-stackoverflow-orange.svg)](https://stackoverflow.com/questions/tagged/drone.io)
[![](https://images.microbadger.com/badges/image/plugins/gcs.svg)](https://microbadger.com/images/plugins/gcs "Get your own image badge on microbadger.com")
[![Go Doc](https://godoc.org/github.com/drone-plugins/drone-gcs?status.svg)](http://godoc.org/github.com/drone-plugins/drone-gcs)
[![Go Report](https://goreportcard.com/badge/github.com/drone-plugins/drone-gcs)](https://goreportcard.com/report/github.com/drone-plugins/drone-gcs)

Drone plugin to publish files and artifacts to Google Cloud Storage. For the usage information and a listing of the available options please take a look at [the docs](http://plugins.drone.io/drone-plugins/drone-gcs/).

Run the following script to install git-leaks support to this repo.
```
chmod +x ./git-hooks/install.sh
./git-hooks/install.sh
```

## Build

Build the binary with the following command:

```console
export GOOS=linux
export GOARCH=amd64
export CGO_ENABLED=0
export GO111MODULE=on

go build -v -a -tags netgo -o release/linux/amd64/drone-gcs
```

## Docker

Build the Docker image with the following command:

```console
docker build \
  --label org.label-schema.build-date=$(date -u +"%Y-%m-%dT%H:%M:%SZ") \
  --label org.label-schema.vcs-ref=$(git rev-parse --short HEAD) \
  --file docker/Dockerfile.linux.amd64 --tag plugins/gcs .
```

### Usage

* For upload
```console
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

* For download
```console
docker run --rm \
  -e PLUGIN_TOKEN="<YOUR_GCP_SERVICE_ACCOUNT_TOKEN>" \
  -e PLUGIN_SOURCE="bucket/dir/" \
  -e PLUGIN_DOWNLOAD="true" \
  -v $(pwd):$(pwd) \
  -w $(pwd) \
  plugins/gcs
```
