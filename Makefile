BINARY    := gowave
CMD       := ./cmd/gowave
SRC_PATH  := $(shell pwd)
LDFLAGS   := -ldflags "-X main.gowaveSrcPath=$(SRC_PATH)"

.PHONY: build install test clean

## build: compile the gowave CLI into ./gowave
build:
	go build $(LDFLAGS) -o $(BINARY) $(CMD)

## install: install gowave to $GOPATH/bin with source path baked in
install:
	go install $(LDFLAGS) $(CMD)

## test: run all tests
test:
	go test ./...

## clean: remove build artifacts
clean:
	rm -f $(BINARY)
