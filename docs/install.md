# Installing tbound

tbound is a host-side supervisor that runs the Pi coding agent as an untrusted
worker. It installs **next to** an existing Pi — it does not modify your global
Node or Pi, and its dev path needs no `sudo`.

> **Current launch status:** Linux `tbound serve --pi` is a client for the
> registered systemd socket-activated service. The client/server handoff,
> authentication, and journaling code exist, but the service is not provisioned
> here; service-mode source capture and the real signed-profile path remain
> unqualified. No positive governed Pi run or G1 result is claimed. The
> --native-fixture route is a Go-only smoke test; --dev-fixture runs a real Pi
> SDK worker with offline scripted behavior. See [launch.md](launch.md) for
> current commands and limits. Install artifacts alone do not enable a governed
> run.

## Channels

| Channel | Command | Notes |
|---|---|---|
| npm/pnpm/bun (planned) | `npm i -g @tbound/cli` | Package layout ready; **not yet published**. Ships `tbound`, `tbound-doctor`, `tbound-profile` via per-platform optional deps |
| one-shot (planned) | `npx @tbound/cli doctor` | **Not yet published** |
| curl installer | unavailable | No release host or installer URL is published |
| release tarball (works today) | download `tbound-<ver>-<os>-<arch>.tar.gz` + `SHA256SUMS` | Air-gapped/manual |
| build from source (works today) | `make build` then `install/install.sh --local-dist dist` | Requires Go and Node |
| container | `packaging/container/Containerfile` | Distribution convenience; **not** the governed D09 cell |

## Prefix and config layout

Installation is user-owned and isolated:

```
${TBOUND_PREFIX:-${XDG_DATA_HOME:-$HOME/.local/share}/tbound}/
  bin/tbound             Go supervisor CLI
  bin/tbound-doctor      preflight/readiness checker
  bin/tbound-profile     host-profile generator/signer/verifier (Linux-only)
  runtime/               worker bundle (src/*.ts + package.json pins)
  runtime/node_modules   -> ../pi/node_modules (side-by-side Pi deps)
  pi/node_modules        pinned Pi packages
  node/                  optional private Node (if you choose to install one)
  libexec/tbound/tbound-cell-entrypoint   linux/amd64 TCB artifact (see below)
${XDG_CONFIG_HOME:-$HOME/.config}/tbound/config.json
```

The `tbound-cell-entrypoint` under `libexec/tbound/` is an unsigned binary
intended for a signed, frozen in-image D09 TCB entrypoint. It is shipped
**linux/amd64 only**, never invoked by the npm/node launcher or
the `tbound` CLI, and must be included in the reviewed image and admitted profile
before it can be used for governed work. A copy in a user-owned prefix is a
distribution convenience, **not** a trusted production artifact; signing and
protected installation remain an explicit operator workflow. The build does not
generate keys or sign the binary/image.

## Install from source (works today)

```sh
make build                # builds dist/tbound-dev-<os>-<arch>.tar.gz + SHA256SUMS (host target)
bash install/install.sh --version dev --local-dist dist --no-path
export PATH="$HOME/.local/share/tbound/bin:$PATH"
tbound-doctor             # check Node/Pi/containment readiness
tbound-profile --help     # Linux host-profile tool (stub outside Linux)
tbound serve --pi --native-fixture   # Linux non-claim-bearing smoke test (exit 0)
tbound serve --pi                    # managed-service client; unavailable until service is provisioned and qualified
```

The installer is a Bash script; run it with `bash`, not `sh`. Match `--version` to
the built artifact (the `make build` default is `dev`).

Install the pinned Pi packages into the prefix (side-by-side, needs network):

```sh
tbound-doctor --install-pi   # installs @earendil-works/pi-* 0.87.1 + typebox 1.3.27
```

## The `tbound-profile` host-profile tool (Linux)

`tbound-profile` generates, signs, and verifies the Linux host profile the
governed launcher admits. It is Linux-only: on macOS/Windows the same command is
a stub that exits with a clear "Linux-only" message.

```sh
tbound-profile init            --out profile.json
tbound-profile template-init   --out d09-cell.json
tbound-profile template-digest --template d09-cell.json
tbound-profile sign            --profile profile.json --key ed25519.key \
                               --out-sig profile.ed25519 --out-pub profile.pub
tbound-profile verify          --profile profile.json --sig profile.ed25519 --pub profile.pub
```

It never prints or copies private key material, and it never generates a trust
anchor automatically: the operator supplies the Ed25519 key. Production admission
uses the fixed root-owned public key at `/etc/tbound/pi-host-profile.pub`.

Under npm the same tool is reached through the launcher (the root Go CLI has no
`profile` subcommand):

```sh
tbound profile verify --profile profile.json --sig profile.ed25519 --pub profile.pub
```

## Environment variables

| Variable | Meaning |
|---|---|
| `TBOUND_PREFIX` | Install/prefix root (default above) |
| `TBOUND_VERSION` | Version to install (default `latest`) |
| `TBOUND_BASE_URL` | Release base URL for `curl` installs (no host published yet) |
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

The ordinary Linux CLI is a managed-service client. The internal `--service`
server entry point is intended for systemd socket activation. Service provisioning,
Linux client/server and restart testing, live source qualification, and the real
signed-profile path remain unverified; the current host returns `service unavailable`
when that service is absent. A local fixture success is not a governed run. See
[launch.md](launch.md) and [governed-setup.md](governed-setup.md).

## Package skeleton

`packaging/npm/` contains the npm distribution skeleton (root launcher +
per-platform optional dependencies). Publishing the `@tbound/cli-<platform>`
packages is a release step. `linux-arm64` is not advertised because the runtime
does not build for it yet.

## What is not here yet (Phase 2)

- Guided Podman/crun/Cosign setup and the frozen containment profile.
- A published release host for the `curl` installer and the platform npm packages.
- Provisioned and tested systemd service operation and qualification of the signed
  profile/source path; a positive governed launch and G1 remain unverified.
