# TBound handoff state

Last updated: 2026-10-04 (Europe/Vienna)

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
- Repository HEAD before this documentation update: `9e1855fecea598d5d38539246e44221244d37710`. The new OpenRouter and workspace packages remain uncompiled and untested.
- This evidence reconciliation updates `TASK_STATE.md`, `docs/implementation-status.md`, and `infra/hyperv/INSTALLATION-RECORD.md`. The README remains unchanged because its architecture-scaffold status is still accurate. The preexisting untracked `vm_start.txt` is unrelated; do not read, stage, alter, or delete it.
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
- `2644a9f` record the preserved Oct 3 offline Go test failure evidence.
- `833915a` retain bounded Go-phase failure diagnostics in the verifier.
- `7c91b47` fix the unused local in the audit duplicate-effect check; guest deployment and the later test result are recorded below.
- `1f03c76` add the follow-on Linux audit symlink-classification fix; it is not applied to the guest or tested.
- `9e1855f` record the applied guest source overlay.

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

## Prior offline verifier evidence through 19:59 UTC

The first two queued runs failed before their Go/Pi suites. The Oct 3 afternoon run passed the offline gate and produced an incomplete Go test summary. The 19:25 UTC run passed the gate but failed compilation; the later 19:59 UTC run compiled and reached the audit tests, where one symlink-classification assertion failed. Its evidence is pinned below. No successful full Go/Pi suite is established.

