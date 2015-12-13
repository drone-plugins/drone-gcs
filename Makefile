BIN := drone-google-cloudstorage
IMG ?= plugins/$(BIN)

docker: $(BIN) Dockerfile
	docker build --rm -t $(IMG) .

$(BIN): $(wildcard *.go)
	GOOS=linux GOARCH=amd64 go build
