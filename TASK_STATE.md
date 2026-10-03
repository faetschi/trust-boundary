# TBound handoff state

Last updated: 2026-10-03 (Europe/Vienna)

## Current goal

Continue the existing TBound prototype on branch `codex/tbound-prototype` in this standalone repository:

`C:\Users\Admin\Desktop\FH\Master Software Engineering\MA Thesis\trust-boundary`

Do not restart the VM decision or provisioning. Finish the narrow demonstration in the thesis checklist at `C:\Users\Admin\Desktop\FH\Master Software Engineering\MA Thesis\MSE_MA_Thesis\agentic-harness\todo-implementation-tbound.md`; after that passes, report its evidence and limitations and proceed to the agreed WP1/G1 roadmap. The full thesis experiments will later run on a separate dedicated Linux environment. Keep this Windows guest useful for early implementation and testing.

The user wants a disposable Linux VM so untrusted model/tool activity cannot directly reach the Windows workstation. The accepted claim boundary allows documented residual hypervisor escape risk; do not claim zero escape risk. Keep the thesis checkout and host credentials outside the guest. Use synthetic repositories and canaries. Do not run adversarial tests on the workstation host.

## Decisions and constraints

- FABIAN is Windows 10 Education. Keep its current Windows version.
- Use the existing Hyper-V guest `TBound-Ubuntu-2404`, Ubuntu Server 24.04.5 LTS, Gen 2, 8 vCPU, 16 GiB fixed RAM, 40 GiB dynamically expanding VHDX, with at most one checkpoint.
- The configured storage budget is at most 100 GiB of tracked TBound files on F: and at least 700 GiB free. VM creation required an 800 GiB free-space gate. Do not expand the VHDX or create a golden export without a renewed budget review.
- Hyper-V Basic Session is the intended console mode. “Erweiterte Sitzung” was observed greyed out; do not enable clipboard or device redirection. This visual observation alone is not a complete security proof.
- Temporary Default Switch networking is allowed for trusted provisioning and maintenance. Disconnect it and verify the offline gate before any untrusted execution or offline trial. The provider-only runtime route is not implemented or verified.
- Use a portable provider design. OpenCode Go or OpenRouter can be considered for initial setup; OpenRouter is intended for thesis runs. Model IDs are deferred. Ask for exact model IDs/private credential setup only when needed. Never request or record API keys or passwords in chat, logs, Git, or this file. Accurately identify any hosted provider/model actually used.
- User approved this initial workspace metadata policy: admit regular files/directories; preserve file contents and executable bits; normalize ownership and other permissions; exclude timestamps from generation identity; reject symlinks, hard links, special files, ACLs, extended attributes, and file capabilities; represent sparse files as ordinary file contents; fail explicitly on unsupported entries. This is a decision only: numeric permission rules and implementation remain outstanding.
- Keep TBound files in this standalone repo so it can later become its own repository.
- Orchestrate at a high level. Delegate mechanical code reading, implementation, and tests to GPT-6 Luna agents with xhigh reasoning. Preserve unrelated work.
- Document a reproducible fresh Windows setup. Clearly mark fresh Linux-host migration as untested; do not invent a tested KVM procedure.
- Do not push or publish without a separate instruction.

## Current verified state

### Git and files

- Branch: `codex/tbound-prototype`
- Last committed HEAD: `7d712318b3abdbb1e07d93944305e060b7c1d97c` — `docs: record queued verifier failure and held-package fix`
- At the last repository inspection, `infra/hyperv/INSTALLATION-RECORD.md` was modified with the second queued-run outcome. `vm_start.txt` was untracked and is unrelated. Do not read, stage, alter, or delete `vm_start.txt`.
- This `TASK_STATE.md` is a handoff note requested by the user; it is not a test result or implementation.
- Preserve the existing installation-record modification. Inspect Git status before any further edits or commits.

Recent relevant commits:

