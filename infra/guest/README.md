# Ubuntu guest provisioning assets

These files prepare a dedicated Ubuntu 24.04 Noble guest for controlled T-bound prototype trials. They do not create or configure a VM. The online provisioning phase and all verification runs happen inside the guest; the guest NIC must be disconnected in the hypervisor before offline verification.

## Provisioning phase

1. Install Ubuntu Server 24.04.5 LTS in the guest, create the intended non-root trial account, and connect its virtual NIC only for provisioning.
2. Copy this directory into the guest and review `setup-ubuntu-24.04.sh`. It accepts only Ubuntu 24.04 Noble on Linux amd64. It rejects a custom `APT_CONFIG` and binary-scoped APT path overrides, verifies APT's effective source-list paths match the files it scans, rejects third-party sources and insecure APT overrides, then installs Ubuntu packages through signature-checked APT.
3. Run `sudo bash ./setup-ubuntu-24.04.sh <trial-user>`. The script installs Podman, crun, `uidmap`, `slirp4netns`, `fuse-overlayfs`, `libseccomp2`, GCC including the `gcc-13-x86-64-linux-gnu` backend, and download/verification helpers. GCC is needed for the Go race detector. It creates a 65,536-ID subordinate UID/GID range if the account lacks one. Log out of the trial account or reboot before testing the new mapping.
4. The script downloads Node.js `v24.21.0` and Go `go1.27.1` from their exact version URLs. It verifies Node's signed `SHASUMS256.txt` using the release-key fingerprints pinned in the script, then checks the archive hash from that signed manifest. It checks the Go amd64 archive against the pinned SHA-256 below. Install locations are versioned under `/opt/tbound/toolchains/`; package versions, GCC, and runtime versions are recorded in `/var/log/tbound-guest-provision.log`. APT holds the installed Podman, crun, uidmap, network/storage helpers, libseccomp2, and GCC package versions for the trial phase.

The script contains no embedded credentials, downloads no private material, adds no PPA, and fetches no container images. If a later trial needs an image, stage an archive with a reviewed immutable image digest during a separately logged provisioning step, transfer it to the guest, verify its digest, and use `podman load` before disconnecting the NIC. Do not use `podman pull` during trials.

## Prepare pinned dependencies while online

Do this in the guest after provisioning and before creating the clean offline baseline. These commands prepare the Go module cache and the adapter's `node_modules`; npm lifecycle scripts are disabled during installation. The Go command uses the official Go module proxy and checksum database to prepare the full module graph, including transitive dependencies, while the guest is online. It may add entries to `go.sum`, so verify the dependency manifest is frozen before offline verification. Finish this step before disconnecting the guest NIC.

```sh
export PATH=/opt/tbound/toolchains/node-v24.21.0-linux-x64/bin:/opt/tbound/toolchains/go1.27.1/bin:$PATH
export GOTOOLCHAIN=local
export TBOUND_TREE=/path/to/tbound

cd "$TBOUND_TREE/supervisor"
GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org go mod download all

cd "$TBOUND_TREE/adapter"
install -d -m 0700 "$HOME/.cache/tbound-npm-cache"
npm_config_cache="$HOME/.cache/tbound-npm-cache" \
npm ci --ignore-scripts --no-audit --no-fund
```

`go.mod`/`go.sum` and `package-lock.json` pin the dependency graph. The npm cache path is used by the offline runner to recreate a fresh staged `node_modules` tree with `npm ci --offline --ignore-scripts`; no lifecycle scripts run. Keep the provisioning log and dependency preparation record with the baseline checkpoint evidence. Do not run these download commands during the offline phase.

## Baseline checkpoint and offline boot

