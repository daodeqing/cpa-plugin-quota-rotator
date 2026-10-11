PLUGIN_ID ?= quota-rotator
VERSION ?= 1.0.0

.PHONY: all test build clean

all: test build

test:
	go test -v -race ./...

build:
	mkdir -p dist
	CGO_ENABLED=1 go build -trimpath -buildmode=c-shared \
		-ldflags "-s -w -X main.version=$(VERSION)" \
		-o dist/$(PLUGIN_ID).dll ./cmd/cpa-plugin-quota-rotator

clean:
	rm -rf dist
