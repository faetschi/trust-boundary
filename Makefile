# tbound developer/release helper targets.
#
# The binaries live under supervisor/; release artifacts are built into dist/.
VERSION ?= dev
GO ?= go

UNAME_S := $(shell uname -s 2>/dev/null)
HOST_OS := $(if $(filter Linux,$(UNAME_S)),linux,$(if $(filter Darwin,$(UNAME_S)),darwin,windows))
HOST_ARCH := $(shell uname -m 2>/dev/null | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')

.PHONY: help build release verify test fmt clean deb rpm container npm-packages publish

help:
	@echo "tbound make targets:"
	@echo "  build        build host-target binaries + runtime bundle into dist/ (tarballs + SHA256SUMS)"
	@echo "  release      cross-build supported targets into dist/ (linux/amd64, darwin/amd64, darwin/arm64, windows/amd64)"
	@echo "  verify       offline end-to-end install verification (install into a throwaway prefix)"
	@echo "  npm-packages generate npm packages from dist/ into dist/npm"
	@echo "  publish      publish a GitHub release (needs gh; VERSION=0.1.0)"
	@echo "  deb          build a .deb from the linux-amd64 tarball (needs dpkg-deb)"
	@echo "  rpm          build an RPM from the linux-amd64 tarball (needs rpmbuild)"
	@echo "  container    build the container image (needs podman or docker)"
	@echo "  test         run the Go unit tests for the doctor and Pi bootstrap"
	@echo "  fmt          gofmt the doctor and Pi bootstrap packages"
	@echo "  clean        remove dist/"

build:
	@scripts/build-release.sh $(VERSION) dist $(HOST_OS)/$(HOST_ARCH)

release:
	@scripts/build-release.sh $(VERSION) dist linux/amd64 darwin/amd64 darwin/arm64 windows/amd64

verify:
	@scripts/verify-install.sh $(VERSION)

npm-packages:
	@scripts/make-npm-packages.sh $(VERSION) dist

publish:
	@scripts/publish-release.sh $(VERSION)

deb:
	@scripts/build-deb.sh $(VERSION) dist

rpm:
	@scripts/build-rpm.sh $(VERSION) dist

container:
	@scripts/build-container.sh $(VERSION)

test:
	cd supervisor && $(GO) test ./internal/piinstall ./cmd/tbound-doctor

fmt:
	cd supervisor && $(GO) fmt ./internal/piinstall ./cmd/tbound-doctor

clean:
	rm -rf dist
