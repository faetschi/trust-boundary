# tbound installer

`install.sh` installs `tbound`, `tbound-doctor`, `tbound-profile`, and the packaged
runtime assets into a user-owned prefix, verifying the artifact checksum first.
It never uses `sudo` and never modifies your global Node or Pi.

There is no public release host or published npm package available. Build and
install a local artifact from the repository root on Linux amd64:

Building needs Bash, `make`, Go 1.27.1 on `PATH`, and access to any Go modules not
already cached. Installation needs `tar`/gzip, `sha256sum` (or `shasum`), and
`realpath` (or `readlink -f`). Installing an already-built artifact does not need
Go or `make`. Node and Pi are separate prerequisites for the Pi integration;
the Go-only fixture does not need them.

```sh
make build VERSION=dev
bash install/install.sh --version dev --local-dist dist \
  --prefix "$HOME/.local/share/tbound" --no-path
export PATH="$HOME/.local/share/tbound/bin:$PATH"
umask 077
mkdir -m 700 -p "$HOME/.cache/tbound-tmp"
export TMPDIR="$HOME/.cache/tbound-tmp"
tbound serve --pi --native-fixture
```

Protected audit paths reject group- or other-writable ancestors, including
sticky `/tmp`. Use the private `TMPDIR` in native Linux, WSL2, and Linux VMs;
existing directories must already have suitable permissions. The fixture
requires no Node or Pi and makes no containment claim. `tbound-doctor` is an
optional advisory report for Pi and governed-host prerequisites.

Use the same explicit version for the build and installation. The installer
prints a suggested `PATH` command unless `--no-path` is supplied; it does not
edit your shell configuration. `--no-completions` skips shell completion setup.
Choose another `--prefix` to change the destination, and use that same prefix
for updates or uninstall:

```sh
bash install/install.sh update --version <ver> --local-dist <dist-dir> --prefix <dir>
bash install/install.sh uninstall --prefix <dir>
```

Artifacts are `tbound-<version>-<os>-<arch>.tar.gz` plus a `SHA256SUMS` file. The
installer refuses to extract an artifact whose SHA-256 is missing or does not
match.

See [the installation guide](../docs/install.md) for Pi installation, Linux and
Windows WSL2 or separate Linux VM setup, and the separate governed-runtime requirements. A local
installation does not provision or qualify a governed host.
