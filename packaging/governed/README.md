# Governed D09 cell image and entrypoint

This directory builds the rootless Podman/crun cell image for the governed Pi
worker. It is the **only** image the D09 launcher accepts, and it is never pulled
at run time.

## Contents

- `Containerfile` — builds `cmd/tbound-cell-entrypoint` statically and installs
  it as the cell entrypoint (`/usr/local/bin/tbound-pi-entrypoint`) on top of a
  pinned Node base image with the pinned Pi runtime.
- The entrypoint source is `supervisor/cmd/tbound-cell-entrypoint/`.

## Build and freeze

```sh
# Build (no network is needed at run time; the build host may fetch base layers).
podman build -f packaging/governed/Containerfile -t localhost/tbound/pi-cell:latest .

# Sign and export a local OCI layout for offline verification.
cosign sign --key <admin-private-key> localhost/tbound/pi-cell:latest
podman save --format oci-archive localhost/tbound/pi-cell:latest -o pi-cell-oci/ || \
  skopeo copy containers-storage:localhost/tbound/pi-cell:latest oci:pi-cell-oci:latest
cosign sign --key <admin-private-key> oci:pi-cell-oci:latest   # sign the local layout

# Freeze the measured identities into the registered template.
tbound-profile template-init --out d09-cell.json
# Measure and offline-sign the exact managed writer root/service/mount policy.
# The registration signature is policy only; the CLI separately verifies that
# no run/command cell is active before it permits publication.
tbound-profile managed-root-measure --root /var/lib/tbound/live \
  --service-name tbound-writer --service-group tbound-writer \
  --service-unit /etc/systemd/system/tbound-writer.service \
  --mount-unit /etc/systemd/system/var-lib-tbound-live.mount \
  --runner /usr/local/bin/tbound \
  --service-cgroup /system.slice/tbound-writer.service \
  --out managed-live-root.json
tbound-profile managed-root-sign --registration managed-live-root.json \
  --key <offline-authorized-private-key> --out-registration managed-live-root.json \
  --out-sig managed-live-root.ed25519 --force
# Fill expected_image_digest, oci_layout_path, cosign_public_key_digest, seccomp_profile_digest,
# plus verify that in-image /usr/local/bin/node bytes exactly match the signed
# root-owned/read-only host Node executable opened on fd3,
# landlock_abi (exactly 8 for TSYNC), landlock_ruleset_digest, frozen_capabilities,
# entrypoint_digest, command_executable (/bin/bash) + command_executable_digest,
# cell_uid/cell_gid (the measured invoking UID/GID),
# namespace_profile=user,mount,pid,ipc,uts,network-none/v1,
# cgroup_version=v2/cgroup_controllers=cpu,memory,pids, pinned podman/crun/cosign
# paths+digests, cpus, pids_max, memory_mb, cgroups_path, and xattr filesystem
# magic/mount-options digest/scanner UID/GID/closed scanner privilege profile/
# derived profile digest plus the root-profile-measured-complete-enumeration/v1
# authority enum; set
# managed_live_root_registration_digest to the printed registration digest.
tbound-profile template-digest --template d09-cell.json
# Set the signed host profile's ContainmentProfileDigest to that digest.
```

Install the template root-owned at `/etc/tbound/d09-cell.json`. The public
verification key is the fixed, root-owned `/etc/tbound/pi-host-profile.pub`; the
CLI never chooses its own trust root.

## Child descriptor contract

The worker launcher passes the fixed role map (`piruntime.D09ChildRoleMap()`)
and binds the verified exposure and runtime source descriptors read-only at the
signed consumer paths inside the container. The entrypoint verifies that each
consumer mount is the same inode, replaces the inherited raw source descriptors
with consumer-mount descriptors, verifies the runtime bundle digest from the
consumer mount, and proves the mount IDs. Re-opening `/proc/self/fd` does not
relocate `f_path` and is not treated as a security boundary.

| fd | role |
|----|------|
| 3  | held, digest-verified Node binary (exec target) |
| 4  | sealed worker exposure consumer mount (read-only) |
| 5  | bootstrap pipe |
| 6  | proposal IPC socketpair |
| 7  | provider bridge socketpair |
| 8  | worker events pipe |
| 9  | runtime bundle consumer mount (read-only) |

No raw private-Git or host-admin descriptor is ever passed. The entrypoint emits
the worker TCB proof on fd10 and closes it before Node exec. Worker HOME/TMPDIR
are private mode-0700 directories on a bounded tmpfs, not in the sealed source.

For Bash, the production runner creates a fresh cgroup scope per lease/view,
binds the typed disposable `CommandView` descriptor read-write at `/work`, and
uses command-mode fd3 (view source), fd4 (TCB proof), fd5 (host start ack). It
checks the command binary against the frozen `/bin/bash` digest and returns no
result unless proof, real container exit, pidfd, cgroup-kill/populated=0,
container removal, output bound, and `/proc/*/mountinfo` detachment checks succeed.

## Honesty

Nothing here establishes containment or G1 by itself. A real provisioned build
and run is **UNVERIFIED** until an operator runs the opt-in integration test on a
prepared host; the image is non-claim-bearing until it is frozen and signed.
