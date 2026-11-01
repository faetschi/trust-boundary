# Self-contained workspace

Everything TBound and the thesis need lives in this repository directory. A clone plus `.local/` moves the project to another machine.

| Path | In git | Contents |
|---|---|---|
| `infra/freeze/` | yes | Freeze kit: gate, freeze, guest chain. |
| `evidence/` | yes | F14 receipts and the few files the thesis cites. |
| `.local/` | no | VM disks, the dedicated WSL distro's disk, seed disk, freeze inputs, owner keys, `freeze-host.env`, local history archive. |
| `thesis/` | no | Checkout of `github.com/faetschi/MSE_MA_Thesis`. |

`scripts/check-self-contained.sh` fails in CI when a tracked file depends on a path outside the repo. Worktrees have no `.local/`: set `TBOUND_LOCAL` to the primary checkout's.

**Linux scratch.** Builds check file modes, which Windows drive mounts don't keep. On Windows, `TBOUND_WORK` therefore points into the WSL disk, and `infra/freeze/tools/pull-receipts.sh` copies results into `evidence/`. Drive mounts must be read-only from WSL.

## Move to another machine

1. Clone this repo, and the thesis into `thesis/`.
2. Copy `.local/` on an encrypted drive. Check it from `.local/assets` with `sha256sum -c SHA256SUMS`.
3. Restore `.local/secrets/freeze-home.tar.gz` on a Linux filesystem with `tar -xzpf`. Without it the kit cannot start another freeze: `10-freeze.sh` checks the previous freeze's receipts and the first freeze's image and runtime tar. The kit also needs `$HOME/go/pkg/mod/cache/download` and `$HOME/.npm/_cacache` filled for its offline builds. The restore is untested.
4. Copy `infra/freeze/host.env.example` to `.local/freeze-host.env` and adjust it.
5. Prepare the host:
   - **Native Linux.** `bash infra/repro/bootstrap-qemu-linux.sh` creates a VM under `.local/vm`, with its image pinned by hash. To use the host itself instead, `sudo bash infra/repro/bootstrap-linux.sh <trial-user>`.
   - **Windows.** `infra\repro\bootstrap-hyperv-windows.ps1` creates the VM under `.local\vm`. `infra\workspace\New-TBoundWsl.ps1 -RootfsPath <rootfs> -RootfsSha256 <hash>` creates the WSL distro `tbound-dev` under `.local\wsl`, with read-only drive mounts for builds.
   - **Inputs.** In a Linux shell, `bash infra/workspace/fetch-inputs.sh <dest-dir>` downloads and verifies the public build inputs. Add `--iso` for the Ubuntu ISO. Then `bash infra/workspace/install-toolchains.sh <dest-dir> <prefix>` unpacks Go and Node and prints the `PATH` to use. `make build` and `make test` also need `gcc` and `make`.
6. Run `bash scripts/check-self-contained.sh --self-test`.

These steps give you a candidate host. They do not qualify it: a fresh guest still goes through [CLEAN-GUEST-QUALIFICATION](../../infra/repro/CLEAN-GUEST-QUALIFICATION.md).

A new guest has new host keys: set `TBOUND_GUEST_KNOWN_HOSTS` and `TBOUND_GUEST_HOST_ALIAS` in `.local/freeze-host.env` after verifying them. The Ubuntu ISO is a download, with its hash in `evidence/thesis-inputs/abi8-seed-receipt.json`.

The kit has run a full freeze only from its previous location, on Windows with WSL and a Hyper-V guest. The new paths and native Linux are untested until the next freeze.

## Disk use

The VM disk can grow to its 40 GiB cap. Run `go clean -cache` in the Linux scratch when the cache grows, and remove old `~/gates/<label>` trees.
