# Cell image scaffold

This directory contains the scaffold for the signed cell image TBound uses for
isolated command execution (`bash`). It is deliberately minimal and requires an
operator to pin, build, sign, and review before any governed use.

Files:

- `Containerfile` — image definition (replace the placeholder base digest).
- `entrypoint.sh` — deterministic in-image entrypoint with structured status.
- `seccomp.json` — strict default-deny seccomp allowlist (starting point).
- `landlock.md` — intended Landlock ruleset (not yet enforced/verified).

## Operator workflow

1. Replace the placeholder base digest in `Containerfile` with a reviewed pin.
2. Build the image with your OCI builder (Podman/Buildah).
3. Record the resulting image digest.
4. Sign it offline with cosign and publish only the public key:
   ```sh
   cosign sign --key cosign.key ghcr.io/<org>/tbound-cell@sha256:<digest>
   cosign public-key --key cosign.key > cell.pub
   ```
5. Verify offline (what TBound does):
   ```sh
   cosign verify --key cell.pub --output json ghcr.io/<org>/tbound-cell@sha256:<digest>
   ```
6. Configure TBound with the digest and public key; the verifier
   (`supervisor/internal/cellimage`) fails closed on any mismatch or missing key.

## Honest limits

- This scaffold is **not** a frozen G1 profile and does not establish
  containment. Seccomp/Landlock must be independently reviewed and enforced on
  the declared evaluation host.
- The private signing key must never be committed or placed in the guest.
