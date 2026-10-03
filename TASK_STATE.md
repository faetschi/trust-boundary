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
- OpenRouter is the primary planned provider. OpenCode Go is optional for testing and may be selectable later; exact authentication compatibility for a second profile is unverified. These are portable design plans, not implemented or verified provider support. Model IDs are deferred. Never request or record API keys or passwords in chat, logs, Git, or this file. Accurately identify any hosted provider/model actually used.
- User approved this initial workspace metadata policy: admit regular files/directories; preserve file contents and executable bits; normalize ownership and other permissions; exclude timestamps from generation identity; reject symlinks, hard links, special files, ACLs, extended attributes, and file capabilities; represent sparse files as ordinary file contents; fail explicitly on unsupported entries. The workspace package implements directory mode `0755`, regular-file mode `0644` or `0755` when executable, normalized ownership, and a canonical manifest from the existing delta types. Its static-reviewed Linux code has not been compiled or tested.
- Keep TBound files in this standalone repo so it can later become its own repository.
- Orchestrate at a high level. Delegate mechanical code reading, implementation, and tests to GPT-6 Luna agents with xhigh reasoning. Preserve unrelated work.
- Document a reproducible fresh Windows setup. Clearly mark fresh Linux-host migration as untested; do not invent a tested KVM procedure.
- Do not push or publish without a separate instruction.

## Current verified state

### Git and files

- Branch: `codex/tbound-prototype`
- Current HEAD: `e187650947c6e45fd10ffa9a2642845bcf438f53`; root independently confirmed this five-file commit contains the workspace package candidate. It has no compile or test result.
- This handoff refresh updates `TASK_STATE.md`, `docs/implementation-status.md`, and `infra/hyperv/INSTALLATION-RECORD.md`. The preexisting untracked `vm_start.txt` is unrelated; do not read, stage, alter, or delete it.
- This `TASK_STATE.md` is a handoff note requested by the user; it is not a test result or implementation.
- Preserve unrelated working-tree changes, including `vm_start.txt` and any other untracked paths. Do not read or alter `vm_start.txt`; do not stage unrelated work.

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
- `2e342f4` add the TBound task handoff state.
- `980f36d` clarify setup paths and implementation status.
- `9bcf88b` add Go module failure diagnostics.
- `711e641` complete offline Go dependency preparation.
- `99f87dd` add the planned restore acceptance runbook (untested).
- `d317e7a` add the OpenRouter request/parser/capture mapping only; no provider request or Pi wiring.
- `e187650` commit the five-file bounded workspace package candidate; uncompiled and untested.

The OpenRouter request/parser/capture mapping is committed at `d317e7a85c09b6d71c1dce557eeb759ce8fa79e4` in `supervisor/internal/broker/openrouter/`. Strict Unicode validation and pre-marshal size bounds passed static review only. It has not been compiled, formatted, or tested; there have been no HTTP requests, Pi wiring, or live provider support proven.

The committed five-file workspace candidate at `e187650` includes `supervisor/internal/workspace/`: descriptor-relative scanning/import, bounded directory reads and streaming, mount identity checks on fresh opened file descriptors before and after reads, disjoint-root validation, and normalization into existing `delta.TreeManifest` types. Static author and independent review accepted these fixes. It has not been compiled, gofmt-checked, or tested; no mount-regression test has run. It does not seal or publish generations, connect approved deltas to a durable ledger, or prove containment. Quiescence and complete xattr visibility remain caller/profile obligations and are not proven on the guest. The new workspace and OpenRouter packages have not been deployed to the guest.

The implementation tree includes `adapter/`, `supervisor/`, `infra/guest/`, `infra/hyperv/`, and `docs/setup.md`. The latest README correctly labels the project “architecture scaffold only” and states that no provider exchange, containment run, conformance result, or WP1/G1 evidence has been established. Review the normative thesis checklist/specs in the original thesis repository when resuming implementation.

### VM and guest baseline

The installed VM was configured with the verified Ubuntu 24.04.5 AMD64 ISO, Linux Secure Boot template, one disconnected NIC for baseline verification, and the intended disk/CPU/RAM settings. One checkpoint named `ubuntu-24.04.5-golden-baseline` exists. Its inventory reported `SnapshotType=Standard` even though the configured checkpoint policy was ProductionOnly with VSS disabled; that inventory field does not prove creation policy or guest freeze/thaw behavior. The checkpoint restore has **not** been tested.

