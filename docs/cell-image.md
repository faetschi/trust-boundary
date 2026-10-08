# Cell image and signing

TBound isolates `bash`/command execution in a signed cell image. The installer
refuses to run governed command execution unless the image verifies against a
digest and a cosign public key. It never accepts a caller boolean as proof.

## Verification path

`supervisor/internal/cellimage.Verify` runs `cosign verify --key <pub> --output
json <image@digest>` and requires the reported manifest digest to equal the
expected digest. It fails closed when:

- the image reference, expected digest, or public key is missing;
- the digest is not a valid `sha256:` value;
- cosign is absent or errors;
- no signature matches the expected digest.

## What the operator must do

1. Build the image from `packaging/cell/` with a pinned base digest.
2. Sign it offline and publish only `cell.pub` (never the private key).
3. Supply the image digest + public key to TBound's configuration.

## Scope

This establishes *image provenance*, not containment. The runtime isolation
profile (namespaces, cgroup-v2 delegation, seccomp, Landlock, `no_new_privs`,
pidfd settlement) must be independently frozen and verified on the declared
evaluation host. See `docs/tbound-readiness-status-2026-10-08.md` and
`packaging/cell/landlock.md`.
