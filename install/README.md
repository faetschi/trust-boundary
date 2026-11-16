# tbound installer

`install.sh` installs the tbound CLI (`tbound`, `tbound-doctor`, `tbound-profile`)
and the runtime bundle into a user-owned prefix, verifying the artifact checksum first. It never
uses `sudo` and never modifies your global Node or Pi.

No prebuilt release is published yet, so build the archive from a Linux checkout
first (see the [quickstart](../docs/guides/quickstart.md)):

```sh
# local build (offline, recommended today)
make build VERSION=dev
bash install/install.sh --version dev --local-dist dist --no-path

# a published GitHub Release, once one exists (the default download base)
bash install/install.sh --version <ver>

# with publisher authentication (recommended for a root run)
bash install/install.sh --version <ver> --public-key <file> --key-fingerprint <hex16>

# options
install/install.sh --version <ver> --prefix <dir> --base-url <url> --local-dist <dir> --no-path
install/install.sh uninstall [--prefix <dir>]
```

The default download is verified only against `SHA256SUMS` from the same release.
That detects corruption, not a replaced download. Pass `--public-key` to verify
the publisher signature.

Artifacts are `tbound-<version>-<os>-<arch>.tar.gz` plus a `SHA256SUMS` file. The
installer refuses to extract an artifact whose SHA-256 is missing or does not
match. The checksum only detects corruption, not a replaced download. A run as
root therefore refuses it and requires `--public-key` (or the key its prefix
pins); see the TRUST notes in `install.sh --help`.

See [`../docs/guides/setup.md`](../docs/guides/setup.md) for the full user guide and the
dev-vs-governed honesty boundary.
