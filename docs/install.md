# Installing tbound

tbound is a host-side supervisor that runs the Pi coding agent as an untrusted
worker. It installs **next to** an existing Pi — it does not modify your global
Node or Pi, and its dev path needs no `sudo`.

> Honesty note: **governed `tbound serve --pi` is not yet shippable.** It requires
> a signed root-owned host profile, a rootless Podman/crun containment stack, and
> the runtime composition plus authoritative verifier that admits them — none of
> which exist yet (Phase 2). What installs today is the dev/runtime surface: the
> CLI, the `tbound-doctor` preflight, the runtime bundle, and side-by-side pinned
> Pi packages. Provisioning prerequisites alone does not enable governed launch.

## Channels

| Channel | Command | Notes |
|---|---|---|
| npm/pnpm/bun (primary) | `npm i -g @tbound/cli` | Good next to a globally-installed Pi; ships the Go binary via per-platform optional deps |
| one-shot | `npx @tbound/cli doctor` | Try before installing |
| curl installer | `curl -fsSL https://get.tbound.dev \| bash` | Universal; checksum-verified |
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
make build                # builds dist/tbound-dev-<os>-<arch>.tar.gz + SHA256SUMS (host target)
bash install/install.sh --version dev --local-dist dist --no-path
export PATH="$HOME/.local/share/tbound/bin:$PATH"
tbound-doctor             # check Node/Pi/containment readiness
tbound serve --pi --native-fixture   # Linux non-claim-bearing smoke test (exit 0)
tbound serve --pi                    # refused (exit 2) until a signed profile exists
```

The installer is a Bash script; run it with `bash`, not `sh`. Match `--version` to
the built artifact (the `make build` default is `dev`).

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

## Update

```sh
install/install.sh update               # reinstall the latest into the existing prefix
install/install.sh update --version 0.2.0
```

## Shell completions

The installer installs completions automatically (bash/zsh/fish) unless you pass
`--no-completions`. To install them manually:

```sh
# bash
cp install/completions/tbound.bash ~/.local/share/bash-completion/completions/tbound
# zsh (with compinit)
cp install/completions/_tbound "${fpath[1]}/_tbound"
# fish
cp install/completions/tbound.fish ~/.config/fish/completions/
```

## Packaging (templates)

- Homebrew: `packaging/homebrew/tbound.rb` (fill in url/sha256 after publishing).
- Debian: `scripts/build-deb.sh <ver>` (needs `dpkg-deb`) builds `tbound_<ver>_<arch>.deb`.
- RPM: `scripts/build-rpm.sh <ver>` (needs `rpmbuild`) builds an RPM from the linux tarball.
- Container: `packaging/container/Containerfile` + `scripts/build-container.sh`
  (podman, or docker) — a distribution convenience, not the governed D09 cell.
- Both `build-rpm.sh` and `build-container.sh` support `--check` to validate inputs
  without a build.

## Governed runs

Governed `tbound serve --pi` requires operator provisioning **and** the runtime
composition plus authoritative verifier (not yet implemented); provisioning alone
does not enable it. See [`doctor.md`](doctor.md) (advisory checks; `governed_ready`
is always false) and [`governed-setup.md`](governed-setup.md) (operator runbook).

## Package skeleton

`packaging/npm/` contains the npm distribution skeleton (root launcher +
per-platform optional dependencies). Publishing the five `@tbound/cli-<platform>`
packages is a release step.

## What is not here yet (Phase 2)

- `init`/`profile sign` tooling for `/etc/tbound/pi-host-profile.*`.
- Guided Podman/crun/Cosign setup and the frozen containment profile.
- A published release host for the `curl` installer and the platform npm packages.
