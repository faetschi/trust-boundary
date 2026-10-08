# tbound installer

`install.sh` installs the tbound CLI (`tbound`, `tbound-doctor`) and the runtime
bundle into a user-owned prefix, verifying the artifact checksum first. It never
uses `sudo` and never modifies your global Node or Pi.

```sh
# remote (default base URL is a placeholder until a release host exists)
curl -fsSL https://get.tbound.dev | sh

# local build (offline)
make build
install/install.sh --local-dist dist --no-path

# options
install/install.sh --version <ver> --prefix <dir> --base-url <url> --local-dist <dir> --no-path
install/install.sh uninstall [--prefix <dir>]
```

Artifacts are `tbound-<version>-<os>-<arch>.tar.gz` plus a `SHA256SUMS` file. The
installer refuses to extract an artifact whose SHA-256 is missing or does not
match.

See [`../docs/install.md`](../docs/install.md) for the full user guide and the
dev-vs-governed honesty boundary.
