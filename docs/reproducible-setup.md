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

The Pi CLI is installed in the Linux user environment. TBound no longer intends
to maintain a private Node or SDK package tree. The CLI/RPC extension path is
still in migration; see [install](install.md) and [status](status.md).

## Native Linux

The prototype bootstrap targets Ubuntu 24.04 amd64:

```sh
sudo bash infra/repro/bootstrap-linux.sh <trial-user>
```

Read the script and review each host change before proceeding. In particular,
the user-namespace workaround can relax a system-wide AppArmor restriction.
On a shared system, use a separately reviewed scoped policy or a dedicated
disposable host. See [infra/repro/README.md](../infra/repro/README.md) and the
[guest runbook](../infra/guest/README.md).

## Windows with an Ubuntu Hyper-V guest

For the existing reference VM, use the start and resume steps in
[launch](launch.md#existing-windows-hyper-v-vm). To create a new guest, first
review [infra/repro/README.md](../infra/repro/README.md) and the
[Hyper-V host runbook](../infra/hyperv/SETUP.md). The automated creation path
has not been qualified on a clean host.

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
