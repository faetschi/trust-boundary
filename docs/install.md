# Installing tbound

tbound is a host-side supervisor that runs the Pi coding agent as an untrusted
worker. It installs **next to** an existing Pi — it does not modify your global
Node or Pi, and its dev path needs no `sudo`.

> Honesty note: **governed `tbound serve --pi` is not yet shippable.** It requires
> a signed root-owned host profile and a rootless Podman/crun containment stack
> that do not exist as an installer yet (Phase 2). What installs today is the
> dev/runtime surface: the CLI, the `tbound-doctor` preflight, the runtime bundle,
> and side-by-side pinned Pi packages.

## Channels

| Channel | Command | Notes |
|---|---|---|
| npm/pnpm/bun (primary) | `npm i -g @tbound/cli` | Good next to a globally-installed Pi; ships the Go binary via per-platform optional deps |
| one-shot | `npx @tbound/cli doctor` | Try before installing |
| curl installer | `curl -fsSL https://get.tbound.dev \| sh` | Universal; checksum-verified |
| release tarball | download `tbound-<ver>-<os>-<arch>.tar.gz` + `SHA256SUMS` | Air-gapped/manual |
| build from source | `make build` then `install/install.sh --local-dist dist` | Requires Go and Node |
| container | *(planned)* | Matches the governed Podman story; Phase 2 |

## Prefix and config layout

Installation is user-owned and isolated:

```
${TBOUND_PREFIX:-${XDG_DATA_HOME:-$HOME/.local/share}/tbound}/
  bin/tbound             Go supervisor CLI
  bin/tbound-doctor      preflight/readiness checker
  runtime/               worker bundle (src/*.ts + package.json pins)
  runtime/node_modules   -> ../pi/node_modules (side-by-side Pi deps)
  pi/node_modules        pinned Pi packages
  node/                  optional private Node (if you choose to install one)
${XDG_CONFIG_HOME:-$HOME/.config}/tbound/config.json
```

## Install from source (works today)

```sh
make build                # builds dist/tbound-<ver>-<os>-<arch>.tar.gz + SHA256SUMS
TBOUND_LOCAL_DIST=dist install/install.sh --no-path
export PATH="$HOME/.local/share/tbound/bin:$PATH"
tbound-doctor             # check Node/Pi/containment readiness
tbound serve --pi --native-fixture   # Linux non-claim-bearing smoke test (exit 0)
tbound serve --pi                    # refused (exit 2) until a signed profile exists
```

Install the pinned Pi packages into the prefix (side-by-side, needs network):

```sh
tbound-doctor --install-pi   # installs @earendil-works/pi-* 0.87.1 + typebox 1.3.27
```

## Environment variables

| Variable | Meaning |
|---|---|
| `TBOUND_PREFIX` | Install/prefix root (default above) |
| `TBOUND_VERSION` | Version to install (default `latest`) |
| `TBOUND_BASE_URL` | Release base URL for `curl` installs |
| `TBOUND_LOCAL_DIST` | Use a local dist directory instead of downloading |

## Uninstall

```sh
install/install.sh uninstall            # removes the prefix
# or: rm -rf "$TBOUND_PREFIX"
```

## Package skeleton

`packaging/npm/` contains the npm distribution skeleton (root launcher +
per-platform optional dependencies). Publishing the five `@tbound/cli-<platform>`
packages is a release step.

## What is not here yet (Phase 2)

- `init`/`profile sign` tooling for `/etc/tbound/pi-host-profile.*`.
- Guided Podman/crun/Cosign setup and the frozen containment profile.
- A published release host for the `curl` installer and the platform npm packages.
