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

## Native Linux

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

## Windows with an Ubuntu Hyper-V guest

For the existing reference VM, use the start and resume steps in
[launch](launch.md#existing-windows-hyper-v-vm). To create a new guest, first
review [infra/repro/README.md](../infra/repro/README.md) and the
[Hyper-V host runbook](../infra/hyperv/SETUP.md). The automated creation path
has not been qualified on a clean host.

## Linux host with a QEMU guest

On a Linux host without Hyper-V (including WSL2), create a cloud-image VM with:

```sh
bash infra/repro/bootstrap-qemu-linux.sh --vm-dir "$HOME/.local/share/tbound-vm"
```

The script uses KVM when it can open `/dev/kvm` and otherwise falls back to
TCG, which is much slower. The default light install uses the GA kernel,
installs Pi and TBound, and runs `tbound-doctor` plus the native fixture.
`--developer` opts into adapter/install verification; `--hwe` opts into the
ABI-8 HWE kernel, which is not yet qualified; `--allow-userns-sysctl` opts into
the host-wide AppArmor workaround. Qualification failures are reported after
the user-prefix install and do not undo it. Review the script before running
it; it creates a new VM and does not delete an existing one. See
[infra/repro/README.md](../infra/repro/README.md#path-c-qemu-vm-on-a-linux-host).

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
