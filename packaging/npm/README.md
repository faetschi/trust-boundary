# `@tbound/cli` (npm distribution skeleton)

This package installs the `tbound` command next to your Pi coding agent via the
standard "root package + platform optional dependencies" pattern (like esbuild):

```
@tbound/cli                -> bin/tbound.js launcher + optionalDependencies
@tbound/cli-linux-x64      -> bin/tbound
@tbound/cli-linux-arm64    -> bin/tbound
@tbound/cli-darwin-x64     -> bin/tbound
@tbound/cli-darwin-arm64   -> bin/tbound
@tbound/cli-win32-x64      -> bin/tbound.exe
```

`bin/tbound.js` only locates the platform binary and `exec`s it; it does no work
of its own, so `npm i -g @tbound/cli` gives a `tbound` command without touching
your Node or Pi installs.

## Status

Skeleton only. To publish you must:

1. Build the per-platform binaries (`make release`).
2. Create one platform package per target, each shipping `bin/tbound[-doctor]`.
3. Publish the platform packages, then this root package.

Until those are published, `@tbound/cli` cannot resolve a binary and will print a
clear error. Build from source in the meantime (`docs/install.md`).
