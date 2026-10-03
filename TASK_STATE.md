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
- Last independently inspected HEAD at the start of this refresh: `9bcf88b53a0d33d19622dde466a5915ff56dd354` — `Add Go module failure diagnostics`.
- This handoff refresh updates `TASK_STATE.md` and `infra/hyperv/INSTALLATION-RECORD.md`. The preexisting untracked `vm_start.txt` is unrelated; do not read, stage, alter, or delete it.
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
- `2e342f4` add the TBound task handoff state.
- `980f36d` clarify setup paths and implementation status.
- `9bcf88b` add Go module failure diagnostics.

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

After the second failed run completed on Oct 2, the user confirmed a maintenance reconnection. On Oct 3, user-provided elevated host inventory reported the VM Running, Generation 2, `ProductionOnly`, with automatic checkpoints disabled; one adapter `Netzwerkkarte` on `Default Switch`; one checkpoint; and F: free space of 810.17 GiB. The inventory reported an attached disk path ending in `TBound-Ubuntu-2404_7434EDE1-4C24-46FB-932E-DA4CC74AD396.avhdx`; that path alone does not establish the full disk chain or restore success. Root has not independently queried the host. The Oct 3 console showed `Enhanced Session` grey and the attached tmux session noted above. The user confirmed current guest IP `172.24.148.77/20` (metric 100), and a pinned-key SSH maintenance probe succeeded as `tboundadmin` UID `1000`, hostname `tboundubuntu2404`, kernel `6.8.0-146-generic`. The first process-filter query had quoting errors; the corrected bounded read-only query found no active Go, npm, Node, verifier, or launcher process. The tmux session and an expected system unattended-upgrade/shutdown Python process were present. No verifier was started during the console check; no new offline run is queued. Do not change VM or network state, restore, or queue another run until the live process state and next action are reviewed.

The user once typed a long command in VMConnect and the keyboard produced garbled symbols; a reboot restored input. Prefer SSH for trusted maintenance and use VMConnect for observing/reattaching to the existing terminal. The earlier `tmux a` command was mistakenly tried in Windows PowerShell once and then redundantly inside the already attached Ubuntu tmux, producing the nesting warning. Do not run Windows-side `tmux`; do not nest/unset `TMUX`.

## Offline verifier evidence and what it means

All queued runs below reached the guest-offline gate. They did **not** reach the Go/Pi test suites. Each failure had `checks={}`, no suites, cleanup PASS with no leftover paths, and all claims false.

1. Earlier report `~/tbound-offline-verify.json`: failed because the sanitized Go check omitted system `PATH`, so cgo reported disabled. Host-side diagnosis verified adding `PATH=/usr/bin:/bin` fixed the cgo reading. Commit `c8bdaa2` updated the verifier.
2. First queued run, private guest directory `~/.tbound-offline-queued.234fHWnyYm`: failed because the manifest parser treated held-but-installed `gcc-13-x86-64-linux-gnu` (`dpkg` status `hi `) as missing. A targeted pure-stdlib parser regression passed; updated verifier was deployed to the guest.
3. Second queued run, private guest directory `~/.tbound-offline-queued.2xrGRICwtY`: queued `2026-10-02T18:21:33Z`, offline gate passed `18:26:38Z`, completed `18:26:39Z` with runner and launcher exit 1. JSON SHA-256 was `72927f8e5f160af51c66f60cb15fa12a89dcd10acb2aa39acdcf00ab1548b6ae`. It failed at `go_modules`: “offline Go module graph resolution failed; provision go.mod dependencies first.” It recognized Go 1.27.1 and held GCC 13.3.0. Before dependency preparation, during trusted Default Switch maintenance, a read-only metadata-only `go list -m all` ran as UID `1000` from a private fresh staging directory with safe `HOME`/`GOCACHE`, the actual `GOMODCACHE`, and the same downloads-disabled verifier environment (`GOPROXY=off`, `GOSUMDB=off`, and related settings). It exited `1`; stderr consisted of six repetitions of `go: module lookup disabled by GOPROXY=off` and named no modules. This confirms the actual cache cannot resolve the graph with downloads disabled, but does not identify the blocking lookup. A separate module-cache inventory found .mod metadata, but did not verify full module archives, for six transitive requirements: testify v1.7.0, go-spew v1.1.0, go-difflib v1.0.0, objx v0.1.0, yaml.v3 v3.0.0-20200313102051-9f266ea9e77c, and check.v1 v0.0.0-20161208181325-20d25e280405. This does not establish which lookup caused the failure. The host repository go.sum exists with two JCS checksum lines. The guest metadata baseline go.sum had eight lines: those two JCS lines plus six transitive go.mod hashes, so it differs from the host file. go.mod declares module tbound/supervisor, Go 1.21, and jcs v1.0.1. An earlier wrong-directory invocation returned `go.mod not found`; it was corrected and is separate from the recorded graph failure. The earlier diagnostic staging was cleaned; no provider call, Go application, or test suite ran.
4. On Oct 3, root independently read preserved host file `F:\TBoundAssets\Evidence\offline-20261002-182638\verification.json`; its SHA-256 `72927f8e5f160af51c66f60cb15fa12a89dcd10acb2aa39acdcf00ab1548b6ae` matches the guest report. This verifies that JSON file only. A mechanics agent reported a full seven-file copy match, but root has not independently compared the full folder; keep that claim pending.