Prior host Verify passed with the VM Off, one checkpoint, expected disk chain, one disconnected adapter, ISO ejected, installed disk first in boot order, Basic Session attestation, and the F: storage gates. At that time F: had 826.61 GiB free and tracked TBound data used 10.44 GiB. Those figures are historical. Recheck the live host before changes.

Guest facts previously verified: Ubuntu 24.04.5 LTS, kernel `6.8.0-146-generic`, amd64; account `tboundadmin`, UID 1000; German keyboard mapping; root filesystem ext4 on LVM; 65,536-entry subordinate UID and GID ranges. Node.js `v24.21.0`, Go `go1.27.1`, Podman `4.9.3`, crun `1.14.1`, GCC `13.3.0` were provisioned/pinned. Go and npm dependency preparation ran online; npm lifecycle scripts were disabled. The Oct 3 console reported `tmux list-sessions` output `tbound-offline: 1 windows (created Fri Oct 2 17:33:58 2026) (attached)`. Record the session as currently observed; do not infer how it survived host or guest shutdown, and inspect actual state before attaching.

The setup script does not install tmux. The setup guide now says to verify `command -v tmux`, install it during trusted provisioning if absent, record its version, and confirm it before baseline creation.

### Isolation and maintenance access

The VM was previously in Basic Session. During offline verification, the network gate observed every guest non-loopback carrier at 0 and no IPv4 or IPv6 default route; the launcher also recorded the host adapter-disconnected attestation. These are offline prerequisites, not proof of runtime containment.

SSH maintenance was explicitly authorized and a temporary key was installed. Never read, print, copy into the guest, or commit the private key:
`F:\TBoundAssets\ProvisioningSSH\guest-maintenance-ed25519`.
Its dedicated known-hosts file is `F:\TBoundAssets\ProvisioningSSH\known_hosts`. The last verified guest host-key fingerprint was `SHA256:22ZTQXN5L54AhovENJ0Eo7ZViomYVvlHRWfbG3izY5g` (the substring `YVvl` contains lowercase `l` after `v`). The Oct 3 console reported guest IP `172.24.148.77/20` (metric 100), and pinned-key SSH succeeded there; `172.19.207.142` is the existing host-key alias, not the destination. Reconfirm the destination IP at the Ubuntu console before each use. The following PowerShell command runs read-only identity, kernel, and tmux status checks with strict host-key verification:
~~~powershell
ssh -F NUL -o "UserKnownHostsFile=F:\TBoundAssets\ProvisioningSSH\known_hosts" -o GlobalKnownHostsFile=NUL -o StrictHostKeyChecking=yes -o HostKeyAlias=172.19.207.142 -o HostKeyAlgorithms=ssh-ed25519 -o PubkeyAcceptedAlgorithms=ssh-ed25519 -o KexAlgorithms=curve25519-sha256,curve25519-sha256@libssh.org -o ForwardAgent=no -o ClearAllForwardings=yes -o BatchMode=yes -o IdentitiesOnly=yes -o IdentityAgent=none -i "F:\TBoundAssets\ProvisioningSSH\guest-maintenance-ed25519" tboundadmin@172.24.148.77 "id -u && hostname && uname -r && tmux list-sessions"
~~~

After the second failed run completed on Oct 2, the user confirmed a maintenance reconnection. On Oct 3, user-provided elevated host inventory reported the VM Running, Generation 2, `ProductionOnly`, with automatic checkpoints disabled; one adapter `Netzwerkkarte` on `Default Switch`; one checkpoint; and F: free space of 810.17 GiB. The inventory reported an attached disk path ending in `TBound-Ubuntu-2404_7434EDE1-4C24-46FB-932E-DA4CC74AD396.avhdx`; that path alone does not establish the full disk chain or restore success. Root has not independently queried the host. The Oct 3 console showed `Enhanced Session` grey and the attached tmux session noted above. The user confirmed current guest IP `172.24.148.77/20` (metric 100), and a pinned-key SSH maintenance probe succeeded as `tboundadmin` UID `1000`, hostname `tboundubuntu2404`, kernel `6.8.0-146-generic`. The first process-filter query had quoting errors; the corrected bounded read-only query found no active Go, npm, Node, verifier, or launcher process. The tmux session and an expected system unattended-upgrade/shutdown Python process were present. At that earlier console check, no verifier had been started and no new offline run had yet been queued. Do not change VM or network state, restore, or queue another run until the live process state and next action are reviewed.

