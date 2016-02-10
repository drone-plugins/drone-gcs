FROM alpine

RUN apk add --update mailcap ca-certificates && rm -rf /var/cache/apk/*

ADD drone-google-cloudstorage /bin/
ENTRYPOINT ["/bin/drone-google-cloudstorage"]