The latest guest verifier SHA-256 was `9665d4004585515bfd90e7774cb4a8cbf54b972178bea4f580e4bd20f7d46d18`. The queued launcher SHA-256 was `12bc7d88bcbff75e9848b42ee46401c9014a594d73ded62fc623573650f41497`. The first maintenance backup contains the original pre-PATH-fix verifier; a later backup contains the pre-held-package-parser verifier. Preserve all failure reports and backups; never overwrite/retry an unknown report directory.

Targeted checks already reported: APT trust-guard regression suite (7 tests) passed; one pure-stdlib held-package parser regression passed; guest Python AST validation passed; Bash syntax checks for the queued launcher passed. These are narrow checks. There is no successful full offline verification, Go normal/race suite, Pi suite, provider call, real E05 exchange, live effect execution, containment proof, G1 durability/destruction proof, or end-to-end TBound demonstration.

## Immediate next steps

1. Use read-only checks. The Oct 3 host inventory is user-provided, not an independent root query; do not infer the full disk chain or restore state from the attached disk path. The console confirms Enhanced Session grey and an attached `tbound-offline` session. The current IP is `172.24.148.77/20` (metric 100), and pinned-key SSH authenticated as `tboundadmin` UID `1000` with the expected hostname and kernel. The corrected read-only process query found no active Go/npm/node/verifier/launcher process; no new verifier was started or queued. Do not start a verifier or change VM/NIC state from this handoff task.
2. The second queued run's preserved host `verification.json` was independently read by root and its SHA-256 matches the guest report. This does not verify the complete seven-file evidence folder; only the individual JSON copy is independently confirmed.
3. Diagnose the Go module graph failure before queuing another run. The guest verifier that produced the second-run report discarded the underlying Go module-list stderr, so the exact missing dependency/cache cause remains unresolved. A previous observability patch attempt stopped with transport errors before changing code. An earlier SSH diagnostic was not executed because automatic approval review ended with a usage failure; this was not a safety rejection, and no bypass was attempted. A separate pinned-key SSH maintenance probe has since succeeded. Commit `9bcf88b53a0d33d19622dde466a5915ff56dd354` adds bounded sanitized diagnostics: fixed command and exit code plus at most 2048 UTF-8 bytes of stderr, URL-userinfo redaction, and a truncation marker while preserving the existing failure gate. Five host-only pure tests, host-side Python AST validation, and diff checks passed. The prepared guest-transfer artifact hash is `bbcefad25a5184bb87b2efaba3b73b2480be609f6daff30a376aed9d184b2272` (LF, no BOM); it has not been deployed. The Oct 3 metadata-only diagnostic, summarized under offline verifier evidence, confirmed the graph failure but named no modules. The separate cache inventory found .mod metadata for the six transitive requirements listed above, but did not verify full archives or identify the failed lookup. The host go.sum has two JCS checksum lines; the guest baseline has eight lines as summarized above. Do not claim a guest fix or full verification. The verifier’s Go environment (see `infra/guest/verify-offline.py`) sets a pinned Go binary, `GOMODCACHE=$HOME/go/pkg/mod`, `GOPROXY=off`, `GOSUMDB=off`, `GOWORK=off`, `GOFLAGS=`, `GOENV=off`, `GOTOOLCHAIN=local`, `CGO_ENABLED=1`, system PATH, and a temporary scratch cache. It stages the supervisor source then runs `go list -m all`, `go mod verify`, and package enumeration. The Oct 3 metadata-only module-list diagnostic has run; see the result above. A separate cache inventory found the six transitive requirements listed above, but it did not establish which lookup caused the failure. Online preparation succeeded in a private fresh stage during trusted Default Switch maintenance: `go mod download all` exited 0 with empty stdout and stderr using the official proxy and sumdb. `go.mod` was byte-identical. Starting from the guest eight-line baseline, the stage `go.sum` gained six module content hashes for the six approved versions, yielding 14 lines with no removals; compared with the host `go.sum` containing two JCS lines, the stage has 12 additional lines. This populated the shared actual guest GOMODCACHE; source-tree go.mod and go.sum remained unchanged, while the candidate go.sum update stayed in the private preparation stage. Afterward, metadata-only `go list -m all` with `GOPROXY=off`/`GOSUMDB=off` exited 0, printed eight lines (main module plus seven graph modules), and had empty stderr; `go mod verify` exited 0 and reported all modules verified. `go.mod` stayed unchanged, and staged `go.sum` was stable across those checks. The VM NIC remained connected to Default Switch during this maintenance; these download-disabled Go commands do not establish an actual VM-offline run. An outer wrapper initially exited 1 because its audit guard compared the updated `go.sum` with the original eight-line baseline; both Go checks succeeded. The corrected strict final-entry audit now passes: the final `go.sum` has 14 unique entries and covers only the seven approved module versions. The preparation artifact is preserved at `F:\TBoundAssets\DependencyPreparation\2026-10-03-go-mod-download-all-TsNRSa`; candidate `go.sum` SHA-256 is `2ff7956b129800bfed3b5f9ca671ece52317fc4fa4786c76f4672abb075f1aea`, and staged supervisor `go.mod` SHA-256 is `72a77c58ae54b3c6e4a76efe96f0ad4313c03693809d85e2d3304ca3adad0713`. Artifact transfer remains pending; no source overwrite or deployment occurred. No Go application, full test suite, source overwrite, or new queued offline verifier run occurred.

