FROM alpine

RUN apk add --update ca-certificates && rm -rf /var/cache/apk/*

ADD drone-google-cloudstorage /bin/
ENTRYPOINT ["/bin/drone-google-cloudstorage"]