- `55c41e1` initial standalone repository import (adapter, supervisor, guest and Hyper-V scaffolds).
- `4878b7e` bounded delta-chain validation.
- `68de01b` APT trust-guard keyring-path correction and regression tests.
- `c8bdaa2` preserve system PATH for the offline Go cgo check.
- `5306d4a` record baseline and the first offline verifier failure.
- `b336602` persistent-tmux offline queue launcher and guest instructions.
- `764bff3` treat held-but-installed Debian packages as installed in the verifier and add a parser regression test.
- `588804b` fresh-host setup guide and README link.
- `7d71231` record the second-run held-package fix and failure.

The implementation tree includes `adapter/`, `supervisor/`, `infra/guest/`, `infra/hyperv/`, and `docs/setup.md`. The latest README correctly labels the project “architecture scaffold only” and states that no provider exchange, containment run, conformance result, or WP1/G1 evidence has been established. Review the normative thesis checklist/specs in the original thesis repository when resuming implementation.

### VM and guest baseline

The installed VM was configured with the verified Ubuntu 24.04.5 AMD64 ISO, Linux Secure Boot template, one disconnected NIC for baseline verification, and the intended disk/CPU/RAM settings. One checkpoint named `ubuntu-24.04.5-golden-baseline` exists. Its inventory reported `SnapshotType=Standard` even though the configured checkpoint policy was ProductionOnly with VSS disabled; that inventory field does not prove creation policy or guest freeze/thaw behavior. The checkpoint restore has **not** been tested.

Prior host Verify passed with the VM Off, one checkpoint, expected disk chain, one disconnected adapter, ISO ejected, installed disk first in boot order, Basic Session attestation, and the F: storage gates. At that time F: had 826.61 GiB free and tracked TBound data used 10.44 GiB. Those figures are historical. Recheck the live host before changes.

Guest facts previously verified: Ubuntu 24.04.5 LTS, kernel `6.8.0-146-generic`, amd64; account `tboundadmin`, UID 1000; German keyboard mapping; root filesystem ext4 on LVM; 65,536-entry subordinate UID and GID ranges. Node.js `v24.21.0`, Go `go1.27.1`, Podman `4.9.3`, crun `1.14.1`, GCC `13.3.0` were provisioned/pinned. Go and npm dependency preparation ran online; npm lifecycle scripts were disabled. The guest has a persistent tmux session named `tbound-offline` from the last observed run.

The setup script does not install tmux. The setup guide now says to verify `command -v tmux`, install it during trusted provisioning if absent, record its version, and confirm it before baseline creation.

### Isolation and maintenance access

The VM was previously in Basic Session. During offline verification, the network gate observed every guest non-loopback carrier at 0 and no IPv4 or IPv6 default route; the launcher also recorded the host adapter-disconnected attestation. These are offline prerequisites, not proof of runtime containment.

SSH maintenance was explicitly authorized and a temporary key was installed. Never read, print, copy into the guest, or commit the private key:
`F:\TBoundAssets\ProvisioningSSH\guest-maintenance-ed25519`.
Its dedicated known-hosts file is under the same directory. The last verified guest host-key fingerprint was `SHA256:22ZTQXN5L54AhovENJ0Eo7ZViomYVv1HRWfbG3izY5g` (the character after `YV` is a lowercase “l”). The last observed guest IP was `172.19.207.142`; DHCP addresses can change. Verify the current address from the console and compare the host key out of band before SSH. If the address changes, do not scan addresses or disable host-key checking.

On Oct 2, after the second failed run completed, the user confirmed the host had reconnected the guest to Default Switch for maintenance. The current VM state, adapter state, IP, SSH reachability, and tmux process state have **not** been rechecked as of this handoff (Oct 3). Inspect them before acting. Do not blindly start, disconnect/reconnect, restore, or queue a new run.

The user once typed a long command in VMConnect and the keyboard produced garbled symbols; a reboot restored input. Prefer SSH for trusted maintenance and use VMConnect for observing/reattaching to the existing terminal. The earlier `tmux a` command was mistakenly tried in Windows PowerShell once and then redundantly inside the already attached Ubuntu tmux, producing the nesting warning. Do not run Windows-side `tmux`; do not nest/unset `TMUX`.

