# Reproducibility setup

This guide points to the current Linux and Windows-host/Linux-VM procedures.
The scripts are prototype tooling: they have not completed end-to-end
qualification on a clean native Linux host or a newly created VM. A successful
bootstrap or capability probe is not evidence of containment or G1.

## Runtime versions

- The setup scripts pin Node.js 24.21.0 for reproducibility.
- The official Pi CLI experiment pin is 0.87.1. Pi's documented Node minimum
  is 22.19; the reproducibility pin is separate from that minimum.
- The guest setup pins Go 1.27.1.
- GNU `make` is installed and held by the guarded guest provisioner; the
  documented `make build` install path needs it.

The Pi CLI is installed in the Linux user environment. TBound no longer intends
to maintain a private Node or SDK package tree. The CLI/RPC extension path is
still in migration; see [install](install.md) and [status](status.md).

## Native Linux (recommended, no VM)

The prototype bootstrap targets Ubuntu 24.04 amd64:

```sh
sudo bash infra/repro/bootstrap-linux.sh <trial-user>
```

Read the script and review each host change before proceeding. The
user-namespace workaround is **not** applied by default; set
`TBOUND_APPLY_USERNS_SYSCTL=1` only on a dedicated disposable host after
reviewing the system-wide AppArmor trade-off. On a shared system, use a
separately reviewed scoped policy instead. See
[infra/repro/README.md](../infra/repro/README.md) and the
[guest runbook](../infra/guest/README.md).

## Windows WSL2 (development only)

An Ubuntu 24.04 WSL2 distro can run the CLI and fixtures, but it is **not** the
governed qualification profile: WSL2 does not provide the declared D09 cgroup,
Landlock, and VM boundary. Use the read-only WSL probe as a development smoke:

```powershell
.\infra\repro\probe-host.ps1 -Target Wsl -Distro <distro>
```

Do not present a WSL pass as governed qualification, and do not relax the
host-wide AppArmor/userns policy just to make WSL pass. Use native Linux or one
of the optional Linux VM paths for governed work.

## Windows with an Ubuntu Hyper-V guest (optional Linux VM)


For the existing reference VM, use the start and resume steps in
[launch](launch.md#existing-windows-hyper-v-vm). To create a new guest, first
review [infra/repro/README.md](../infra/repro/README.md) and the
[Hyper-V host runbook](../infra/hyperv/SETUP.md). The automated creation path
has not been qualified on a clean host.

## Linux host with a QEMU guest (optional Linux VM)

On a Linux host without Hyper-V (or from a WSL2 development host), create a cloud-image VM with:

```sh
bash infra/repro/bootstrap-qemu-linux.sh --vm-dir "$HOME/.local/share/tbound-vm"
```

The script uses KVM when it can open `/dev/kvm` and otherwise falls back to
TCG, which is much slower. The default light install uses the GA kernel,
installs Pi and TBound, and runs `tbound-doctor` plus the native fixture.
`--developer` opts into adapter/install verification; `--hwe` opts into the
ABI-8 HWE kernel, which is not yet qualified; `--allow-userns-sysctl` opts into
the host-wide AppArmor workaround. `--qualify-cgroup-reuse` explicitly runs the
audited cgroup-reuse probe once in a delegated scope; it is not part of the
default install, and an `incompatible`/`unverified` exit 2 is a qualification
failure that leaves the user-prefix install intact. Qualification failures are
reported after the user-prefix install and do not undo it. Review the script
before running it; it creates a new VM and does not delete an existing one. See
[infra/repro/README.md](../infra/repro/README.md#qemu-on-linux-linux-vm-path).

### Observed cgroup-reuse result (2026-10-20)

The audited one-run `tbound-profile cgroup-reuse-check
--confirm-disposable-cgroup` check on the existing Ubuntu 24.04.5 VM with kernel
`7.0.0-38-generic` returned **`incompatible` / exit 2**: the post-kill clone
received SIGKILL while the fresh and outside controls remained alive. Source,
archive, identity, and raw observations are in the audit receipts.

This means HWE 7.0 is **not** a compatible kernel for this profile. HWE remains
unqualified, and GA 6.8 remains development-only (Landlock ABI 4). Use a
fixed/backported kernel or wait for the functional check to pass before treating
a host as qualified. Do not retry, extend sleeps, or bypass the clone path to
obtain a different result.

## Thesis run readiness

A checkout contains the install, measurement, and evidence components, but a
successful setup is not a thesis run. The repository-side pieces are:

- host bootstrap, pinned toolchains, Pi, TBound install, doctor and the
  explicit cgroup-reuse qualification wrapper;
- D09 image build/sign tools in `packaging/governed/`, service units in
  `infra/guest/systemd/`, and the owner runbook
  [`infra/guest/PROVISIONING-SIGNING.md`](../infra/guest/PROVISIONING-SIGNING.md);
- the draft pilot and G1 corpora under [`corpus/pilot`](../corpus/pilot/README.md)
  and `corpus/g1`, with `build.py`, artifacts, fixtures, and rubrics;
- the EVQ/route inputs in [`infra/conformance`](../infra/conformance), the
  `tbound-run`/`tbound-profile`/`tbound-doctor` commands, and the evidence
  index under `docs/`.

Owner-gated material is not in the repository: the offline admin and Cosign
keys, the signed host profile and D09 image/registration, the service account
and managed live root, corpus reviews and preregistration, the provider-route
switch/credentials, and a qualified ABI-8 guest. Do not treat a local build or
a probe pass as those artifacts.

The correct order is: native Linux setup (or an optional qualified VM), install
and doctor, explicit cgroup-reuse qualification, the owner signing session,
corpus review/digest/preregistration, then the pilot and thesis corpus. The
pilot README is explicit that its results are not thesis results. See
[`docs/status.md`](status.md) for the current claim boundary.

## What a probe means

The capability probe reports host mechanisms and tool availability. It does
not test the TBound process boundary, the signed runtime profile, service
admission, external effects, or a Pi/provider exchange. The current reference
VM reports Landlock ABI 4. Production source freezes D09 to exactly ABI 8:
`D09LandlockABI` is 8, template validation requires equality, and the cell
entrypoint rejects other requested ABIs. The generic sandbox diagnostic reports
Landlock availability from ABI 1 onward; that broader signal is not the D09
profile gate. Later HWE boots reached userspace but are not qualified. Measure
the running ABI directly; do not infer it from a kernel version or installed
package.

For offline verification inside the dedicated guest, follow
[infra/guest/README.md](../infra/guest/README.md). An offline PASS covers its
listed host and code checks only; it is not a governed TBound run.