1. Earlier report `~/tbound-offline-verify.json`: failed because the sanitized Go check omitted system `PATH`, so cgo reported disabled. Host-side diagnosis verified adding `PATH=/usr/bin:/bin` fixed the cgo reading. Commit `c8bdaa2` updated the verifier.
2. First queued run, private guest directory `~/.tbound-offline-queued.234fHWnyYm`: failed because the manifest parser treated held-but-installed `gcc-13-x86-64-linux-gnu` (`dpkg` status `hi `) as missing. A targeted pure-stdlib parser regression passed; updated verifier was deployed to the guest.
3. Second queued run, private guest directory `~/.tbound-offline-queued.2xrGRICwtY`: queued `2026-10-02T18:21:33Z`, offline gate passed `18:26:38Z`, completed `18:26:39Z` with runner and launcher exit 1. JSON SHA-256 was `72927f8e5f160af51c66f60cb15fa12a89dcd10acb2aa39acdcf00ab1548b6ae`. It failed at `go_modules`: “offline Go module graph resolution failed; provision go.mod dependencies first.” It recognized Go 1.27.1 and held GCC 13.3.0. Before dependency preparation, during trusted Default Switch maintenance, a read-only metadata-only `go list -m all` ran as UID `1000` from a private fresh staging directory with safe `HOME`/`GOCACHE`, the actual `GOMODCACHE`, and the same downloads-disabled verifier environment (`GOPROXY=off`, `GOSUMDB=off`, and related settings). It exited `1`; stderr consisted of six repetitions of `go: module lookup disabled by GOPROXY=off` and named no modules. This confirms the actual cache cannot resolve the graph with downloads disabled, but does not identify the blocking lookup. A separate module-cache inventory found .mod metadata, but did not verify full module archives, for six transitive requirements: testify v1.7.0, go-spew v1.1.0, go-difflib v1.0.0, objx v0.1.0, yaml.v3 v3.0.0-20200313102051-9f266ea9e77c, and check.v1 v0.0.0-20161208181325-20d25e280405. This does not establish which lookup caused the failure. The host repository go.sum exists with two JCS checksum lines. The guest metadata baseline go.sum had eight lines: those two JCS lines plus six transitive go.mod hashes, so it differs from the host file. go.mod declares module tbound/supervisor, Go 1.21, and jcs v1.0.1. An earlier wrong-directory invocation returned `go.mod not found`; it was corrected and is separate from the recorded graph failure. The earlier diagnostic staging was cleaned; no provider call, Go application, or test suite ran.
4. Root independently read the preserved host `verification.json` for the Oct 2 second run; SHA-256 `72927f8e5f160af51c66f60cb15fa12a89dcd10acb2aa39acdcf00ab1548b6ae` matches the guest report. This verifies only that JSON file; the full seven-file evidence folder has not been independently compared.
5. Third queued run (2026-10-03): the user refreshed `sudo -v`; the assistant queued one launcher over SSH at 13:09:52 UTC. Root read `WAITING_FOR_OFFLINE` at 13:13:05 UTC; launcher PID `9019` had a sleep child and Bash parent PID `1949`. The user confirmed the guarded NIC disconnect, PowerShell showed blank `SwitchName`, and the offline gate passed at 13:18:33 UTC. The verifier completed at `2026-10-03T13:19:19+00:00` with runner and launcher exit code `1`, status `FAIL`.
6. Preserved evidence is in `F:\TBoundAssets\Evidence\offline-20261003-131833`. Root independently verified `verification.json` SHA-256 `5c5fa36fea3d91c36f70c32100f4f1cfaca2f86fe6ec1169a452ddbb8baa8350` and `capture-manifest` SHA-256 `48170022e98ed0c15216074e8b8f276b3f83389f81e7273c555c016062397878`. An agent verified size/hash pairs for all nine captured guest files; root's independent file verification is limited to those two named files. The report records `go_modules`, `adapter_dependencies`, and `node_adapter` PASS; Pi `0.87.1`, `provider_stream_attempts=0`; `go_tests` and `go_race` counters of 62 passed, 0 failed, 0 skipped; an overall exit 1 and `unexpected_skips`/failed-packages condition naming expected `TestOpenRejectsUntrustedOwnership`. The summary omitted `package_results`; do not treat these counters as a successful full suite or infer the underlying Go error from the unexpected-skip entry. Captured stdout/stderr were only in memory and scratch cleanup deleted them; saved `verification.stderr` and `sudo.stderr` are empty. The exact Go error is unknown. The report records ownership audit package-fail `0`, run/pass `0/0`, noninteractive sudo true, cleanup and global cleanup PASS, offline carrier `0` and no default routes with host-disconnect attestation true, and all four claims false. The reported source comparison matched six of seven files; the captured setup installer came from an earlier source base than the corrected host installer, so this is not a full-tree match.
6. Preserved evidence is in `F:\TBoundAssets\Evidence\offline-20261003-131833`. Root independently verified `verification.json` SHA-256 `5c5fa36fea3d91c36f70c32100f4f1cfaca2f86fe6ec1169a452ddbb8baa8350` and `capture-manifest` SHA-256 `48170022e98ed0c15216074e8b8f276b3f83389f81e7273c555c016062397878`. An agent verified size/hash pairs for all nine captured guest files; root's independent file verification is limited to those two named files. The report records `go_modules`, `adapter_dependencies`, and `node_adapter` PASS; Pi `0.87.1`, `provider_stream_attempts=0`; `go_tests` and `go_race` counters of 62 passed, 0 failed, 0 skipped; an overall exit 1 and `unexpected_skips`/failed-packages condition naming expected `TestOpenRejectsUntrustedOwnership`. The summary omitted `package_results`; do not treat these counters as a successful full suite or infer the underlying Go error from the unexpected-skip entry. Captured stdout/stderr were only in memory and scratch cleanup deleted them; saved `verification.stderr` and `sudo.stderr` are empty. The exact Go error is unknown. The report records ownership audit package-fail `0`, run/pass `0/0`, noninteractive sudo true, cleanup and global cleanup PASS, offline carrier `0` and no default routes with host-disconnect attestation true, and all four claims false. The reported source comparison matched six of seven files; the captured setup installer came from an earlier source base than the corrected host installer, so this is not a full-tree match.
7. An intervening Oct 3 launcher attempt in `~/.tbound-offline-queued.KVSVFHQErF` ended with `SUDO_AUTHORIZATION_MISSING` and exit `1` before the verifier started; it did not reach the offline gate or run tests. A local expected-hash transcription omitted the first character, but the corrected full guest verifier hash matched; no verifier change was made for that comparison.
8. Later queued run `~/.tbound-offline-queued.LPvHct5VqZ`: the offline gate passed at `2026-10-03T19:25:35Z`; the terminal reported completion at `19:26:17Z` with exit `1`. The captured report records `go_tests`, `go_race`, and `privileged_ownership_test` failing at compile time with `internal/audit/audit.go:248:5: declared and not used: state`. The ownership test itself did not run; do not weaken its expected-test gate. Root independently verified `verification.json` SHA-256 `f536204b261f7ca4f099082fd7758337906f9fdc12b12b834cb0511136b75761` and read its failure details. The capture agent verified `capturemanifest.json` SHA-256 `68b6502667ef37e2ab101793d14a1f3ca2674fdd95af963890070d4b2de36d01` and matched all nine captured source/local file size and hash pairs (123,503 bytes total) under `F:\TBoundAssets\Evidence\offline-20261003-192535`. The JSON records status `FAIL`, all four claims false, and `sudo_noninteractive=true` but ownership audit run/pass `0/0`; the test functions did not run because compilation failed. Saved `verification.stderr` and `sudo.stderr` are empty; the compiler error is recorded in the report. A metadata-only host inventory recorded 29,043,137,045 tracked bytes across 60 files and 869,740,834,816 free bytes on F:; both the 100-GiB tracked-file and 700-GiB free-space limits passed at that measurement.
9. Latest queued run `~/.tbound-offline-queued.vlH6WMLbws`: the offline gate passed at `2026-10-03T19:59:47Z`; the verifier completed at `20:00:34Z` with runner and launcher exit `1`. `go_tests` and `go_race` failed in `TestOpenRejectsBroadPermissionsAndLeafSymlink` (`audit_test.go:431`): expected `ErrJournalSymlink`, got `open audit journal: open audit path directory "parent-link": not a directory`. The privileged ownership phase passed, run/pass `1/1`. Overall status is `FAIL`; all four claims are false. Root verified the report hash and failure details; the capture agent verified the manifest and all nine guest-file size/hash pairs. Preserved evidence is `F:\TBoundAssets\Evidence\offline-20261003-195947`: `verification.json` SHA-256 `095af79e04c4b1587e890fd66c2b4f2d5fdf0440f33cc59d9272fcc6b05e5890`, `capturemanifest.json` SHA-256 `29fc15b13a4f542578e09cb9bfcbd53b0f8d66ab73a79204e46b01d189287281`, and nine regular evidence files totaling 124,538 bytes. The earlier `LPvHct5VqZ` compile failure is a separate historical run. Post-capture metadata-only inventory measured 4,081,694,378 bytes across 62 `TBoundAssets` files and 25,028,681,596 bytes across 8 `TBoundVMs` files, total 29,110,375,974 bytes; there were no reparse points, and F: had 869,672,910,848 bytes free. Both the 100-GiB tracked-data and 700-GiB free-space limits passed.

