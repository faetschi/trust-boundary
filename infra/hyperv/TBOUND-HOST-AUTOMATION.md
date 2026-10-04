# TBound host automation (trusted offline verifier only)

This is a fixed-task setup for the reviewed offline verifier on the Windows Hyper-V host. It is not an install or run log: creating these files does not register tasks or change the VM. The one-time admin install remains a separate, human-reviewed step.

## Scope and trust boundary

The initial policy is `TRUSTEDVERIFIERONLY`. The guest may run the reviewed offline verifier and fixed test set; it must not run an adversarial LLM or untrusted agent that could persist code and then be booted with the maintenance switch. This workflow does not restore a clean guest image. Adversarial use needs a verified clean-reset and evidence path before it can share these tasks.

The host does not accept guest status as permission to reconnect or run an elevated command. Only the Hyper-V host's observed VM state `Off` permits the fixed `ConnectOff` action. `StopOffline` may request graceful guest shutdown after the bounded test wait; reaching `Off` proves quiescence, not that the verifier passed. It never upgrades a test result to PASS.

The host-action mutex serializes these fixed tasks only. Do not make out-of-band Hyper-V changes to the pinned VM or NIC while one is running.

The normal-user controller must bind each task invocation to a fresh protected host receipt and the exact current guest run. It reconnects and boots only through the fixed trusted-maintenance task after the host observed `Off`, then retrieves the finalized report. A test result is PASS only when the complete artifacts for that invocation are present and verified. Missing, unfinished, stale, or contradictory artifacts remain UNKNOWN/ABORTED_UNKNOWN. A timer, a shutdown request, a host receipt, or an SSH disconnect is not test completion.

## Fixed host tasks

Registered task paths are:

- `\TBound\Inspect`
- `\TBound\Disconnect`
- `\TBound\StopOffline`
- `\TBound\ConnectOff`
- `\TBound\StartTrustedMaintenance`

Each task has a fixed operation and fixed protected action. The operator can run and query these tasks but cannot edit their actions, principal, script, profile, or receipt directory. The protected installed copy lives under `C:\ProgramData\TBoundHostAutomation`; operator-visible receipts are data only.

- `Inspect`: read-only checks; it leaves VM and switch state unchanged.
- `Disconnect`: disconnects the single pinned VM adapter for the untrusted offline phase.
- `StopOffline`: only accepts the disconnected VM, requests graceful shutdown, and reports its host-observed final state. Failure to reach `Off` leaves the adapter disconnected.
- `ConnectOff`: attaches the pinned adapter to the pinned Default Switch only while the VM is `Off`; it does not boot the VM.
- `StartTrustedMaintenance`: boots the same VM on the Default Switch under the fixed `TRUSTEDVERIFIERONLY` policy.

The host helper rechecks the pinned VM identity, adapter and switch IDs, generation, CPU and memory, checkpoint policy/count, disk chain/location/size, and storage thresholds before a mutation. Unknown identity or capacity fails closed.

## One-time administrative install

Review the host action and installer source plus the resulting diff first. The install script is intended to be run once from an elevated PowerShell window in the repository:

```powershell
Set-Location 'C:\Users\Admin\Desktop\FH\Master Software Engineering\MA Thesis\trust-boundary'
.\infra\hyperv\Install-TBoundHostAutomation.ps1
```

The installer validates the target VM and storage without starting/stopping it or changing its network. It copies the reviewed fixed action to the protected ProgramData location, writes the pinned profile, registers the five on-demand SYSTEM tasks, and grants the designated operator run/query access only. It must refuse to overwrite existing protected files or task names. It is not being run as part of this source preparation.

The reviewed profile must match the target `TBound-Ubuntu-2404`, its VM/NIC/Default Switch GUIDs, VM root `F:\TBoundVMs\TBound-Ubuntu-2404`, assets root `F:\TBoundAssets`, 8 vCPU, fixed 16 GiB memory, Generation 2, and the configured single dynamic 40 GiB disk. Storage limits remain at most 100 GiB across tracked roots and at least 700 GiB free on F:.

## Removal and recovery

The uninstaller unregisters only the fixed tasks after verifying their protected action paths, fixed operation arguments, and SYSTEM principal. It leaves the protected action, profile, receipts, and any VM data in place for inspection. Reinstalling requires a separate reviewed process; the installer must not silently adopt or overwrite an existing installation.

If a task action, principal, profile, or receipt is missing or differs from the installed fixed identity, stop and inspect manually. Do not edit task XML or invoke the elevated helper with custom arguments. A failed or timed-out shutdown leaves the network disconnected; only retry a reviewed fixed task after inspecting host state.

## Verification boundary

No host task registration, guest shutdown permission, guest boot, VM network change, checkpoint operation, or test run is performed by preparing these source files. The existing `vm_start.txt` is intentionally left unread and unchanged.
