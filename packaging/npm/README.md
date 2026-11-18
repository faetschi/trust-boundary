# @tbound/cli

The `tbound` command for TBound, a Linux supervisor for the Pi coding agent.
TBound checks each Pi tool call against a fixed policy, runs `bash` in an isolated
container, and publishes changes to your repository only when the policy allows
them.

TBound is the artifact of a master's thesis, not a supported product.

## Install

```sh
npm install -g @tbound/cli
tbound --version
tbound doctor
```

The package supports Linux amd64 only and needs Node 22.19.0 or later. The binary
comes from the optional dependency `@tbound/cli-linux-x64`, so do not install with
`--omit=optional`. On macOS or Windows, use a Linux VM, or WSL 2 for building and
testing.

## Commands

The launcher forwards two subcommands to their own binaries:

```
tbound doctor ...    -> tbound-doctor   checks that Node and Pi are ready
tbound profile ...   -> tbound-profile  creates, signs and verifies the host profile
tbound <anything>    -> tbound
```

A governed `tbound serve --pi` also needs a signed host profile and the
Podman/crun containment stack. The
[setup guide](https://github.com/faetschi/trust-boundary/blob/main/docs/guides/setup.md)
covers both.

## Not in this package

The cell entrypoint, `tbound-cell-entrypoint`, is not on npm. It ships in the
Linux release tarball, and only the governed container runtime launches it.

## Links

- [Quickstart](https://github.com/faetschi/trust-boundary/blob/main/docs/guides/quickstart.md)
- [Release tarballs and checksums](https://github.com/faetschi/trust-boundary/releases)
- [Source](https://github.com/faetschi/trust-boundary)