Before the Oct 3 dependency apply, the guest verifier SHA-256 was `9665d4004585515bfd90e7774cb4a8cbf54b972178bea4f580e4bd20f7d46d18` and guest `supervisor/go.sum` SHA-256 was `7913bff96ace35f736cabee75fb7b469f97976bb09804ad91a4794f705fe8c21`. The prior queue launcher was recorded as SHA-256 `12bc7d88bcbff75e9848b42ee46401c9014a594d73ded62fc623573650f41497`; its current hash, mode, and owner must be rechecked before reuse. Preserve all failure reports and backups; never overwrite/retry an unknown report directory.

### Latest guest application and pane state - 2026-10-03

- Host commit `711e6416424766e8aa7e0d7ff8beac4dcb171002` committed the prepared `supervisor/go.sum` and README; it did not change `go.mod` or application source.
- An atomic guest apply guarded by exact old-file hashes installed `supervisor/go.sum` SHA-256 `2ff7956b129800bfed3b5f9ca671ece52317fc4fa4786c76f4672abb075f1aea` and `infra/guest/verify-offline.py` SHA-256 `bbcefad25a5184bb87b2efaba3b73b2480be609f6daff30a376aed9d184b2272`; both are mode `0600`, UID `1000`. Guest `go.mod` remained byte-identical, SHA-256 `72a77c58ae54b3c6e4a76efe96f0ad4313c03693809d85e2d3304ca3adad0713`.
- The private rollback directory `/home/tboundadmin/.tbound-maintenance-backup-2Y2Yh0` is UID `1000`, mode `0700`, and retains the old sum and verifier hashes above. The apply log SHA-256 is `60799d933f6cfa77baf306f93d7f29d4e823655434d7408941c332d285e4e097`.
- `tbound-offline` window 0, pane 0, terminal `pts/1`, Bash PID `1949`, attached client tty `1`, and `FIONREAD=0` were observed before the latest queue. At that point, no Go, npm, Node, launcher, or verifier process was active.
- Metadata-only `go list -m all` and `go mod verify` passed while the NIC was connected; they are not an offline verifier pass. After the `LPvHct5VqZ` run, the user confirmed reconnection to `Default Switch` for trusted maintenance, and no active guest test job remained at that observation. The guest application remains the earlier `9a73` tree with the separately recorded dependency-sum update, diagnostics verifier, and audit source patch applied; the new OpenRouter and workspace packages have not been deployed.
- Host commit `833915a` retains bounded Go-phase failure diagnostics. The diagnostic verifier was applied to the guest as SHA-256 `a0cabda405c58e5bcfe8453eaa0e892579dbe51ee81d0f9d8add45d4d5476de5`. Host-only Python and Go module-helper tests passed 5/5 each, with AST/compile checks; no Go build or tests for the new broker/workspace packages or provider requests occurred.
- Host commit `7c91b47cf54c71cfd6e24b9107329bc2ba2095a6` fixed the unused `state` local in `supervisor/internal/audit/audit.go`. The guest file matched SHA-256 `3a6f97639aa9b2d38f21de8f274867c7c3f97dc430624dbfc88fdce22e761c82`, UID `1000`, mode `0600`, link count `1`. Its prior version (SHA-256 prefix `0362265`) is backed up at `/home/tboundadmin/.tbound-audit-fix-tp2e7cqj/audit.go.before`; `patch-lineage.json` SHA-256 is `9258a3ebbe54ed9707c6d2e40a24331862d0347044b6fcabfe293d9738e7cf1e`. The later `vlH6WMLbws` run compiled and exposed the symlink-classification test failure recorded above. No claim is made that a follow-on symlink fix has been deployed.

