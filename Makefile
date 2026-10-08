# TBound top-level build helpers. The Go module lives under supervisor/.
SHELL := /bin/bash
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)

.PHONY: build test vet package clean doctor

build:
	cd supervisor && CGO_ENABLED=0 go build ./...

test:
	cd supervisor && go test ./...

vet:
	cd supervisor && go vet ./...

# Build the Linux release artefacts (binary, tarball, and deb/rpm when nfpm exists).
package:
	bash packaging/build-packages.sh

# Print the local host preflight report (Linux only; refuses elsewhere).
doctor: build
	cd supervisor && CGO_ENABLED=0 go build -o /tmp/tbound-dev ./cmd/tbound && /tmp/tbound-dev doctor || true

clean:
	rm -rf dist
