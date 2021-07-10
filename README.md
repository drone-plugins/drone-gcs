# drone-gcs

[![Build Status](http://cloud.drone.io/api/badges/drone-plugins/drone-gcs/status.svg)](http://cloud.drone.io/drone-plugins/drone-gcs)
[![Gitter chat](https://badges.gitter.im/drone/drone.png)](https://gitter.im/drone/drone)
[![Join the discussion at https://discourse.drone.io](https://img.shields.io/badge/discourse-forum-orange.svg)](https://discourse.drone.io)
[![Drone questions at https://stackoverflow.com](https://img.shields.io/badge/drone-stackoverflow-orange.svg)](https://stackoverflow.com/questions/tagged/drone.io)
[![](https://images.microbadger.com/badges/image/plugins/gcs.svg)](https://microbadger.com/images/plugins/gcs "Get your own image badge on microbadger.com")
[![Go Doc](https://godoc.org/github.com/drone-plugins/drone-gcs?status.svg)](http://godoc.org/github.com/drone-plugins/drone-gcs)
[![Go Report](https://goreportcard.com/badge/github.com/drone-plugins/drone-gcs)](https://goreportcard.com/report/github.com/drone-plugins/drone-gcs)

Drone plugin to publish files and artifacts to Google Cloud Storage. For the usage information and a listing of the available options please take a look at [the docs](http://plugins.drone.io/drone-plugins/drone-gcs/).

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

#### Optional object folder setting

The object folder can be specified as part of the `PLUGIN_TARGET` or as part of the `PLUGIN_FOLDER` setting. The value
of the `PLUGIN_FOLDER` setting will be appended to any value supplied as part of the `PLUGIN_TARGET`.

```console
docker run --rm \
  -e PLUGIN_SOURCE="dist" \
  -e PLUGIN_TARGET="bucket/dir/" \
  -e PLUGIN_FOLDER="${DRONE_PULL_REQUEST}" \
  -e PLUGIN_IGNORE="bin/*" \
  -e PLUGIN_ACL="allUsers:READER,user@domain.com:OWNER" \
  -e PLUGIN_GZIP="js,css,html" \
  -e PLUGIN_CACHE_CONTROL="public,max-age=3600" \
  -e PLUGIN_METADATA='{"x-goog-meta-foo":"bar"}' \
  -v $(pwd):$(pwd) \
  -w $(pwd) \
  plugins/gcs
```

One potential use case for specifying the `PLUGIN_FOLDER` separately is when storing the value for `PLUGIN_TARGET` as a
drone secret while needing to generate a final object folder value during CI. The above example illustrates how it may
be possible to publish to `bucket/dir/DRONE_PULL_REQUEST`.

Perhaps a more illustrative example:

```yaml
kind: pipeline
name: upload prs

trigger:
  branch:
    include:
      - develop
  event:
    - pull_request

steps: 
  - name: upload to cloud storage prs bucket
    image: plugins/gcs
    settings:
      source: build
      target:
        from_secret: prs_bucket
      folder: "prs/${DRONE_PULL_REQUEST}"
      gzip: js,css,html
      token:
        from_secret: prs_token
```

The above drone configuration file is not meant to be complete.