Targeted checks already reported: APT trust-guard regression suite (7 tests) passed; one held-package parser regression passed; guest Python AST validation passed; queued-launcher Bash syntax checks passed; and the host Go-phase diagnostic helper tests passed 5/5 with Python tests 5/5. These are component checks. The Oct 3 afternoon Go report has incomplete test summaries; the 19:25 run failed compilation, and the 19:59 run compiled but failed the audit symlink-classification assertion. No successful full Go/Pi suite is independently verified. No provider call, real E05 exchange, live effect execution, containment proof, G1 durability/destruction proof, or end-to-end TBound demonstration is established.

## Immediate next steps recorded before the latest diagnostic run

1. The latest preserved run is `vlH6WMLbws` at `F:\TBoundAssets\Evidence\offline-20261003-195947`; its report/manifest pins and nine-file capture verification are recorded above. The 19:25 compile failure is a separate historical run.
2. The latest run verified that the `7c91b47` audit.go compile fix reached Go tests, where normal and race phases failed at the symlink-classification assertion. At that point, host commit `1f03c76` contained the reviewed follow-on fix but had not been deployed or tested. Do not relax the expected-test gate, queue automatically, or restore.
3. The user approved transfer of the selected source bundle at `05245378a3df82fbf2cb413aa1ac59085dfc0bf6`; the queue agent reports eight selected files applied and hash-verified under the guest handoff tree, not the full HEAD. Archive SHA-256 is `afc1610495ec34c754460351cd1e73e1fb8f351913d5e1762eed4feb1a2fb1f1`; deployment-manifest SHA-256 is `a792425bcc8916b62bc42b46a6403f8450d7a441170e50be737806f7e1fcf1ef`. The five preimage backups are preserved. Per-file hashes and paths are in `docs/implementation-status.md`. The static root-owned fixture bootstrap is still being prepared; it has not been installed or run, and no post-transfer fixture test or offline verification exists. The next verification must wait for the reviewed bootstrap and use the updated launcher as `tboundadmin` without per-run `sudo`.
4. The post-capture F: inventory passed the 100-GiB tracked-data and 700-GiB free-space limits; older host inventory values remain historical. No successful full Go/Pi suite, provider call, real E05 exchange, containment proof, G1 durability/destruction proof, or end-to-end demonstration is established.