## Offline verifier evidence and what it means

All queued runs below reached the guest-offline gate. They did **not** reach the Go/Pi test suites. Each failure had `checks={}`, no suites, cleanup PASS with no leftover paths, and all claims false.

1. Earlier report `~/tbound-offline-verify.json`: failed because the sanitized Go check omitted system `PATH`, so cgo reported disabled. Host-side diagnosis verified adding `PATH=/usr/bin:/bin` fixed the cgo reading. Commit `c8bdaa2` updated the verifier.
2. First queued run, private guest directory `~/.tbound-offline-queued.234fHWnyYm`: failed because the manifest parser treated held-but-installed `gcc-13-x86-64-linux-gnu` (`dpkg` status `hi `) as missing. A targeted pure-stdlib parser regression passed; updated verifier was deployed to the guest.
3. Second queued run, private guest directory `~/.tbound-offline-queued.2xrGRICwtY`: queued `2026-10-02T18:21:33Z`, offline gate passed `18:26:38Z`, completed `18:26:39Z` with runner and launcher exit 1. JSON SHA-256 was `72927f8e5f160af51c66f60cb15fa12a89dcd10acb2aa39acdcf00ab1548b6ae`. It failed at `go_modules`: “offline Go module graph resolution failed; provision go.mod dependencies first.” It recognized Go 1.27.1 and held GCC 13.3.0. The precise cache/module cause was not diagnosed. No tests ran.
4. The user confirmed reconnection for maintenance after run 3. An agent reported a private evidence folder `F:\TBoundAssets\Evidence\offline-20261002-182638` with matching hashes, but the root agent could not independently verify it due access-review constraints. The current installation record still says host evidence-folder/hash verification is pending. Resolve this discrepancy only if relevant and access is available; do not claim independently verified host evidence.

The latest guest verifier SHA-256 was `9665d4004585515bfd90e7774cb4a8cbf54b972178bea4f580e4bd20f7d46d18`. The queued launcher SHA-256 was `12bc7d88bcbff75e9848b42ee46401c9014a594d73ded62fc623573650f41497`. The first maintenance backup contains the original pre-PATH-fix verifier; a later backup contains the pre-held-package-parser verifier. Preserve all failure reports and backups; never overwrite/retry an unknown report directory.

Targeted checks already reported: APT trust-guard regression suite (7 tests) passed; one pure-stdlib held-package parser regression passed; guest Python AST validation passed; Bash syntax checks for the queued launcher passed. These are narrow checks. There is no successful full offline verification, Go normal/race suite, Pi suite, provider call, real E05 exchange, live effect execution, containment proof, G1 durability/destruction proof, or end-to-end TBound demonstration.

## Immediate next steps

1. Inspect current host state in elevated PowerShell: VM state, adapter switch, F: free space. Open VMConnect only after checking whether the VM is already running. From Ubuntu, confirm current IP, SSH host-key fingerprint, whether the intended tmux session still exists, and whether any launcher/runner is active. Do not disconnect a live test or start another one.
2. Retrieve the already completed latest queued run (the second queued run, third offline attempt overall) read-only if its report still exists; preserve exact output and hashes. Verify any host evidence copy against the guest report before updating the installation record.
3. Diagnose the Go module graph failure before queuing another run. The verifier’s Go environment (see `infra/guest/verify-offline.py`) sets a pinned Go binary, `GOMODCACHE=$HOME/go/pkg/mod`, `GOPROXY=off`, `GOSUMDB=off`, `GOWORK=off`, `GOFLAGS=`, `GOENV=off`, `GOTOOLCHAIN=local`, `CGO_ENABLED=1`, system PATH, and a temporary scratch cache. It stages the supervisor source then runs `go list -m all`, `go mod verify`, and package enumeration. Start by capturing the exact sanitized module-list error without network access or editing the tracked source. Compare cache location/ownership and the report go_mod_sha256/dependency setup record. Only provision missing modules during trusted Default Switch maintenance, log the action, and verify offline resolution again. Do not infer the specific cause from the generic verifier message.

