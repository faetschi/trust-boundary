# tbound installer

`install.sh` installs the tbound CLI (`tbound`, `tbound-doctor`, `tbound-profile`)
and the runtime bundle into a user-owned prefix, verifying the artifact checksum first. It never
uses `sudo` and never modifies your global Node or Pi. It supports Linux x86_64 only
and refuses every other platform before it downloads anything.

No prebuilt release is published yet, so build the archive from a Linux checkout
first (see the [quickstart](../docs/guides/quickstart.md)):

```sh
# local build (offline, recommended today)
make build VERSION=dev
bash install/install.sh --version dev --local-dist dist --no-path

# a published GitHub Release, once one exists (the default download base)
bash install/install.sh --version <ver>

# options
install/install.sh --version <ver> --prefix <dir> --base-url <url> --local-dist <dir> --no-path
install/install.sh uninstall [--prefix <dir>]
```

The default download is verified only against `SHA256SUMS` from the same release.
That detects corruption, not a replaced download.

`--public-key <file> --key-fingerprint <hex16>` verifies the publisher signature
instead. It needs `tbound-release-manifest.json` and its `.sig` next to the
artifacts. The GitHub Release workflow does not attach them yet, so
`--public-key` fails against the default download with `manifest download
failed`. Use it only with a `--local-dist` or `--base-url` origin that holds a
signed manifest (`tbound release manifest` and `tbound release sign`).

Artifacts are `tbound-<version>-linux-amd64.tar.gz` plus a `SHA256SUMS` file. The
installer refuses to extract an artifact whose SHA-256 is missing or does not
match. The checksum only detects corruption, not a replaced download. A run as
root therefore refuses it and requires `--public-key` (or the key its prefix
pins), so a root install from the default GitHub Release is not possible yet; see
the TRUST notes in `install.sh --help`.

See [`../docs/guides/setup.md`](../docs/guides/setup.md) for the full user guide and the
dev-vs-governed honesty boundary.