## Current status — diagnostic run and follow-up

The latest queued run `q0swUUj1Kl` passed the offline gate at `2026-10-03T21:41:28Z` and completed at `21:42:09Z` with runner/launcher exit 1. Overall verifier status is FAIL solely on `failed_packages` in both modes; all four claims are false.

Both normal and race Go phases report 5/5 packages, 86 test-run events, 85 passes, 0 failures, and one expected test-level skip for `TestOpenRejectsUntrustedOwnership`. The correlation package has a separate package-level skip with the exact `[no test files]` marker; the current parser flags that marker as failure.

The dedicated `ownership_fixture_test` passed without per-run sudo: UID/GID 1000, one named test/run/pass, zero failures or skips. A static root-owned fixture bootstrap was installed once; no persistent NOPASSWD sudoers entry was added.

The report records UID 1000, offline `eth0` carrier 0, no IPv4/IPv6 default route, cleanup PASS, and all claims false. Saved `verification.stderr` is empty. Root verified report SHA-256 `1fd2bec7cfb4d3cabf0031f163c6e323418914db5190d77b962e5469a45da561` (123,858 bytes), manifest SHA-256 `58bc33702a40464aa7ddc424e96add1941c327ade19e0209df3344562b8d9934`, and the capture confirmation matching all eight captured guest files at `F:\TBoundAssets\Evidence\offline-20261003-214128`.

The host parser correction was not deployed to or tested in this guest run. Passing targeted Go and fixture phases do not establish a successful overall verifier, provider exchange, end-to-end demonstration, or containment.

After this run, source commit `90ee23e` corrected the package-skip parser and passed seven host-only pure tests. A guarded two-file guest deployment was verified as UID 1000 without sudo: `infra/guest/verify-offline.py` SHA-256 `cb4d8d2a235548b2a3a1528befb880e0dc5f1619d5ac46591672ed17547b204a` and `infra/guest/test_go_phase_diagnostic.py` SHA-256 `61a56ea541e5ed5648b02b79643a2b6a667144a99b2129b77ea7622dc1e6099f`. The preimages are preserved under `/home/tboundadmin/.tbound-verifier-backup-90ee23e34b60f4e2`; deployment manifest SHA-256 is `07c791f66507a3125ca73ed96696c50853c5509422f07f32aa8e870693e90af4`. A follow-up queue was then run; its terminal reported successful completion, but the report and capture remain unavailable for independent verification.