The user once typed a long command in VMConnect and the keyboard produced garbled symbols; a reboot restored input. Prefer SSH for trusted maintenance and use VMConnect for observing/reattaching to the existing terminal. The earlier `tmux a` command was mistakenly tried in Windows PowerShell once and then redundantly inside the already attached Ubuntu tmux, producing the nesting warning. Do not run Windows-side `tmux`; do not nest/unset `TMUX`.

## Offline verifier evidence and what it means

The first two queued runs failed before their Go/Pi suites. The third passed the guest-offline gate, ran Go test phases, and failed overall with exit code 1; see the preserved report summary below. Its underlying Go failure cause is unknown.

1. Earlier report `~/tbound-offline-verify.json`: failed because the sanitized Go check omitted system `PATH`, so cgo reported disabled. Host-side diagnosis verified adding `PATH=/usr/bin:/bin` fixed the cgo reading. Commit `c8bdaa2` updated the verifier.
2. First queued run, private guest directory `~/.tbound-offline-queued.234fHWnyYm`: failed because the manifest parser treated held-but-installed `gcc-13-x86-64-linux-gnu` (`dpkg` status `hi `) as missing. A targeted pure-stdlib parser regression passed; updated verifier was deployed to the guest.
3. Second queued run, private guest directory `~/.tbound-offline-queued.2xrGRICwtY`: queued `2026-10-02T18:21:33Z`, offline gate passed `18:26:38Z`, completed `18:26:39Z` with runner and launcher exit 1. JSON SHA-256 was `72927f8e5f160af51c66f60cb15fa12a89dcd10acb2aa39acdcf00ab1548b6ae`. It failed at `go_modules`: “offline Go module graph resolution failed; provision go.mod dependencies first.” It recognized Go 1.27.1 and held GCC 13.3.0. Before dependency preparation, during trusted Default Switch maintenance, a read-only metadata-only `go list -m all` ran as UID `1000` from a private fresh staging directory with safe `HOME`/`GOCACHE`, the actual `GOMODCACHE`, and the same downloads-disabled verifier environment (`GOPROXY=off`, `GOSUMDB=off`, and related settings). It exited `1`; stderr consisted of six repetitions of `go: module lookup disabled by GOPROXY=off` and named no modules. This confirms the actual cache cannot resolve the graph with downloads disabled, but does not identify the blocking lookup. A separate module-cache inventory found .mod metadata, but did not verify full module archives, for six transitive requirements: testify v1.7.0, go-spew v1.1.0, go-difflib v1.0.0, objx v0.1.0, yaml.v3 v3.0.0-20200313102051-9f266ea9e77c, and check.v1 v0.0.0-20161208181325-20d25e280405. This does not establish which lookup caused the failure. The host repository go.sum exists with two JCS checksum lines. The guest metadata baseline go.sum had eight lines: those two JCS lines plus six transitive go.mod hashes, so it differs from the host file. go.mod declares module tbound/supervisor, Go 1.21, and jcs v1.0.1. An earlier wrong-directory invocation returned `go.mod not found`; it was corrected and is separate from the recorded graph failure. The earlier diagnostic staging was cleaned; no provider call, Go application, or test suite ran.
4. Root independently read the preserved host `verification.json` for the Oct 2 second run; SHA-256 `72927f8e5f160af51c66f60cb15fa12a89dcd10acb2aa39acdcf00ab1548b6ae` matches the guest report. This verifies only that JSON file; the full seven-file evidence folder has not been independently compared.
5. Third queued run (2026-10-03): the user refreshed `sudo -v`; the assistant queued one launcher over SSH at 13:09:52 UTC. Root read `WAITING_FOR_OFFLINE` at 13:13:05 UTC; launcher PID `9019` had a sleep child and Bash parent PID `1949`. The user confirmed the guarded NIC disconnect, PowerShell showed blank `SwitchName`, and the offline gate passed at 13:18:33 UTC. The verifier completed at `2026-10-03T13:19:19+00:00` with runner and launcher exit code `1`, status `FAIL`.
6. Preserved evidence is in `F:\TBoundAssets\Evidence\offline-20261003-131833`. Root independently verified `verification.json` SHA-256 `5c5fa36fea3d91c36f70c32100f4f1cfaca2f86fe6ec1169a452ddbb8baa8350` and `capture-manifest` SHA-256 `48170022e98ed0c15216074e8b8f276b3f83389f81e7273c555c016062397878`. An agent verified size/hash pairs for all nine captured guest files; root's independent file verification is limited to those two named files. The report records `go_modules`, `adapter_dependencies`, and `node_adapter` PASS; Pi `0.87.1`, `provider_stream_attempts=0`; `go_tests` and `go_race` counters of 62 passed, 0 failed, 0 skipped; an overall exit 1 and `unexpected_skips`/failed-packages condition naming expected `TestOpenRejectsUntrustedOwnership`. The summary omitted `package_results`; do not treat these counters as a successful full suite or infer the underlying Go error from the unexpected-skip entry. Captured stdout/stderr were only in memory and scratch cleanup deleted them; saved `verification.stderr` and `sudo.stderr` are empty. The exact Go error is unknown. The report records ownership audit package-fail `0`, run/pass `0/0`, noninteractive sudo true, cleanup and global cleanup PASS, offline carrier `0` and no default routes with host-disconnect attestation true, and all four claims false. The reported source comparison matched six of seven files; the captured setup installer came from an earlier source base than the corrected host installer, so this is not a full-tree match.

