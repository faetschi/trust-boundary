# tbound container image

`Containerfile` builds a runnable tbound image (CLI + pinned Node/Pi runtime) with
a multi-stage build, so the Go build toolchain stays out of the final image.

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
host profile and containment described in [`../../docs/governed-setup.md`](../../docs/governed-setup.md).

## Notes

- The final image runs as the node image's non-root user space; `/var/lib/tbound`
  is the prefix (declared as a volume).
- `npm install --omit=dev` in the image installs the pinned Pi packages
  (`@earendil-works/pi-*` 0.87.1 + `typebox` 1.3.27) — the same pins as the host
  install, so the runtime matches.
- Build context is the repository root; do not add the full repo to the image
  beyond the copied `supervisor/` and `adapter/` subsets.