From the existing Ubuntu checkout, a read-only diagnostic that closely matches the verifier module-list environment is:

~~~bash
cd "$HOME/tbound-handoff-9a73af56e8a8/tbound/supervisor"
env -i HOME="$HOME" PATH="/opt/tbound/toolchains/go1.27.1/bin:/usr/bin:/bin" GOTOOLCHAIN=local GOENV=off GOPROXY=off GOSUMDB=off GOMODCACHE="$HOME/go/pkg/mod" GOWORK=off GOFLAGS= CGO_ENABLED=1 /opt/tbound/toolchains/go1.27.1/bin/go list -mod=readonly -m all
~~~

Capture its exact stdout, stderr, and exit code. The explicit readonly flag prevents this diagnostic from editing go.mod/go.sum; if its result differs from the verifier, compare that flag/environment difference before changing source or provisioning.
4. Once the dependency issue is fixed and reviewed source is staged, authorize `sudo -v` privately in the existing tmux pane, queue exactly one new run, disconnect the adapter using the guarded host command below, and wait for terminal launcher status before reconnecting. Use a fresh report directory. Never launch untrusted Go/Pi/provider execution with the NIC attached.
5. If full offline verification passes, record the evidence and continue the narrow todo implementation/tests. Also complete offline capability falsification probes, the baseline restore test, and missing end-to-end requirements before calling the narrow demonstration complete.
6. After the narrow demonstration, give a claim-bounded report and start the WP1/G1 roadmap. Keep Linux physical-host migration explicitly marked untested until exercised.

## Resume VMConnect and the existing tmux terminal

Run the following in **elevated Windows PowerShell** to inspect the live VM, adapter, and F: space:

~~~powershell
$vmName = 'TBound-Ubuntu-2404'
Get-VM -Name $vmName | Select-Object Name,State,CheckpointType,AutomaticCheckpointsEnabled
Get-VMNetworkAdapter -VMName $vmName | Select-Object Name,SwitchName
Get-PSDrive -Name F | Select-Object Name,Free
~~~

If the VM is already Running, open its Basic Session console:

~~~powershell
vmconnect.exe localhost 'TBound-Ubuntu-2404'
~~~

If it is Off, do not start it until the full Hyper-V Test-UbuntuHyperVVm.ps1 -Mode Verify -BasicSessionConfirmed gate passes with the existing VM/ISO arguments, the checkpoint/disk chain is inspected, and no offline run or unknown job is active. Then this guarded command starts it only if it is still Off, its single NIC remains disconnected, and the F: reserve is intact; it also opens VMConnect:

~~~powershell
& { $ErrorActionPreference='Stop'; $n='TBound-Ubuntu-2404'; $v=Get-VM -Name $n; if ([string]$v.State -ne 'Off') { throw 'VM must still be Off after Verify.' }; if ((Get-PSDrive -Name F).Free -lt 700GB) { throw 'F: free space is below 700 GiB.' }; $a=@(Get-VMNetworkAdapter -VMName $n); if ($a.Count -ne 1 -or $a[0].SwitchName) { throw 'Expected exactly one disconnected adapter.' }; Start-VM -Name $n; vmconnect.exe localhost $n }
~~~

Once the console is open, sign in as `tboundadmin` and inspect the terminal/session:

~~~bash
if [ -n "$TMUX" ]; then
  tmux display-message -p -t "$TMUX_PANE" 'session=#{session_name} pane=#{pane_id} tty=#{pane_tty} command=#{pane_current_command}'
else
  tmux list-sessions
fi
~~~

If this shows the existing `tbound-offline` session and you are already inside it, continue at that prompt; do **not** attach again. If you are outside tmux, list sessions first and attach only if the named session exists:

