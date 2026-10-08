# TBound packaging

Layout:

```
packaging/
  nfpm.yaml                 # deb/rpm packaging config
  build-packages.sh         # build static binary + tarball (+ deb/rpm via nfpm)
  install.sh                # tarball bootstrap installer
  uninstall.sh              # safe uninstaller
  systemd/
    tbound-user.service     # preferred: rootless user unit with Delegate=yes
    tbound.service          # system unit for a dedicated service account
  cell/                     # signed cell image scaffold (see cell/README.md)
```

## Build

```sh
bash packaging/build-packages.sh           # honours VERSION and OUTDIR
VERSION=1.2.3 bash packaging/build-packages.sh
```

Produces `dist/tbound-<version>-linux-amd64.tar.gz` + `SHA256SUMS`, and
`.deb`/`.rpm` when `nfpm` is installed. No network access is used.

## Install / uninstall

```sh
tar -xzf dist/tbound-<version>-linux-amd64.tar.gz
cd tbound-<version>
./install.sh            # installs CLI + user unit, runs tbound doctor
./uninstall.sh          # removes CLI + unit; keeps /opt/tbound unless --purge
```

## Operator-gated

Signing packages, provisioning the host profile, and building/signing the cell
image are **not** automated here and require explicit operator authorization.
See [../docs/packaging.md](../docs/packaging.md) and
[../docs/cell-image.md](../docs/cell-image.md).