The queued follow-up is `/home/tboundadmin/.tbound-offline-queued.VFHmYRa5HO`, created at `2026-10-04T09:03:53Z`. The offline gate passed at `2026-10-04T09:06:39Z`; the terminal reported successful completion at `09:07:19Z`. This is a provisional terminal result only: the report JSON and capture have not been retrieved or independently verified, so do not claim verifier PASS, full checklist completion, or containment.

Host automation source remains independently security-reviewed. The retry reached the installer’s firmware gate but rejected the valid Ubuntu entry because BootType is the native enum Microsoft.HyperV.PowerShell.VMBootSourceType, while the guard required a string. The guard now accepts only string File or the exact native enum value File, along with the pinned VM identity, null Device, empty checkpoint IDs/names, IsDeleted false, and the structural GPT path to \EFI\ubuntu\shimx64.efi. The installer captures that exact FirmwarePath in protected profile schema 2; runtime requires the same first-entry path. Windows PowerShell 5.1 parsing passed for four scripts and 45 pure fixtures passed using the real Hyper-V enum metadata; canonical LF action SHA-256 and installer pin are 9EF7B338B18DA61204F84E3B74FD1ADA609652E4FC7A25DEB5CF633605581D26. The enum correction is prepared but not installed or live-tested; the failed attempt stopped before persistent changes. The one-time elevated retry remains pending, followed by Inspect, safe quiescence, and VFH report/capture verification.

A focused firmware guard fix now accepts only the target VM’s File entry with null Device, matching VM identity, empty checkpoint IDs/names, IsDeleted false, and a local GPT path to \EFI\ubuntu\shimx64.efi. The installer captures that validated FirmwarePath in protected profile schema 2; runtime requires an exact match to the pinned path at boot-order position one. The partition GUID and geometry are validated structurally and captured per installation. Secure Boot’s Linux template and the single attached disk remain separate existing checks. Windows PowerShell 5.1 parsed the three source scripts and the pure fixture harness passed 36 accept/reject cases; canonical LF action SHA-256 and installer pin are 3740571565DBDA14A3517D9A891FAA98E9DFEF80D22A9BBC67F9389575CDA716.

The source fix is prepared but not installed or live-tested; this work did not inspect or alter the live VM, firmware, Hyper-V settings, tasks, or SSH state. The one-time elevated installer retry remains pending. After installation, run Inspect and safe quiescence, then retrieve and verify the VFH report JSON and capture. The VFH terminal success remains provisional until those artifacts are verified.

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

The Oct 3 queued runs are complete; do not reissue or retry them. The eight-file source overlay is applied but untested. Do not queue another offline run until the static root-owned fixture bootstrap is prepared, reviewed, and installed once during connected trusted maintenance. The human refreshes `sudo -v` privately in the attached pane for that one-time bootstrap only; the updated launcher and verifier run afterward as `tboundadmin` without per-run `sudo`.

For the later SSH path, run the updated launcher while the guest is connected in trusted maintenance mode. Before disconnecting the host adapter, confirm the terminal prints `queued; waiting...`, get the exact report-directory path for that invocation, and verify its `status` file contains `WAITING_FOR_OFFLINE`. Do not select a report directory by wildcard or queue a second launcher. Keep the adapter disconnected through verifier completion, then reconnect only for trusted evidence collection after confirming the launcher and verifier have exited.

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
- The latest offline-gated run reached Go tests but failed the audit symlink-classification assertion; no full Go/Pi suite has passed. The privileged ownership phase passed 1/1 in that run, which does not establish containment or an overall verifier pass.
- Provider-only network enforcement, provider request capture, a real model exchange, live effect execution, verified Pi closure, full containment, durable-effect fault injection, G1 fixtures, and the narrow end-to-end todo demonstration remain unfinished.
- A regular-filesystem policy has been agreed but not implemented in the generation store.
- Linux-host migration instructions exist only as a stated future task. Fresh physical Linux setup is untested.
- Keep claims aligned with these limits.
