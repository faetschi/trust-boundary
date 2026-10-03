# Fresh-host setup and offline verification

This guide coordinates the [Hyper-V host runbook](../infra/hyperv/SETUP.md) and the
[Ubuntu guest runbook](../infra/guest/README.md). The verified baseline is host FABIAN,
Windows 10 Education, Hyper-V, and Ubuntu Server 24.04.5. Other Windows editions and fresh
Linux physical hosts have not been verified. No Windows OS upgrade is prescribed; stop if
preflight fails.

## Choose a host path

- **Windows:** follow the host runbook end to end, then use the guest steps below. Hyper-V
  availability depends on edition and configuration; run preflight before creating the VM.
- **Linux — UNVERIFIED:** this repository has no tested fresh-physical-host VM setup. The guest instructions
  apply inside an isolated Ubuntu 24.04 amd64 VM, not directly on a personal workstation. A
  Linux host needs its own isolation decision and tested profile; a future baseline belongs on
  dedicated hardware with a declared Ubuntu host version. No KVM procedure is provided here.

The Windows profile is Generation 2, 8 vCPUs, 16 GiB fixed memory, a 40 GiB maximum dynamic
VHDX, one disconnected adapter, and at most one ProductionOnly checkpoint. Do not export or
duplicate it. Its F: storage guards are profile-specific: at most 100 GiB of tracked TBound
files and at least 700 GiB free (800 GiB required before creation). These limits do not apply
to Linux hosts. Keep host paths and other profile values consistent with the runbook.

## Prepare the Windows guest

1. Read the host runbook. Check Hyper-V, its PowerShell module, the storage roots, free-space
   guards, and the named provisioning switch. Its examples use `F:\TBoundAssets`,
   `F:\TBoundVMs`, and `Default Switch`.
2. Download Ubuntu Server 24.04.5 AMD64. Verify the signed checksum manifest and ISO hash as
   described in the runbook; use that verified hash in preflight and creation. Never bypass a
   failed check. Skip the partial-VM repair section on a fresh host.
3. Require a successful `.\Test-UbuntuHyperVVm.ps1 -Mode Preflight` before running
   `.\New-UbuntuHyperVVm.ps1`; confirm creation with `-Mode Created`.
4. Install Ubuntu interactively and create a dedicated non-root trial account. Connect the
   adapter to `Default Switch` only for trusted installation and provisioning. Do not run Go
   or LLM/provider tests while connected; only trusted setup and static checks may run online.
5. While online, copy the source checkout and `infra/guest` assets into the guest. Set
   `TBOUND_TREE` to that checkout's absolute path inside Ubuntu, then run
   `sudo bash ./setup-ubuntu-24.04.sh <trial-user>` from `infra/guest`. It installs Podman/crun,
   pinned Node.js `v24.21.0` and Go `go1.27.1`, and creates a 65,536-ID subordinate UID/GID
   range if needed. Start a fresh trial-user login session (or reboot) as the guest runbook
   instructs.
6. Prepare caches with the guest runbook's `go mod download` and `npm ci --ignore-scripts` commands.
   Keep the provisioning log and dependency-preparation record. Do not download dependencies
   during offline runs. If using SSH, verify `command -v tmux` in the guest before baseline creation.
   The setup script does not install tmux; install it during trusted online provisioning if absent,
   record its package version, and confirm it is available before creating the baseline.
7. Confirm VMConnect is in Basic Session and clipboard/device redirection are unavailable.
   Shut Ubuntu down. Disconnect all adapters, eject the ISO, set the installed disk first in
   boot order, and pass the runbook's complete `-Mode Verify -BasicSessionConfirmed` gate while
   the VM is Off. Create one ProductionOnly checkpoint only after the gate passes. If checkpoint
   creation fails, retain the error; do not change its type or integration-service policy.
8. Confirm the VM remains Off and has exactly one checkpoint, then pass the complete Verify
   gate again. Boot with the adapter disconnected. In Ubuntu, confirm there is no IPv4 or IPv6
   default route and every non-loopback interface has carrier `0`.

## Check guest capabilities

Once after provisioning, while the guest is offline and signed in as the non-root trial account,
run the read-only probe:

```sh
bash "$TBOUND_TREE/infra/guest/probe-capabilities.sh"
```

Save its output and `uname -r`. Require all required probes to pass; `UNVERIFIED` is
unresolved, not success. The probe intentionally does not write to `cgroup.kill`; an installed
package or binary alone does not prove a live kernel capability.

## Run an offline verification