From the existing Ubuntu checkout, a read-only diagnostic that closely matches the verifier module-list environment is:

~~~bash
cd "$HOME/tbound-handoff-9a73af56e8a8/tbound/supervisor"
env -i HOME="$HOME" PATH="/opt/tbound/toolchains/go1.27.1/bin:/usr/bin:/bin" GOTOOLCHAIN=local GOENV=off GOPROXY=off GOSUMDB=off GOMODCACHE="$HOME/go/pkg/mod" GOWORK=off GOFLAGS= CGO_ENABLED=1 /opt/tbound/toolchains/go1.27.1/bin/go list -mod=readonly -m all
~~~

The Oct 3 diagnostic exited `1` with six repetitions of `go: module lookup disabled by GOPROXY=off` and named no modules. If an authorized reproduction is needed, capture stdout, stderr, and exit code; keep `-mod=readonly` so go.mod/go.sum remain unchanged, and compare command/environment differences before changing source or provisioning.
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

- The VM is provisioned and has passed prior host configuration checks, but its checkpoint restore is untested. User-provided Oct 3 inventory reports it Running on `Default Switch`; root has not independently checked the host or full disk chain.
- Offline network-gate evidence exists for three failed preflight runs; no full Go/Pi tests completed.
- Provider-only network enforcement, provider request capture, a real model exchange, live effect execution, verified Pi closure, full containment, durable-effect fault injection, G1 fixtures, and the narrow end-to-end todo demonstration remain unfinished.
- A regular-filesystem policy has been agreed but not implemented in the generation store.
- Linux-host migration instructions exist only as a stated future task. Fresh physical Linux setup is untested.
- Keep claims aligned with these limits.