Before the Oct 3 dependency apply, the guest verifier SHA-256 was `9665d4004585515bfd90e7774cb4a8cbf54b972178bea4f580e4bd20f7d46d18` and guest `supervisor/go.sum` SHA-256 was `7913bff96ace35f736cabee75fb7b469f97976bb09804ad91a4794f705fe8c21`. The prior queue launcher was recorded as SHA-256 `12bc7d88bcbff75e9848b42ee46401c9014a594d73ded62fc623573650f41497`; its current hash, mode, and owner must be rechecked before reuse. Preserve all failure reports and backups; never overwrite/retry an unknown report directory.

### Latest guest application and pane state - 2026-10-03

- Host commit `711e6416424766e8aa7e0d7ff8beac4dcb171002` committed the prepared `supervisor/go.sum` and README; it did not change `go.mod` or application source.
- An atomic guest apply guarded by exact old-file hashes installed `supervisor/go.sum` SHA-256 `2ff7956b129800bfed3b5f9ca671ece52317fc4fa4786c76f4672abb075f1aea` and `infra/guest/verify-offline.py` SHA-256 `bbcefad25a5184bb87b2efaba3b73b2480be609f6daff30a376aed9d184b2272`; both are mode `0600`, UID `1000`. Guest `go.mod` remained byte-identical, SHA-256 `72a77c58ae54b3c6e4a76efe96f0ad4313c03693809d85e2d3304ca3adad0713`.
- The private rollback directory `/home/tboundadmin/.tbound-maintenance-backup-2Y2Yh0` is UID `1000`, mode `0700`, and retains the old sum and verifier hashes above. The apply log SHA-256 is `60799d933f6cfa77baf306f93d7f29d4e823655434d7408941c332d285e4e097`.
- `tbound-offline` window 0, pane 0, terminal `pts/1`, Bash PID `1949`, attached client tty `1`, and `FIONREAD=0` were observed before the latest queue. At that point, no Go, npm, Node, launcher, or verifier process was active.
- Metadata-only `go list -m all` and `go mod verify` passed while the NIC was connected; they are not an offline verifier pass. After the failed run, the user reconnected the guest to `Default Switch` for trusted maintenance only. Fresh pinned-key SSH confirmed no active guest test processes. The VM is now maintenance-connected, not offline. The new workspace and OpenRouter packages have not been deployed; the guest remains on the earlier `9a73` source plus the prior verifier and dependency-sum patches.
- A bounded Go-phase diagnostics patch is written but uncommitted and under independent review by `/root/go_phase_diagnostics_review_luna`. Host-only pure checks passed (Python 3/3 and Go module helper 5/5); no Go compile, gofmt, or guest test has run for this patch, and it is not deployed.

Targeted checks already reported: APT trust-guard regression suite (7 tests) passed; one pure-stdlib held-package parser regression passed; guest Python AST validation passed; Bash syntax checks for the queued launcher passed. These are narrow checks. The preserved third-run report has Go test and race phase counters but an overall failure and missing package details; no successful full Go/Pi suite has been independently verified. No provider call, real E05 exchange, live effect execution, containment proof, G1 durability/destruction proof, or end-to-end TBound demonstration is established.

## Immediate next steps

