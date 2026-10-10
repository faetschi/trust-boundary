# TBound host automation (trusted offline verifier only)

This is a fixed-task setup for the reviewed offline verifier on the Windows Hyper-V host. It is not an install or run log: creating these files does not register tasks or change the VM. The one-time admin install remains a separate, human-reviewed step.

## Scope and trust boundary

The initial policy is `TRUSTEDVERIFIERONLY`. It supports trusted component offline tests only. It does not authorize adversarial agent execution, establish runtime containment, or prove a clean guest image. The guest may run the reviewed offline verifier and fixed test set; it must not run an adversarial LLM or untrusted agent that could persist code and then be booted with the maintenance switch.

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
- `Disconnect`: disconnects the single pinned VM adapter before a trusted offline component-test phase.
- `StopOffline`: only accepts the disconnected VM, requests graceful shutdown, and reports its host-observed final state. Failure to reach `Off` leaves the adapter disconnected.
- `ConnectOff`: attaches the pinned adapter to the pinned Default Switch only while the VM is `Off`; it does not boot the VM.
- `StartTrustedMaintenance`: boots the same VM on the Default Switch under the fixed `TRUSTEDVERIFIERONLY` policy.

The host helper rechecks the pinned VM identity, adapter and switch IDs, generation, CPU and memory, checkpoint policy/count, disk chain/location/size, and storage thresholds before a mutation. Unknown identity or capacity fails closed.

## One-time administrative install

Review the bootstrap, installer, action, policy module, and initializer plus their hashes first. The repository is writable by the operator account, so never run an elevated script directly from the repository. Use the exact hash-guarded bootstrap command supplied with the reviewed source snapshot from a 64-bit elevated Windows PowerShell 5.1 window:

```powershell
$sourceRoot = 'C:\Users\Admin\Desktop\FH\Master Software Engineering\MA Thesis\trust-boundary'
$bootstrapPath = Join-Path $sourceRoot 'infra\hyperv\Bootstrap-TBoundHostAutomation.ps1'
$expectedBootstrapSha256 = '0EF86511A6A00F0EB3F285E9CDD6D9703892C9F311B86A427E367130CC335469'
$bootstrapBytes = [IO.File]::ReadAllBytes($bootstrapPath)
$sha = [Security.Cryptography.SHA256]::Create()
try { $actualBootstrapSha256 = ([BitConverter]::ToString($sha.ComputeHash($bootstrapBytes))).Replace('-', '') } finally { $sha.Dispose() }
if ($actualBootstrapSha256 -cne $expectedBootstrapSha256) { throw 'Bootstrap SHA-256 mismatch.' }
$bootstrapText = [Text.UTF8Encoding]::new($false, $true).GetString($bootstrapBytes)
& ([ScriptBlock]::Create($bootstrapText)) -SourceRoot $sourceRoot -OperatorSid 'S-1-5-21-2350865082-651554413-1548572510-1004'
```

The bootstrap verifies exact source bytes, atomically creates the Admin/SYSTEM-only source staging directory, and runs the captured installer from that protected copy. The installer validates the target VM and storage without starting/stopping it or changing its network. It creates protected ProgramData files and five disabled SYSTEM tasks, verifies action/profile hashes, task definitions, and exact task ACLs, then enables the tasks and grants only the designated SID run/query access. It refuses existing install or task paths. Any failure preserves the stage and partial protected state for inspection; do not retry until the exact live state has been reviewed. The installer currently requires the VM host state `Off`.

After a successful administrator install, the protected `Initialize-TBoundOperatorSsh.ps1` is run once as `FABIAN\CodexSandboxOffline`. It generates a separate Ed25519 maintenance key under the operator-only key directory and places only its public key in the outbox. A human Admin then enrolls that public key in the guest account using the existing protected Admin credential. Add the OpenSSH `restrict` option to the authorized key. That option disables forwarding and PTY requests; it does not constrain the key to a fixed guest command. The key provides the normal `tboundadmin` shell and SCP/SFTP access needed for trusted maintenance. No Windows Hyper-V administrator/group rights or password automation are granted, and the old Admin-owned private key remains untouched.

The controller accepts only the exact operator SID, verifies the private-key and policy-file ACLs, and checks the single guest host key against the case-sensitive ED25519 fingerprint pin before SSH. The operator-writable public-data directory can be changed by that operator, so the fingerprint check remains mandatory. Current repository changes do not pass the controller's older reviewed-source commit gate; freeze and review the exact controller/source snapshot before trying a verifier run.

The SID embedded in this snapshot is specific to `FABIAN`. A fresh Windows host must resolve its own dedicated Codex operator SID and regenerate/review the source pins before any install; do not carry SID `...-1004` to another host. The existing baseline checkpoint restore has not been tested and is outside this trusted-verifier helper scope.

The reviewed profile must match the target `TBound-Ubuntu-2404`, its VM/NIC/Default Switch GUIDs, VM root `F:\TBoundVMs\TBound-Ubuntu-2404`, assets root `F:\TBoundAssets`, 8 vCPU, fixed 16 GiB memory, Generation 2, and the configured single dynamic 40 GiB disk. Storage limits remain at most 100 GiB across tracked roots and at least 700 GiB free on F:.

## Removal and recovery

The uninstaller unregisters only the fixed tasks after verifying their protected action paths, fixed operation arguments, and SYSTEM principal. It leaves the protected action, profile, receipts, and any VM data in place for inspection. Reinstalling requires a separate reviewed process; the installer must not silently adopt or overwrite an existing installation.

If a task action, principal, profile, or receipt is missing or differs from the installed fixed identity, stop and inspect manually. Do not edit task XML or invoke the elevated helper with custom arguments. A failed or timed-out shutdown leaves the network disconnected; only retry a reviewed fixed task after inspecting host state.

## Verification boundary

No host task registration, guest key enrollment, guest shutdown, guest boot, VM network change, checkpoint operation, or test run is performed by preparing these source files. The existing `vm_start.txt` is intentionally left unread and unchanged.
