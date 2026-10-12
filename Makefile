# tbound developer/release helper targets.
#
# The binaries live under supervisor/; release artifacts are built into dist/.
VERSION ?= dev
GO ?= go

UNAME_S := $(shell uname -s 2>/dev/null)
HOST_OS := $(if $(filter Linux,$(UNAME_S)),linux,$(if $(filter Darwin,$(UNAME_S)),darwin,windows))
HOST_ARCH := $(shell uname -m 2>/dev/null | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')

.PHONY: help build release verify test check-go check-adapter check-closure test-all fmt clean deb rpm container npm-packages publish

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
	@echo "  test         run the existing narrow doctor and Pi-bootstrap Go tests"
	@echo "  check-go     run Go tests across supervisor/ (provider/network tests opt-in)"
	@echo "  check-adapter run adapter typecheck, tests, and fixture tests using existing dependencies"
	@echo "  check-closure run the Pi closure probe (overwrites adapter/artifacts/pi-closure-report.json)"
	@echo "  test-all     run check-go and check-adapter; test skips may be platform-dependent"
	@echo "                 set RUN_PROVIDER_TESTS=1 only to opt in to provider/network tests"
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

# Real-provider tests stay disabled unless explicitly opted in.
RUN_PROVIDER_TESTS ?= 0

check-go: export TBOUND_RUN_REAL_PROVIDER_TESTS := $(RUN_PROVIDER_TESTS)
check-go:
	cd supervisor && $(GO) test -count=1 ./...

check-adapter:
	cd adapter && npm run typecheck && npm test && node --experimental-strip-types --test src/native-pi-host.test.ts src/native-pi-policy.test.ts src/sdk-worker.test.ts

check-closure:
	cd adapter && npm run closure

test-all: check-go check-adapter

fmt:
	cd supervisor && $(GO) fmt ./internal/piinstall ./cmd/tbound-doctor

clean:
	rm -rf dist