1. The Oct 3 host inventory remains user-provided; do not infer the full disk chain or restore state from the attached path. The latest queued-run status is below; no new run may be queued.
2. Root independently verified the preserved host `verification.json` and `capture-manifest` hashes. An agent verified all nine guest-file size/hash pairs; root's own file verification is limited to those two files. The source comparison matched six of seven files; the captured setup installer predates the corrected host installer, so there is no full-tree match.
3. The preserved third-run report is under `F:\TBoundAssets\Evidence\offline-20261003-131833`; runner and launcher exited `1`. The report names an unexpected-skip/failed-packages condition but omits package results, and captured Go stderr was not preserved, so the underlying error remains unknown.
4. The user has reconnected to `Default Switch` for trusted maintenance; fresh pinned-key SSH found no active guest test processes. The bounded Go-phase diagnostics patch is under independent review; after review, commit it, apply it to the guest with old-file guards and backups, then run one original-baseline offline verifier attempt with private user `sudo -v` and the guarded host disconnect. Do not auto-requeue or restore; diagnose the exact Go error first. No successful full Go/Pi suite is independently verified.

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

### Queue procedure for a later run

The Oct 3 run is already queued; do not execute this command for that run or issue a retry. It is a procedure for a later run only, after the current run has terminally completed and its evidence has been reviewed. Before any later queue, verify the installed launcher hash, mode, and owner against the committed file and obtain fresh private `sudo -v` authorization in the attached pane.

~~~bash
sudo -v
bash "$HOME/tbound-handoff-9a73af56e8a8/tbound/infra/guest/run-offline-queued.sh" --host-adapter-disconnected
~~~

The launcher creates a new private report directory and waits up to ten minutes for every non-loopback carrier to be zero and both IP route tables to have no default route. Do not queue a second launcher. Watch this same pane for a terminal status and exit code. Keep the VM network disconnected through completion and only then reconnect for trusted evidence collection.

Before disconnecting, elevated PowerShell must confirm the VM is Running, F: has at least 700 GiB free, and exactly one adapter is connected to `Default Switch`. The guarded disconnect form used previously is:

~~~powershell
& { $ErrorActionPreference='Stop'; $n='TBound-Ubuntu-2404'; if ((Get-PSDrive -Name F).Free -lt 700GB) { throw 'F: free space is below 700 GiB.' }; $a=@(Get-VMNetworkAdapter -VMName $n); if ($a.Count -ne 1 -or $a[0].SwitchName -ne 'Default Switch') { throw 'Unexpected adapter configuration.' }; Disconnect-VMNetworkAdapter -VMName $n -Name $a[0].Name; $a=@(Get-VMNetworkAdapter -VMName $n); if ($a.Count -ne 1 -or $a[0].SwitchName) { throw 'Network disconnect verification failed.' }; $a | Format-List Name,SwitchName }
~~~

Do not use that disconnect command until a new run is actually queued and waiting. Reconnect only after the guest pane shows terminal completion and the launcher has returned; verify no runner/launcher remains. Then collect the report and stderr over pinned-key SSH for trusted maintenance. If inspection shows the VM is Running and the adapter is disconnected, this guarded one-line command reconnects it for trusted maintenance:

~~~powershell
& { $ErrorActionPreference='Stop'; $n='TBound-Ubuntu-2404'; if ([string](Get-VM -Name $n).State -ne 'Running') { throw 'VM must be running.' }; if ((Get-PSDrive -Name F).Free -lt 700GB) { throw 'F: free space is below 700 GiB.' }; $a=@(Get-VMNetworkAdapter -VMName $n); if ($a.Count -ne 1 -or $a[0].SwitchName) { throw 'Expected exactly one disconnected adapter.' }; Connect-VMNetworkAdapter -VMName $n -Name $a[0].Name -SwitchName 'Default Switch'; Get-VMNetworkAdapter -VMName $n | Format-List Name,SwitchName }
~~~

If the adapter is already connected, do not run that command. After maintenance, shut down cleanly, verify the adapter disconnected and the VM Off, and run the complete host Verify gate before any subsequent offline trial.

## Important limitations at handoff

- The VM is provisioned and has passed prior host configuration checks, but its checkpoint restore is untested. User-provided Oct 3 inventory reports it Running on `Default Switch`; root has not independently checked the host or full disk chain.
- Offline network-gate evidence exists for three failed preflight runs; no full Go/Pi tests completed.
- Provider-only network enforcement, provider request capture, a real model exchange, live effect execution, verified Pi closure, full containment, durable-effect fault injection, G1 fixtures, and the narrow end-to-end todo demonstration remain unfinished.
- A regular-filesystem policy has been agreed but not implemented in the generation store.
- Linux-host migration instructions exist only as a stated future task. Fresh physical Linux setup is untested.
- Keep claims aligned with these limits.