~~~bash
tmux list-sessions
tmux attach -t tbound-offline
~~~

If the session does not exist, first inspect the launcher/report directories and process list to confirm there is no active or unknown run. Create a session only after that inspection. The prior terminal was an attached pane `%0`, `/dev/pts/1`, shell PID 1949, but those values are historical and may have changed.

For trusted SSH maintenance, verify the current guest IP and pinned host key first. Use the protected private key only with the existing restricted SSH configuration; never output its contents. If SSH is unavailable while disconnected, use VMConnect. An SSH timeout after a deliberate NIC disconnect is expected.

### Queue a future offline run (only after the module issue is resolved)

In the existing attached Ubuntu tmux pane, type the account password privately when prompted:

~~~bash
sudo -v
bash "$HOME/tbound-handoff-9a73af56e8a8/tbound/infra/guest/run-offline-queued.sh" --host-adapter-disconnected
~~~

The launcher creates a new private report directory and waits up to ten minutes for every non-loopback carrier to be zero and both IP route tables to have no default route. Do not queue a second launcher. Watch this same pane for a terminal status and exit code. Keep the VM network disconnected through completion and only then reconnect for trusted evidence collection.

Before disconnecting, elevated PowerShell must confirm the VM is Running, F: has at least 700 GiB free, and exactly one adapter is connected to `Default Switch`. The guarded disconnect form used previously is:

~~~powershell
& { $ErrorActionPreference='Stop'; $n='TBound-Ubuntu-2404'; if ((Get-PSDrive -Name F).Free -lt 700GB) { throw 'F: free space is below 700 GiB.' }; $a=@(Get-VMNetworkAdapter -VMName $n); if ($a.Count -ne 1 -or $a[0].SwitchName -ne 'Default Switch') { throw 'Unexpected adapter configuration.' }; Disconnect-VMNetworkAdapter -VMName $n -Name $a[0].Name; $a=@(Get-VMNetworkAdapter -VMName $n); if ($a.Count -ne 1 -or $a[0].SwitchName) { throw 'Network disconnect verification failed.' }; $a | Format-List Name,SwitchName }
~~~

Do not use that disconnect command until a new run is actually queued and waiting. Reconnect only after the report shows terminal completion and the guest pane shows the launcher returned; verify no runner/launcher remains. If inspection shows the VM is Running and the adapter is disconnected, this guarded one-line command reconnects it for trusted maintenance:

~~~powershell
& { $ErrorActionPreference='Stop'; $n='TBound-Ubuntu-2404'; if ([string](Get-VM -Name $n).State -ne 'Running') { throw 'VM must be running.' }; if ((Get-PSDrive -Name F).Free -lt 700GB) { throw 'F: free space is below 700 GiB.' }; $a=@(Get-VMNetworkAdapter -VMName $n); if ($a.Count -ne 1 -or $a[0].SwitchName) { throw 'Expected exactly one disconnected adapter.' }; Connect-VMNetworkAdapter -VMName $n -Name $a[0].Name -SwitchName 'Default Switch'; Get-VMNetworkAdapter -VMName $n | Format-List Name,SwitchName }
~~~

If the adapter is already connected, do not run that command. After maintenance, shut down cleanly, verify the adapter disconnected and the VM Off, and run the complete host Verify gate before any subsequent offline trial.

## Important limitations at handoff

- The VM is provisioned and has passed host configuration checks, but its checkpoint restore is untested and current live host state has not been rechecked.
- Offline network-gate evidence exists for three failed preflight runs; no full Go/Pi tests completed.
- Provider-only network enforcement, provider request capture, a real model exchange, live effect execution, verified Pi closure, full containment, durable-effect fault injection, G1 fixtures, and the narrow end-to-end todo demonstration remain unfinished.
- A regular-filesystem policy has been agreed but not implemented in the generation store.
- Linux-host migration instructions exist only as a stated future task. Fresh physical Linux setup is untested.
- Keep claims aligned with these limits.