Before each run, observe Basic Session with the guest running, then shut Ubuntu down cleanly
and wait for Hyper-V to report Off. Disconnect every adapter and pass the full host
`-Mode Verify -BasicSessionConfirmed` gate while the VM is Off. Continue only if it passes.
Keep using the same guest checkout and export its actual absolute path as `TBOUND_TREE` in
each trial-user shell; do not use a Windows host path.

### Direct console

Keep the adapter disconnected, boot Ubuntu, sign in as the trial account, and run:

```sh
sudo -v
python3 "$TBOUND_TREE/infra/guest/verify-offline.py" --host-adapter-disconnected
```

The verifier records the adapter-disconnected attestation and checks that each non-loopback
interface has carrier `0` and neither IP family has a default route. These are offline
prerequisites, not proof of runtime containment.

### SSH maintenance

SSH is for trusted staging and maintenance. After the powered-off host Verify gate passes,
reconnect the named provisioning switch and boot the guest only to stage files and queue the
run. Do not run Go or LLM/provider tests while connected. The queued launcher must confirm the
guest is offline before it starts verification.

Use the guest's current address and compare its SSH host key with a fingerprint verified
out of band. Address and fingerprint values vary by host; configure them locally. A public
fingerprint is an identifier, not a credential, and may be recorded with host installation
evidence. Enter the dedicated trial owner's password interactively, or use a private key held
on the operator's machine with restricted local access and no agent forwarding. Never commit
passwords or private keys.

In an SSH PTY, start an attached tmux session only if you are not already in the intended
session. Run `sudo -v` in that same pane, then queue exactly one job:

```sh
tmux new-session -A -s tbound-offline
sudo -v
bash "$TBOUND_TREE/infra/guest/run-offline-queued.sh" --host-adapter-disconnected
```

While the launcher waits, disconnect every Hyper-V adapter using the host runbook. It waits up
to 10 minutes for carrier `0` on all non-loopback interfaces and no default route in either
IP family, then starts the offline verifier and test phases. Keep the adapter disconnected.
Do not queue a second job or retry an unknown state. An SSH timeout after disconnection is
expected; tmux continues in the guest. Reattach with `tmux a -t tbound-offline` from a non-tmux
shell. If already in the intended tmux session, use it; do not nest a session or unset `TMUX`.

Do not reconnect while verification or tests are active. Wait for launcher completion and its
exit status before collecting evidence. The launcher creates a new private mode-0700 run
directory under the trial account home. `status`, `completed`, and `launcher.exit-code`
report launcher completion, including early failures. The verifier's `runner.exit-code`,
`verification.json`, and `verification.stderr` exist only if it started; missing runner
artifacts do not mean PASS. Preserve compact runner JSON stdout separately if needed.

Reconnect only after the run completes if SSH is needed to collect results. Then shut the
guest down cleanly, disconnect adapters, and pass host Verify while the VM is Off before the
next run. With the console path, collect locally after completion and shutdown.

## Interpret the result

### Direct-console run

A direct-console success requires `verify-offline.py` to exit 0 and its compact JSON summary to
report `PASS`. Save the verifier's stdout and exit status together. This path does not create
queued-launcher completion files.

### SSH-queued run

A queued success requires terminal launcher status, `completed`, and `launcher.exit-code=0`,
plus a started verifier with `runner.exit-code=0` and `verification.json` reporting `PASS`.
If the launcher exits before the verifier starts, runner artifacts will be absent; classify
that as a pre-verification failure, never as a pass.

### Scope of PASS

A verifier `PASS` covers offline prerequisites, pinned toolchain/dependency checks, Go normal
and race suites, the privileged ownership test, and the adapter type/closure check. It does not
prove runtime containment, a real provider exchange, external-effect control, or WP1/G1. The
adapter check makes zero provider-stream attempts, and the Go end-to-end transcript is
synthetic. Provider route, model IDs, and LLM credentials remain deferred; supply keys privately
after those profiles are selected.

Keep each run's evidence private. Record the host/guest profile, completion state, exit codes,
JSON, and error output. A failed or unverified network gate is not a pass. Do not remove held
package versions to force a pass. Investigate from a trusted provisioning phase before
scheduling another run.

## Fresh-host completion checklist

- [ ] The host path is the documented Windows profile, or a Linux host has its own tested VM
      isolation profile.
- [ ] Signed ISO verification, read-only preflight, VM creation, and Created/Verify checks pass.
- [ ] Guest setup, pinned dependency preparation, provisioning records, Basic Session check,
      and the single disconnected baseline checkpoint are complete.
- [ ] The selected direct-console or SSH-queued path reaches a terminal verifier result. Capture
      its JSON and exit status; for SSH queues also preserve launcher completion and exit data.
      Collect evidence or reconnect only after the run completes.
- [ ] The result is described as code/offline verification only unless containment and WP1/G1
      evidence has been separately produced.