5. After completing online dependency preparation, save the provisioning log and dependency record. While the guest is running, confirm VMConnect shows Basic Session and clipboard/device redirection are unavailable. Shut Ubuntu down cleanly from its console and wait for Hyper-V to report the VM Off. Follow the host [Hyper-V setup runbook, Phase 2](../hyperv/SETUP.md#phase-2-install-provision-temporarily-then-disconnect): disconnect every network adapter, eject the installer ISO, set the installed disk first in boot order, run the documented read-only verification, and only then create the single ProductionOnly golden baseline checkpoint. Boot with every adapter still disconnected. Confirm the guest has no IPv4 or IPv6 default route and every non-loopback interface reports carrier `0`. Keep the VM offline between runs.

## Offline code verification

For a direct guest-console run, boot the guest from the completed baseline state with every adapter disconnected. Run the verifier as the non-root trial account; enter `sudo -v` locally after disconnecting, then run:

```sh
sudo -v
python3 "$TBOUND_TREE/infra/guest/verify-offline.py" --host-adapter-disconnected
```

For the direct console path, verify in the hypervisor before booting that the VM's virtual NIC is disconnected. `--host-adapter-disconnected` records that operator confirmation. The runner also checks that every guest non-loopback interface has a readable carrier value of `0` and that neither IP family has a default route. Missing, unreadable, unknown, or connected link state fails the gate. Its JSON records the interface names, carrier values, and default-route booleans. These are offline-run prerequisites; they do not prove runtime containment.

### Queue a run from SSH before disconnecting

When operating over SSH, stage this directory and the source tree while the VM is connected. Start an attached tmux pane through an SSH PTY, run `sudo -v` inside that pane, then queue the run:

```sh
tmux new-session -A -s tbound-offline
sudo -v
bash "$TBOUND_TREE/infra/guest/run-offline-queued.sh" --host-adapter-disconnected
```

While the launcher waits, disconnect every Hyper-V network adapter from the host with the approved PowerShell procedure. It waits at most 10 minutes for every non-loopback guest interface to report carrier `0` and for both IP route tables to have no default route, then runs the reviewed verifier as the trial user. The verifier repeats those checks. The flag is an explicit operator attestation; the launcher cannot inspect Hyper-V and does not disconnect or reconnect adapters. Keep the adapter disconnected through the full run. The launcher preserves each run in a new mode-`0700` directory under `$HOME`; `status`, `completed`, `runner.exit-code`, `verification.json`, and `verification.stderr` record its result. A timeout or unreadable network state fails closed. tmux must be available in the guest before the baseline is made, and sudo authorization must be cached from the same tmux pane.

The runner checks the installed Go and Node.js versions, npm, and the actual GCC driver file's `dpkg-query -S` owner. On Noble amd64 it requires the held `gcc-13-x86-64-linux-gnu` backend package, reports that package's version, the resolved driver path, and its binary hash; it also reports the installed package/version manifest for context. It verifies the Go module cache offline; hashes the staged supervisor and adapter source files (excluding generated `artifacts/`, original `node_modules`, and cache directories); hashes the fresh staged dependency tree plus `go.mod`, `go.sum`, `package.json`, `package-lock.json`, `profile.json`, the runner, and setup script; and reports the resolved Go module graph hash and count. It runs `go test -json -count=1 ./...` and `go test -race -json -count=1 ./...` as the trial user with `GOPROXY=off`, `GOSUMDB=off`, and `GOTOOLCHAIN=local`. GCC and cgo are required for the race gate; missing race support is a failure.

The audit suite has one intentional privilege boundary: `TestOpenRejectsUntrustedOwnership` skips for the trial user because changing ownership requires root. Each full normal and race run must report exactly the `(tbound/supervisor/internal/audit, TestOpenRejectsUntrustedOwnership)` skip and no other skipped test. After offline module verification, the runner refreshes cached authorization noninteractively and invokes a fixed helper via `sudo -n env -i` before either longer full-suite phase. It runs only that named test in the audit package with a root-owned temporary HOME, GOCACHE, and TMPDIR beneath `/root`. The helper removes those paths and reports cleanup failure with the exact remaining path. It is a development verification phase, not production privilege separation. The privileged phase must pass without a skipped test. The complete result is `PASS` only when both full-suite phases, that ownership test, offline module verification, and the adapter check pass.

The adapter source is copied to temporary staging without the original `node_modules` or cache directories. The runner rebuilds dependencies there from the prepared npm cache using `npm ci --offline --ignore-scripts --no-audit --no-fund`, then runs `npm run check` with offline mode and lifecycle scripts disabled. That script runs the TypeScript no-emit check and local closure probe. It verifies the Pi SDK version against the profile and lockfile and reports zero provider-stream attempts. The Go end-to-end transcript is a synthetic protocol fixture. No live provider exchange or external effect is run.

The runner writes one compact JSON summary to stdout and exits 0 on success or 1 on failure. Generated Go and adapter reports, build caches, and logs are confined to private temporary directories and removed at exit; it does not write a test log into the source tree by default. The prepared npm cache remains under `$HOME/.cache/tbound-npm-cache`. Cleanup failures fail the run and report any remaining temporary path. The JSON records tested packages, test and skip counts, version/hash/lock evidence, and offline prerequisite observations. It explicitly leaves containment and the WP1/G1 durability gate unestablished. Preserve stdout separately if an evidence record is needed.

## Offline capability probe

Run the read-only capability probe as the non-root account after logging in again:

```sh
bash ./probe-capabilities.sh
```

It prints `PASS`, `FAIL`, or `UNVERIFIED` for each check. Exit status is 0 when all checks pass, 1 when a required check fails, and 2 when the non-destructive probe cannot verify an item. `UNVERIFIED` is not success. The probe does not start a container or pull an image. It tests pidfd and Landlock with live kernel calls, installs a harmless allow-all seccomp filter only in a disposable child process, attempts a rootless user namespace, and asks Podman for its configured rootless state and OCI runtime using temporary storage paths. It reads the current cgroup's `cgroup.type` and checks whether `cgroup.kill` is present. It never writes to `cgroup.kill`, because a successful write sends SIGKILL to every process in that cgroup subtree; the write operation is therefore explicitly `UNVERIFIED` until tested in a separately controlled disposable cgroup.

## Probe interpretation and kernel choice

A kernel feature passes only when the probe calls its syscall or observes its live kernel interface. A package name or binary on disk alone never earns a capability `PASS`. `cgroup.kill-interface: PASS` means the current non-root cgroup exposes the kernel control file; `cgroup-domain: PASS` means its current `cgroup.type` reads exactly `domain`. `cgroup.kill-write` remains `UNVERIFIED` by design because writing it kills processes. If a control is inaccessible through the current cgroup namespace or delegated subtree, the result is `UNVERIFIED`, not an inferred pass.

Do not install an HWE kernel preemptively. First save the probe output and current `uname -r`. If a required kernel feature fails on the live guest and the Ubuntu GA kernel lacks that feature, return to the provisioning phase and consider Ubuntu's supported `linux-generic-hwe-24.04` package. Reboot, rerun the probe, record the kernel and package versions, then disconnect the NIC again. Ubuntu documents HWE as an option for Server installations; its kernel track changes over the life of the LTS release, so record the actual installed build.

## Pinned artifacts

- Node.js: `v24.21.0`, Linux x64 archive `node-v24.21.0-linux-x64.tar.xz`; SHA-256 is taken from the version-specific Node manifest only after its detached signature verifies against the pinned release keys.
- Go: `go1.27.1.linux-amd64.tar.gz`; SHA-256 `63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445`.
- Adapter SDK: `@earendil-works/pi-coding-agent` `0.87.1`, exact versions and transitive integrity values recorded in `package-lock.json`.
- Ubuntu packages: versions resolved from signed Noble archive metadata at provisioning time and recorded from `dpkg-query`. The provisioner holds selected runtime packages, including `gcc`, `gcc-13`, and its actual `gcc-13-x86-64-linux-gnu` backend package. The full installed-package manifest and driver hash provide snapshot context; they do not claim that every compiler dependency is separately held. The log records installed package versions and held package names without printing configured source URLs.

## References

- [Ubuntu archive integrity verification](https://documentation.ubuntu.com/security/software-integrity/archive-verification/) explains APT verification of signed Ubuntu archive metadata and package hashes.
- [Ubuntu's `apt-get` manpage](https://manpages.ubuntu.com/manpages/noble/man8/apt-get.8.html) identifies the effective source-list and source-parts configuration paths; [Ubuntu's Noble `apt.conf` manpage](https://manpages.ubuntu.com/manpages/noble/man5/apt.conf.5.html) documents `APT_CONFIG`, `RootDir`, and configuration loading order.
- [Podman rootless mode](https://docs.podman.io/en/stable/markdown/podman.1.html) documents the subordinate ID requirement and rootless storage behavior.
- [Node.js v24.21.0 downloads](https://nodejs.org/en/download/archive/v24.21.0) publishes the exact Linux x64 artifact and signed checksum files; [Node.js release keys](https://github.com/nodejs/release-keys) publishes the release signing keys and fingerprints.
- [Go downloads](https://go.dev/dl/) publishes the exact Go archive and SHA-256 checksum; [Go installation guidance](https://go.dev/doc/install) describes archive installation.
- Linux kernel documentation: [cgroup v2](https://docs.kernel.org/admin-guide/cgroup-v2.html), [pidfd_open(2)](https://man7.org/linux/man-pages/man2/pidfd_open.2.html), [Landlock](https://docs.kernel.org/userspace-api/landlock.html), and [seccomp filters](https://docs.kernel.org/userspace-api/seccomp_filter.html).
- [Ubuntu HWE kernels](https://documentation.ubuntu.com/kernel/reference/hwe-kernels/) documents the optional 24.04 HWE stack and its current support cycle.
