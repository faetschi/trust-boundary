# tbound developer/release helper targets.
#
# The binaries live under supervisor/; release artifacts are built into dist/.
VERSION ?= dev
GO ?= go

.PHONY: help build release verify test fmt clean deb rpm container

help:
	@echo "tbound make targets:"
	@echo "  build     build host binaries + runtime bundle into dist/ (tarballs + SHA256SUMS)"
	@echo "  release   cross-build linux/darwin/windows (amd64,arm64) into dist/"
	@echo "  verify    offline end-to-end install verification (install into a throwaway prefix)"
	@echo "  deb       build a .deb from the linux-amd64 tarball (needs dpkg-deb)"
	@echo "  rpm       build an RPM from the linux-amd64 tarball (needs rpmbuild)"
	@echo "  container build the container image (needs podman or docker)"
	@echo "  test      run the Go unit tests for the doctor and Pi bootstrap"
	@echo "  fmt       gofmt the doctor and Pi bootstrap packages"
	@echo "  clean     remove dist/"

build:
	@scripts/build-release.sh $(VERSION) dist

release:
	@scripts/build-release.sh $(VERSION) dist linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

verify:
	@scripts/verify-install.sh $(VERSION)

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
