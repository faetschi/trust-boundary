# tbound developer/release helper targets.
#
# The binaries live under supervisor/; release artifacts are built into dist/.
VERSION ?= dev
GO ?= go

.PHONY: help build release verify test check-node boundary extension-webchat extension-webchat-frontend-verify extension-webchat-csp fmt clean deb rpm container npm-packages publish

help:
	@echo "tbound make targets:"
	@echo "  build        build the linux/amd64 release tarball + SHA256SUMS into dist/"
	@echo "  release      same as build (releases are linux/amd64 only)"
	@echo "  verify       offline end-to-end install verification (install into a throwaway prefix)"
	@echo "  npm-packages generate npm packages from dist/ into dist/npm"
	@echo "  publish      publish a GitHub release (needs gh; VERSION=0.1.0)"
	@echo "  deb          build a .deb from the linux-amd64 tarball (needs dpkg-deb)"
	@echo "  rpm          build an RPM from the linux-amd64 tarball (needs rpmbuild)"
	@echo "  container    build the container image (needs podman or docker)"
	@echo "  test         run the extension boundary guard, then the full supervisor Go suite (needs Node on PATH)"
	@echo "  boundary     check that core never imports the optional extensions/webchat (pure sh)"
	@echo "  extension-webchat  gofmt, vet, race tests and Node tests for the optional web chat extension (needs Node)"
	@echo "  extension-webchat-frontend-verify  npm ci --ignore-scripts, frontend verify, and dist reproducibility (needs Node; not a core gate)"
	@echo "  extension-webchat-csp  opt-in headless-browser CSP smoke; CSP_URL=http://127.0.0.1:PORT targets a running demo gateway (needs Chrome; not a core gate)"
	@echo "  fmt          gofmt all Go packages"
	@echo "  clean        remove dist/"

build:
	@scripts/build-release.sh $(VERSION) dist linux/amd64

release: build

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

# Several supervisor tests (internal/taskaccept, internal/corpusadmit,
# cmd/tbound-profile and others) run node and fail, not skip, without it.
check-node:
	@command -v node >/dev/null 2>&1 || { echo "make test needs Node.js on PATH (supervisor tests run node); see docs/guides/setup.md. 'make boundary' needs no Node." >&2; exit 1; }

test: check-node boundary
	cd supervisor && $(GO) test ./...

# boundary stays pure sh and grep: it needs neither Node nor the optional
# extension, so it can run on a host without Node. Only the full test target
# above requires Node.
boundary:
	@sh scripts/check-extension-boundary.sh
	@sh scripts/check-extension-boundary.sh --self-test

# Optional extension only (extensions/webchat). Not a prerequisite of any core
# target. The browser-side tests live in the React frontend (the next target).
extension-webchat:
	cd extensions/webchat && gofmt -l . | (! grep .) && $(GO) vet -mod=readonly ./... && $(GO) test -race -mod=readonly -count=1 ./...

# React frontend of the optional extension (extensions/webchat/frontend). Node is
# needed only to change the UI; the Go gateway embeds the committed build. Installs
# ignore scripts. repro runs before verify, so it compares two clean rebuilds with
# the dist/ committed at HEAD (the pre-commit equivalent of git diff --exit-code).
extension-webchat-frontend-verify:
	cd extensions/webchat/frontend && npm ci --ignore-scripts && npm run repro && npm run verify

# Opt-in headless-browser CSP smoke. Not part of extension-webchat or any core
# target. With CSP_URL it checks a demo gateway already running on a fresh
# 127.0.0.1 port (never 8791); without it, the script builds and starts one.
extension-webchat-csp:
	cd extensions/webchat && node scripts/csp-smoke.mjs $(if $(CSP_URL),--url $(CSP_URL))

fmt:
	cd supervisor && $(GO) fmt ./...

clean:
	rm -rf dist
