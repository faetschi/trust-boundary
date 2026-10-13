# Candidate clean Linux guest qualification

Preparation template only: this is not image registration, containment evidence,
or a G1 manifest. G1 remains false and H1 remains unexecuted. Keep the related
[candidate experiment manifest](../../docs/experiment-manifest.md) as the
existing cross-reference; this worksheet does not create registration evidence.

## Prepare and identify the candidate

Use a new dedicated amd64 Ubuntu 24.04 guest. Before bootstrap, authenticate
the selected base artifact against Ubuntu's signed SHA256SUMS. Select and record
the exact release serial and versioned publisher URL; do not record the mutable
`noble/release/` alias as the immutable input. Preserve the artifact filename
and format, signed checksum and signature files, signing-key fingerprint and
trust source, signature-verification result, and the artifact SHA-256 from the
authenticated checksum list. For an archive, also record the extracted disk
SHA-256 and its relation to that archive. A bootstrap's caller-supplied
`ExpectedImageSha256` check establishes equality to that value, not publisher
authenticity.

Verification instructions (run later in a trusted Ubuntu environment; keep the
selected artifact and checksum files in the same directory):

```sh
set -eu
BASE_URL='<exact versioned Ubuntu release URL selected for this build>'
ARTIFACT='<exact artifact filename supported by the chosen hypervisor>'
curl -fSLO "$BASE_URL/SHA256SUMS"
curl -fSLO "$BASE_URL/SHA256SUMS.gpg"
curl -fSLO "$BASE_URL/$ARTIFACT"
gpg --no-default-keyring \
  --keyring /usr/share/keyrings/ubuntu-cloudimage-keyring.gpg \
  --list-keys --with-fingerprint
gpg --no-default-keyring --status-fd 1 \
  --verify --keyring /usr/share/keyrings/ubuntu-cloudimage-keyring.gpg \
  SHA256SUMS.gpg SHA256SUMS
test "$(awk -v f="$ARTIFACT" \
  '$2 == f || $2 == "*" f {n++} END {print n+0}' SHA256SUMS)" -eq 1
sha256sum --check --ignore-missing SHA256SUMS
sha256sum "$ARTIFACT"
```

