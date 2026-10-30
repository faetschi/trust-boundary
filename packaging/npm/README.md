# `@tbound/cli` (npm distribution skeleton)

This package installs the `tbound` command next to your Pi coding agent via the
standard "root package + platform optional dependencies" pattern (like esbuild):

```
@tbound/cli                -> bin/tbound.js launcher + optionalDependencies
@tbound/cli-linux-x64      -> bin/tbound, bin/tbound-doctor, bin/tbound-profile
@tbound/cli-darwin-x64     -> bin/tbound, bin/tbound-doctor, bin/tbound-profile
@tbound/cli-darwin-arm64   -> bin/tbound, bin/tbound-doctor, bin/tbound-profile
@tbound/cli-win32-x64      -> bin/tbound.exe, bin/tbound-doctor.exe, bin/tbound-profile.exe
```

`bin/tbound.js` only locates the platform binary and `exec`s it; it does no work
of its own, so `npm i -g @tbound/cli` gives a `tbound` command without touching
your Node or Pi installs.

## Subcommands

The root Go CLI has no `doctor`/`profile` subcommand; the launcher forwards two
subcommands to their dedicated binaries (the same pattern as `tbound doctor`):

```
tbound doctor ...    -> tbound-doctor
tbound profile ...   -> tbound-profile   # Linux-only; a clear stub elsewhere
tbound <anything>    -> tbound
```

`tbound-profile` generates/signs/verifies the Linux host profile. It is a
**Linux-only** tool: on macOS/Windows the forwarded binary is a stub that exits
with a clear "Linux-only" message. See [`../../docs/guides/setup.md`](../../docs/guides/setup.md)
and [`../../docs/guides/release.md`](../../docs/guides/release.md).

## Not shipped here

The linux/amd64 TCB entrypoint (`tbound-cell-entrypoint`) is **not** an npm
artifact. The release tarball/container carry its unsigned binary under
`libexec/tbound/` as an input to the operator's signed in-image D09 workflow; it
must never be launched by the npm/node launcher. `linux-arm64` is also absent
because the runtime does not build for linux/arm64 yet.

## Status

Skeleton only. To publish you must:

1. Build the per-platform binaries (`make release`).
2. Create one platform package per target, each shipping `bin/tbound[-doctor|-profile]`.
3. Publish the platform packages, then this root package.

Until those are published, `@tbound/cli` cannot resolve a binary and will print a
clear error. Build from source in the meantime (`docs/guides/setup.md`).
