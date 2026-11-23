# Self-contained workspace

Everything TBound and the thesis need lives in this repository directory, and nothing goes outside it. A clone plus `.local/` moves the project to another machine.

| Path | In git | Contents |
|---|---|---|
| `infra/freeze/` | yes | Freeze kit: gate, freeze, guest chain. |
| `evidence/` | yes | F14 receipts and the few files the thesis cites. |
| `.local/` | no | VM disks, the dedicated WSL distro's disk, seed disk, owner keys, `freeze-host.env`, worktrees, local history archive. |
| `thesis/` | no | Checkout of the thesis repository. |

`scripts/check-self-contained.sh` fails in CI when a tracked file depends on a path outside the repo.

**No hardcoded machine paths.** A live file (anything outside `evidence/` and the frozen `infra/freeze/freezes/`) names no drive-letter path, user profile, the owner's login, `.t3` worktree or Claude scratch folder. Use `$TBOUND_LOCAL`, `.local/`, a path relative to the repo root, an environment variable, or a placeholder such as `/home/<user>`. The same script enforces this.

**Worktrees.** Create them in `.local/worktrees/<branch-slug>`. A worktree has no `.local/` of its own: set `TBOUND_LOCAL` to the primary checkout's.

**Linux scratch.** Builds check file modes, which Windows drive mounts don't keep. On Windows, name the distro in every command (`wsl.exe -d tbound-dev`), because the machine's default distro may have its disk elsewhere. `TBOUND_WORK` points into that distro's disk, and `infra/freeze/tools/pull-receipts.sh` copies results into `evidence/`. Drive mounts must be read-only from WSL.

## Move to another machine

1. Clone this repo, and the thesis into `thesis/`.
2. Copy `.local/` on an encrypted drive. Check it from `.local/assets` with `sha256sum -c SHA256SUMS`.
3. To continue the existing chain, restore `.local/secrets/freeze-home.tar.gz` with `tar -xzpf` on a Linux filesystem, at a short path such as `~/tbound-h`. The restore is untested. To start a new chain instead, follow [Start a new chain](../../infra/freeze/README.md#start-a-new-chain).
4. Copy `infra/freeze/host.env.example` to `.local/freeze-host.env` and adjust it.
5. Prepare the host:
   - **Native Linux.** `bash infra/repro/bootstrap-qemu-linux.sh` creates a VM under `.local/vm`, with its image pinned by hash. To use the host itself instead, `sudo bash infra/repro/bootstrap-linux.sh <trial-user>`.
   - **Windows.** `infra\repro\bootstrap-hyperv-windows.ps1` creates the VM under `.local\vm`. `infra\workspace\New-TBoundWsl.ps1 -RootfsPath <rootfs> -RootfsSha256 <hash>` creates the WSL distro `tbound-dev` under `.local\wsl`, with read-only drive mounts for builds.
   - **Inputs.** In a Linux shell, `bash infra/workspace/fetch-inputs.sh "$TBOUND_WORK/inputs"` downloads and verifies the public build inputs. Add `--iso` for the Ubuntu ISO. `bash infra/workspace/install-toolchains.sh "$TBOUND_WORK/inputs" <prefix>` unpacks Go and Node and prints the environment to source, then `bash infra/workspace/fill-caches.sh` fills the Go and npm caches for the offline builds. `make build` and `make test` also need `gcc` and `make`.
6. Run `bash scripts/check-self-contained.sh --self-test`.

These steps give you a candidate host. They do not qualify it: a fresh guest still goes through [CLEAN-GUEST-QUALIFICATION](../../infra/repro/CLEAN-GUEST-QUALIFICATION.md).

A new guest has new host keys: set `TBOUND_GUEST_KNOWN_HOSTS` and `TBOUND_GUEST_HOST_ALIAS` in `.local/freeze-host.env` after verifying them. The Ubuntu ISO is a download, with its hash in `evidence/thesis-inputs/abi8-seed-receipt.json`.

The owner side of a first freeze ran from a fresh clone in `tbound-dev`, through signing. The kit's guest chain does not yet accept a new chain, and no full freeze has run from the new paths or on native Linux.

## Disk use

The VM disk can grow to its 40 GiB cap. Run `go clean -cache` in the Linux scratch when the cache grows, and remove old `~/gates/<label>` trees.