Record the actual signing fingerprint and verification output; do not fill them
from an example or assume the keyring is trusted without recording its trust
source. Consult Ubuntu's [artifact reference](https://ubuntu.com/docs/public-images/public-images-reference/artifacts/)
and [checksum verification guide](https://ubuntu.com/docs/public-images/public-images-how-to/verify-image-checksum/).
Azure VHD archives are intended for Azure and are not supported for local
Hyper-V by that reference. The existing CloudVhdx path is not a qualified local
guest recipe; choose supported installation media or validate a generic-image
conversion separately before using it. Follow the applicable bootstrap procedure. Record
hypervisor settings, filesystem/mount options and the final offline
disk/checkpoint-chain identity. Disable shared folders and clipboard
integration. The existing development VM and WSL are component-test
environments and cannot substitute for the registered clean guest.

Provision in a connected maintenance phase before freezing the baseline. Use
the existing bootstrap's pinned Go 1.27.1, Node 24.21.0 and Cosign 3.1.3,
retaining artifact digests and checksum/signature verification records. Install
Pi separately during maintenance using the [installation guide](../../docs/install.md):
```sh
export PATH="/opt/tbound/toolchains/node-v24.21.0-linux-x64/bin:$HOME/.local/bin:$PATH"
node --version
npm --version
command -v node npm
npm_config_prefix="$HOME/.local" npm install --global --ignore-scripts @earendil-works/pi-coding-agent@0.87.1
```
APT holds applied after installation prevent later upgrades; they do not pin
the initial dependency resolver. Record the package source configuration,
resolver inputs and full installed package manifest with the other guest
measurements. Reproducing the build requires frozen package sources and pinned
inputs. Repeating deployment from the exact qualified, frozen offline disk
reuses that measured image; it does not demonstrate a reproducible rebuild.

Create a non-root trial account with a subordinate-ID range of at least 65,536
IDs for each UID and GID mapping and start a fresh login session. Review the bootstrap's system-wide
user-namespace policy changes and include them in the recorded host profile.
Record the running kernel release, package version, kernel config digest and
live Landlock ABI. The registered D09 contract requires **exactly ABI 8**;
neither a kernel release name nor a capability probe's minimum threshold proves
this identity. A different ABI requires explicit protocol reconciliation.

As the trial account, capture identities without copying credentials:

```sh
set -eu
export PATH="/opt/tbound/toolchains/go1.27.1/bin:/opt/tbound/toolchains/node-v24.21.0-linux-x64/bin:$HOME/.local/bin:$PATH"
id
awk -F: -v user="$(id -un)" -v uid="$(id -u)" \
  '$1 == user || $1 == uid {print FILENAME ":" $0}' /etc/subuid /etc/subgid
uname -r
sha256sum "/boot/config-$(uname -r)"
dpkg-query -W -f='${binary:Package}\t${Version}\n'
apt-mark showhold
for tool in go node npm podman crun cosign pi; do
  path=$(command -v "$tool")
  resolved=$(readlink -f "$path")
  printf '%s: %s -> %s\n' "$tool" "$path" "$resolved"
  case "$tool" in go|cosign) "$path" version;; *) "$path" --version;; esac
  sha256sum "$resolved"
done
```

Also record the actual tool executable paths, versions and SHA-256 digests;
OCI image/signer; seccomp profile; cgroup root; entrypoint; source snapshot;
and signed runtime-profile identities. This worksheet supplies no fabricated
registration values. Retain the full package manifest with the source-media
checksum and offline disk identity.

## Offline preflight

Prepare module/npm caches first. Shut down, disconnect every virtual NIC at the
hypervisor, boot from the candidate baseline, and use the guest console as the
credential-free trial account. Set `TBOUND_TREE` to the identified source tree.
Do not pass provider variables, pull images or call a provider.

```sh
set -eu
test "$(id -u)" -ne 0
test -n "${TBOUND_TREE:?set the identified source tree}"
env -i HOME="$HOME" USER="$(id -un)" LOGNAME="$(id -un)" \
  PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
  /usr/bin/python3 -I "$TBOUND_TREE/infra/guest/verify-offline.py" \
  --host-adapter-disconnected
env -i PATH=/usr/bin:/bin /usr/bin/python3 - <<'PY'
import ctypes, sys
libc = ctypes.CDLL(None, use_errno=True)
libc.syscall.restype = ctypes.c_long
abi = libc.syscall(444, None, ctypes.c_size_t(0), ctypes.c_uint(1))
print(f'Landlock ABI: {abi}; errno: {ctypes.get_errno() if abi < 0 else 0}')
sys.exit(0 if abi == 8 else 1)
PY
env -i HOME="$HOME" USER="$(id -un)" LOGNAME="$(id -un)" \
  PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
  XDG_RUNTIME_DIR="/run/user/$(id -u)" GOTOOLCHAIN=local \
  bash "$TBOUND_TREE/infra/guest/probe-capabilities.sh"
env -i PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
  bash "$TBOUND_TREE/scripts/check-governed-host.sh"
```

Retain outputs and exit codes, including failures. The offline verifier checks
network disconnection; the capability probe does not enforce it. Confirm the
reported ABI is exactly 8. Require cgroup v2/domain, user-namespace creation,
rootless Podman/crun and live UID/GID mapping evidence. The probe intentionally
leaves `cgroup.kill-write` and `uidmap-helpers` UNVERIFIED. Exercise teardown
only in a disposable cgroup and retain independent observations. The host
checker remains advisory and may fail until a signed host profile exists.

Passing preflight does not establish G1. Registration and all required WP1
containment, audit, independent effect, recovery and durability checks remain
necessary before headline trials.
