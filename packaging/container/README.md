# tbound container image

`Containerfile` builds a runnable tbound image (CLI, Node runtime, and adapter
sources) with a multi-stage build, so the Go build toolchain stays out of the final image.

```sh
podman build -f packaging/container/Containerfile -t tbound:dev .
podman run --rm tbound:dev serve --pi --native-fixture   # non-claim-bearing smoke
podman run --rm tbound:dev --help
```

Or via the helper:

```sh
scripts/build-container.sh dev      # uses podman, falls back to docker
scripts/build-container.sh --check  # validate inputs without building
```

## Honesty boundary

This image is a **distribution convenience**, not the governed D09 cell. It does
not provide the namespaces/cgroup/Landlock/seccomp containment profile, and it
does not contain a signed host profile. Governed `serve --pi` still requires the
host profile and containment described in [`../../docs/guides/governed-setup.md`](../../docs/guides/governed-setup.md).

## Notes

- The image runs as the non-root `node` user shipped by the base image; the prefix
  `/var/lib/tbound` is owned by that user and declared as a volume.
- `npm install --omit=dev` installs only TBound's production Node dependencies;
  it does not install Pi development dependencies. Production packaging of the
  official installed Pi CLI and verification of its executable/extension closure
  are pending. No private embedded Pi SDK is packaged.
- The build context is narrowed by `.dockerignore` (podman and buildah read it too).
- Base images are currently **not digest-pinned**; pin `golang:1.27-*` and
  `node:24-*` digests before claiming a pinned runtime.
- Build context is the repository root; do not add the full repo to the image
  beyond the copied `supervisor/` and `adapter/` subsets.
