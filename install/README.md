# tbound installer

`install.sh` installs the tbound CLI (`tbound`, `tbound-doctor`, `tbound-profile`)
and the runtime bundle into a user-owned prefix, verifying the artifact checksum first. It never
uses `sudo` and never modifies your global Node or Pi.

```sh
# remote (default base URL is a placeholder until a release host exists)
curl -fsSL https://get.tbound.dev | bash

# local build (offline)
make build VERSION=dev
bash install/install.sh --version dev --local-dist dist --no-path

# options
install/install.sh --version <ver> --prefix <dir> --base-url <url> --local-dist <dir> --no-path
install/install.sh uninstall [--prefix <dir>]
```

Artifacts are `tbound-<version>-<os>-<arch>.tar.gz` plus a `SHA256SUMS` file. The
installer refuses to extract an artifact whose SHA-256 is missing or does not
match. The checksum only detects corruption, not a replaced download. A run as
root therefore refuses it and requires `--public-key` (or the key its prefix
pins); see the TRUST notes in `install.sh --help`.

See [`../docs/guides/setup.md`](../docs/guides/setup.md) for the full user guide and the
dev-vs-governed honesty boundary.
