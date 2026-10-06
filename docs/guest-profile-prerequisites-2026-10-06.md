# Guest/profile prerequisite recheck — 2026-10-06

**Outcome: current-source guest offline verification remains blocked; frozen
containment remains `not-established`, G1=false.** This is a host-only,
read-only prerequisite/source review, not a controller admission or guest run.

## Scope and provenance

- Initial HEAD: `cb63b110eb1dbc9cebb9d2964e736399dec2dd76` (requested baseline
  `cb63b11`). Observation window: 2026-10-06; sampled host clock read
  `2026-10-06T17:35:07.7679073+00:00`.
- Sole owned change: this new report. No commits, staging, implementation edits,
  old blocker-document edits, or TaskState reads/edits. Pre-existing modified
  infrastructure/user documents and untracked helper/controller sources were
  preserved, not adopted into a commit. The excluded VM-start file was not read.
- No elevation, installation, VM query/transition, task trigger, controller
  invocation (including `-PolicyOnly`), queue, network/provider operation, SSH,
  SCP, ssh-keygen, private-key read, or guest capability probe occurred.
- Source-only references: `docs/verifier-blockers-2026-10-05.md`,
  `docs/containment-blockers-2026-10-05.md`,
  `infra/hyperv/INSTALLATION-RECORD.md`, the existing host installer/bootstrap,
  initializer, access policy, controller and policy-test sources; guest
  launcher/verifier/provisioning/export sources; `supervisor/internal/podman`,
  `supervisor/internal/sandbox`, and `supervisor/internal/executor`.
- Current thesis references are under
  `../MSE_MA_Thesis/agentic-harness/AH-technical/`: sandboxing decision frozen
  profile, language decision pre-exec/crash-safe contracts, runtime flow §§1/6/9,
  process §3.1, recovery §4.1, and architecture containment section. The README's
  sibling `../agentic-harness` path is absent here; it was not substituted as an
  authoritative source. `docs/experiment-manifest.md` is explicitly candidate,
  not a freeze or current guest inventory.

## Current host metadata: commands and results

All metadata wrappers completed with shell exit **0** because query errors were
caught and recorded. This does **not** make missing prerequisites PASS.

Identity query (Windows PowerShell/.NET read-only):

```powershell
$id = [Security.Principal.WindowsIdentity]::GetCurrent()
$p = [Security.Principal.WindowsPrincipal]::new($id)
$id.Name
$id.User.Value
$p.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
$PSVersionTable.PSVersion.ToString()
[Environment]::Is64BitProcess
```

| Field | Observed |
| --- | --- |
| Identity | `FABIAN\Admin` |
| SID | `S-1-5-21-2350865082-651554413-1548572510-1001` |
| Administrator role enabled | `false` (unelevated token) |
| PowerShell | `5.1.19041.6456` |
| 64-bit process | `true` |
| Controller-required SID, **source requirement only** | `S-1-5-21-2350865082-651554413-1548572510-1004` (`FABIAN\CodexSandboxOffline`) |

Independent `whoami.exe /groups /fo csv /nh` returned native exit **0**:
Administrators SID `S-1-5-32-544` was **deny-only** and integrity label
`S-1-16-8192` was **medium**, corroborating the unelevated context.

Task Scheduler COM read-only query:

```powershell
$s = New-Object -ComObject Schedule.Service
$s.Connect()
$s.GetFolder('\TBound')
# For each exact leaf below, in a separate caught query:
$s.GetFolder('\TBound').GetTask($leaf)
```

The exact folder and all five queries returned **HRESULT `0x80070002`**, system
cannot find the file. An initial `GetFolder('\TBound\')` spelling returned
`0x8007007B` (invalid path syntax), not absence evidence; the corrected COM path
above and independent native queries establish the result below.

| Exact native read-only command | Native exit | Result |
| --- | --- | --- |
| `schtasks.exe /query /tn \TBound\Inspect` | 1 | File not found |
| `schtasks.exe /query /tn \TBound\Disconnect` | 1 | File not found |
| `schtasks.exe /query /tn \TBound\StopOffline` | 1 | File not found |
| `schtasks.exe /query /tn \TBound\ConnectOff` | 1 | File not found |
| `schtasks.exe /query /tn \TBound\StartTrustedMaintenance` | 1 | File not found |

No installed principal/action/settings/SDDL can be read back from absent tasks.
Coordinator independently rechecked the current SID `...-1001`, unelevated token,
absent protected helper, and exact Inspect query native exit 1. No task triggered.
Do not describe this as an installed principal mismatch. Other task namespaces
were neither enumerated nor used as substitutes.

For each following literal path, `Get-Item -LiteralPath $path -Force
-ErrorAction Stop` returned
`PathNotFound,Microsoft.PowerShell.Commands.GetItemCommand`. Conditional
`Get-Acl` was never reached; no key-file path or content was opened.

```text
C:\ProgramData\TBoundHostAutomation
C:\ProgramData\TBoundHostAutomationSource
C:\ProgramData\TBoundHostAutomation\TBoundHostActions.ps1
C:\ProgramData\TBoundHostAutomation\TBoundHostAccessPolicy.psm1
C:\ProgramData\TBoundHostAutomation\Initialize-TBoundOperatorSsh.ps1
C:\ProgramData\TBoundHostAutomation\profile.json
C:\ProgramData\TBoundHostAutomation\receipts
C:\ProgramData\TBoundHostAutomation\operator-ssh
C:\ProgramData\TBoundHostAutomation\operator-public
```

## Source pins and LF export (not deployment evidence)

Read-only Git commands used correctly grouped arguments; no archive was created:

```powershell
$repo = (Get-Location).Path
$pin = '5ccc52b2fa35be3dde9d3839f63ec613627b796b'
git -c ('safe.directory=' + $repo) -c core.autocrlf=false -C $repo cat-file -e ($pin + '^{commit}')
# native exit 0: old reviewed commit exists
git -c ('safe.directory=' + $repo) -c core.autocrlf=false -C $repo diff --quiet $pin -- adapter supervisor infra/guest
# native exit 1: current scoped source differs
```

The controller still pins that old commit at line 21. Existence is not review of
the current source. Its line 407 vector is defective; line 446 export lacks
explicit `core.autocrlf=false`. Updating the commit string alone cannot repair
either defect or establish protected installation, operator access, or a run.

`Get-FileHash -LiteralPath <exact source path> -Algorithm SHA256` returned:

| Source | SHA-256 |
| --- | --- |
| `infra/guest/run-offline-queued.sh` | `F0BF235943EE13B3EE44614D6E172BF8179E3DEADDE882662EA8009C6E711148` |
| `infra/guest/verify-offline.py` | `CB4D8D2A235548B2A3A1528BEFB880E0DC5F1619D5AC46591672ED17547B204A` |
| `infra/hyperv/Install-TBoundHostAutomation.ps1` | `7638C052C1A707FD587C11571034A1AC4A29343090F037B4BED97725E5A6DA01` |
| `infra/hyperv/TBoundHostActions.ps1` (disk bytes) | `BB39E08E890B6DE6333E5B2E94038F09A70AD6CC53EA4E3EF9B3B7840D190400` |
| `infra/hyperv/TBoundHostAccessPolicy.psm1` | `C06BAE160364EB4D88914A3468FBB766C426010145ACA9D2C43DD803A993BB15` |
| `infra/hyperv/Initialize-TBoundOperatorSsh.ps1` | `E521CCB79AD0CF51E7F48A9B027082F5FD92D640697146F6DB0601BFFF596851` |
| `infra/hyperv/Bootstrap-TBoundHostAutomation.ps1` | `0EF86511A6A00F0EB3F285E9CDD6D9703892C9F311B86A427E367130CC335469` |
| `infra/hyperv/Invoke-TBoundTrustedOfflineVerifier.ps1` | `5ECD8488B0E3517952B70DE0E037B130A6DA2CA910432752AA22BD29D198946C` |

The action's in-memory UTF-8/no-BOM LF normalization returned
`9EF7B338B18DA61204F84E3B74FD1ADA609652E4FC7A25DEB5CF633605581D26`,
matching the installer's normalized pin. Other bundle hashes match the pins in
the reviewed source bootstrap/runbook. These are integrity comparisons against
source constants, not independent authorization or installation attestations.
Launcher/verifier disk hashes still match the controller's existing pins.

Future export must use a separately reviewed committed source identity, explicit
LF configuration, no uncommitted overlays, exact regular-file scope/member
inventory and hash readback, with approved storage/quiescence gates. The
`infra/guest/Export-TBoundGuestBundle.ps1` source already makes LF explicit at
line 361, but its whole-worktree cleanliness/branch/storage requirements cannot
be inferred from a scoped export. It was not executed. Preserve unrelated dirty
work; do not clean it by adoption, deletion, or commit. The Oct 5 `4cbfbf6b...`
host archive remains historical source-export evidence, not a current guest run.

## Independent dummy-only defect reproductions

Both dummy wrappers exited **0** with expected-defect assertions satisfied. This
means the defects were reproduced, **not** that the controller passed. PowerShell
AST parsing found no controller parse errors. Only the exact definitions of
`Get-TBoundHermeticSshOptions` and `Test-TBoundCleanupDisconnectRequired` were
extracted into memory and called; top-level controller code, native process
runner, context loader, receipts, tasks, and native clients were not invoked.

Extraction method:

```powershell
$tokens = $null; $errors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile(
  (Join-Path (Get-Location) 'infra\hyperv\Invoke-TBoundTrustedOfflineVerifier.ps1'),
  [ref]$tokens, [ref]$errors)
# Select exactly one FunctionDefinitionAst by each of the two names above.
# Dot-source only [scriptblock]::Create($fn.Extent.Text), never the file.
```

### Dynamic arrays

Dummy values were `C:\DUMMY repo with spaces`, `C:\DUMMY key\id`,
`C:\DUMMY known\known_hosts`, and `DUMMY_HOST_ALIAS`; no path was accessed.

```powershell
$repo = 'C:\DUMMY repo with spaces'
$badGit = @('-c','safe.directory=' + $repo,'-C',$repo)
$goodGit = @('-c',('safe.directory=' + $repo),'-c','core.autocrlf=false','-C',$repo)
```

- Existing Git vector: **5** tokens, `-c`, `safe.directory=`, dummy repo, `-C`,
  dummy repo. Corrected grouping alone gives 4; adding the explicit LF setting
  gives **6** tokens, including one complete `safe.directory=<dummy repo>`.
- Existing exact SSH option function: **45** tokens instead of **43**.
  Index 8 is `UserKnownHostsFile=`, index 9 the separate dummy path; index 13
  is `HostKeyAlias=`, index 14 the separate alias. Parenthesizing each dynamic
  value yields complete single tokens and **43** options.
- SSH/SCP both append two positional arguments: existing full vectors **47**,
  corrected **45**. SCP inherits the same two defective option constructions.
  Neither client was invoked. Argument-array correctness does not yet test the
  C# Windows native quoting layer.

### Cleanup admission/order

Existing lines 123–125 return only `CleanupEnabled -and RunFailed`, ignoring
`QueueStarted` and `DisconnectRequested`. Line 432 enables cleanup before
context/Inspect admission. Lines 510–512 can therefore request Disconnect on a
pre-queue failure with empty or merely optional identities. Task trigger line
289 precedes receipt/identity checking at lines 303–304.

An in-memory flow used only throwing context/Inspect/SSH/queue stubs and a
`List[string]` task-call log. It followed the source ordering and used the
extracted existing cleanup predicate:

| Injected failure | Queue attempted | Disconnect attempted | Identity inputs | Dummy cleanup trigger |
| --- | --- | --- | --- | --- |
| context | false | false | empty | yes, before hypothetical post-trigger identity check |
| Inspect | false | false | empty | yes, before hypothetical post-trigger identity check |
| SSH probe | false | false | present | yes, despite no queue |
| uncertain queue request | true | false | present | yes |
| uncertain Disconnect request | true | true | present | yes |

No real operation implemented a stub. The stub log is a control-flow
counterexample, not evidence that a live Disconnect happened.

A **candidate**, not installed, guard
`enabled && failed && admittedIdentity && (queueAttempted || disconnectAttempted)`
was evaluated across all **32** Boolean combinations and **8** explicit cases:
no admission, context failure, Inspect failure, pre-queue SSH failure, uncertain
queue, uncertain Disconnect, missing identity after request, and success. Expected
results passed: only the two identity-present uncertain-request cases allowed
cleanup. `admittedIdentity` must derive from validated protected receipts/profile,
not a caller-provided truth assertion. Setting attempt flags **before** requests
remains necessary to cover an accepted request with a lost/error response.

### Smallest future controller correction/test set (not performed)

1. Parenthesize the Git configuration string and both SSH dynamic options; make
   LF configuration explicit in the common Git vector used for export. Retest
   exact vectors with spaces, quotes, trailing backslashes and metacharacters;
   test native quoting with a harmless argv-recording fixture, never SSH/SCP.
2. Arm cleanup only after exact host identity/state admission; require a possible
   queue/disconnect release attempt. Validate all mutation identity pins **before**
   a task can be triggered, distinguishing bootstrap read-only Inspect from
   mutating operations. Empty or contradictory pins must cause zero stub triggers.
3. Fault-inject every pre-queue stage (lock/context/Inspect/state/address/archive/
   staging) and require zero cleanup mutation; retain cleanup on uncertain queue
   and Disconnect requests. Cover receipt failure, timeout, lost response,
   conflicting identity and cleanup failure. Never reconnect from a catch/finally
   path; retain fresh host-observed Off/disconnected reconnect gate.
4. Review/update source/archived-file pins only after a code freeze and rerun the
   above tests. Do not take over the entire pre-existing untracked controller to
   accomplish these narrow corrections. Existing policy/simple-native-output
   test sources lack these array/pre-queue cases and were not executed here.

## Unlocks requiring user/operator context

1. **Review and authorize the helper install**, not merely its existence in the
   repository. Arrange the exact VM **Off and its single NIC disconnected** only
   after inspecting active work through an authorized channel. Preserve the golden
   baseline. This report did not query current VM/NIC state or storage budgets.
2. Use one authorized elevated **64-bit Windows PowerShell 5.1** Admin session and
   the hash-guarded bootstrap/protected staging path. The installer refuses an
   existing install/folder and checks generation 2, fixed 8 CPUs/16 GiB, exact
   disk chain/paths, checkpoint/integration/Secure Boot/firmware/DVD policy and
   storage gates (100 GiB tracked, 700 GiB free). Off/disconnected is an admission
   requirement; installation itself does not arrange it. No UAC prompt here.
3. Read back exact protected file/profile hashes/ACLs and all five fixed
   SYSTEM/highest/ServiceAccount, on-demand/no-trigger task definitions and
   folder/task ACLs. Source policy is SYSTEM/Admin control plus exact operator
   run/query, not Admin-as-operator or broad Hyper-V rights.
4. Run the protected key initializer **as the exact `...-1004` account**, then
   authorize human Admin enrollment of only its separate public key in the guest
   with the documented `restrict` option. Keep private material operator-only.
   The prior Admin maintenance key is not this enrollment. `restrict` is not a
   fixed-command confinement policy and does not make a guest shell trusted.
5. Authorize an exact pinned host-key/operator channel and a reviewed lifecycle
   run after the controller corrections and source freeze. Collect fresh
   run/source identities, protected receipts, offline observations, terminal
   exits, report and recomputed artifact-manifest hashes before claiming PASS.
6. Separately approve containment provisioning: authorized **public** signer and
   policy; offline Cosign verifier/toolchain provenance and signature material;
   signed image containing the one selected entrypoint; exact fixed non-threaded
   delegated runner/cell roots, writable `cgroup.kill`, effective bounds and
   recursive settlement. Narrow guest privileged setup requires user-authorized
   sudo/root context. No persistent broad sudo or AppArmor/userns weakening.

The `TRUSTEDVERIFIERONLY` helper is not authorization for adversarial trials or
checkpoint restore. Contaminated-guest recovery/reset and claim-bearing trial
authorization require their own reviewed runner protocol.

## What source work is possible now, and what cannot be guessed

| Area | Existing source reality | Safe future code-only work | External/freeze gate |
| --- | --- | --- | --- |
| Profile/image admission | `podman` labels itself non-claim-bearing; digest pin and `--pull=never` are not signer authorization | Pure bounded profile validation, canonical identity/digest comparison, injected offline-verifier interface with typed failure and missing-evidence rejection | Approved public signer identity/policy, signed image/signature bundle, verifier executable provenance and actual offline verification |
| Signed entrypoint | `podman_linux.go:483–501` is a host-supplied shell string, not signed in-image code; no dedicated image/entrypoint build recipe in inspected guest sources | Bounded sealed-descriptor/status protocol and rejection tests; a minimal native-entrypoint source/build-recipe proposal under review | One language/build selected by the spike, pinned compiler/linker/static dependencies/SBOM, signed image and measured binary/syscall identity; no guessed image/tag/signer |
| Static seccomp | Podman uses runtime default filtering; dev allowlist is broad and explicitly non-claim-bearing, denies Landlock setup calls and is installed after Landlock | Deterministic static-profile validation/digest/vector tests for a reviewed launcher-plus-target syscall union; reject rewrites, wrong architecture/default action | Reviewed actual launcher/target surface, exact profile source/digest, generated OCI identity and runtime acceptance/probes; no dynamic per-command synthesis |
| Landlock | `sandbox/landlock_linux.go` handles ABI-1 rights only; `child_linux.go:444–494` locks a Go thread, not all sibling threads | Rights-to-minimum-ABI validation, descriptor-only rule planning, missing-right/path rejection, typed setup/status tests; new strict path separate from dev backend | Registered exact ABI/rights plus enforcement tests. Below ABI 8, Go `LockOSThread` alone cannot establish process-wide restriction; use the one proven single-threaded native choice, or prove TSYNC at a registered capable ABI |
| cgroup/pidfd owner | Dev setup uses hierarchy-root-derived best-effort scope; errors become nil, kill/wait errors are ignored, process-group/wait4 fallback exists | Strict fixed-root identity/control/event parsing and mocked lifecycle state machine; reject missing/duplicate/malformed `populated` and threaded domains; carry errors through terminal accounting | Real authorized delegation/limits/readback, root pidfd behavior, surviving external runner, recursive kill/settlement, mount/runtime reconciliation |

These interfaces can be implemented/tested without guest access, but cannot
truthfully issue successful runtime attestations from caller-supplied booleans.
A verifier adapter must consume bounded retained verifier output bound to exact
artifact/key/policy/tool identities; an injected stub is unit evidence only.
New strict modules must not silently upgrade or rename the existing development
backends as claim-bearing. This recheck adds **no** such implementation.

The prior blocker records ABI 4 on Oct 5 and warns it cannot satisfy a profile
requiring ABI ≥5 rights. The current normative frozen-profile text requires the
**registered ABI and rights**, not inference from a kernel version; those exact
rights/identities must be explicitly reviewed. Do not downgrade an intended ≥5
requirement to the older ABI-1 code, infer TSYNC from ABI 4, or pick a new kernel
configuration here. Missing approved signer/image/toolchain/profile values are
admission gaps, not permission to fill configuration with guesses.

## Concrete frozen-profile delivery and conformance outline

1. **Freeze inputs after review:** one VM/filesystem/kernel/config, rootless
   Podman/crun builds, mapping, image/index/platform identities, authorized public
   signer/signature/verifier identities, one entrypoint source/build/binary,
   static seccomp union/digest, Landlock ABI/rights, generated OCI configuration,
   fixed delegated roots, effective resource/tree bounds, pidfd behavior,
   protected ownership and Pi/adapter identities. No placeholders count as pins.
2. **Fail-closed host admission:** offline signer verification, no pull/network
   fallback, exact generated-config comparison, descriptor/profile ownership,
   mapping/delegation/probe results. Missing/drifted identities, unsupported rights,
   runtime profile rewrite or verification rejection prevent session start.
3. **One strict launch path:** external evaluation runner registers durable
   ownership; supervisor reserves/claims single-use authority before release;
   crun applies identity/capability/NNP/namespace/cgroup/static-seccomp controls
   before signed PID-1 entrypoint starts. Entrypoint validates bounded sealed
   configuration and observable postconditions, applies/verifies registered
   Landlock, closes unlisted FDs, and executes the target exactly once. Typed
   close-on-exec status rejects partial setup; EOF alone is not success after a
   launcher crash without independent runtime/exit corroboration.
4. **Settlement/recovery:** close admission, kill complete owned cgroup subtree,
   wait bounded recursive `populated=0`, corroborate pidfd/runtime/process state,
   detach owned mounts, validate complete delta, then record terminal/import
   outcome durably. Surviving runner owns supervisor-death cleanup; reconcile only
   identity-matched resources under fixed roots. Uncertain release is UNKNOWN,
   never replayed because a target or terminal record is absent.
5. **Conformance:** allowed control plus forbidden syscall/FS/network/process
   cases; static profile accepted before entrypoint and Landlock setup calls
   permitted; unauthorized signer/missing signature/wrong digest/replaced
   entrypoint/profile/OCI/mapping rejected; all capability sets/UID/GID/NNP/FD
   boundaries verified; exact Landlock rights and metadata/mount/DAC limits
   exercised; protected trees, live `.git`, credentials and general network
   inaccessible; complete delta provenance and independent canaries/oracles.
6. **Fault suite:** crash every setup/release/terminal boundary, malformed or
   unsealed descriptors, wrong status framing/errno, launcher death, exec failure,
   daemonization/double-fork, descendants moved within subtree, PID reuse,
   cancellation/panic/SIGTERM/supervisor SIGKILL/runtime-monitor death/host restart,
   cgroup-kill error, stuck/malformed events, mount detach failure, audit
   write/sync/full errors, resource/tree-budget exhaustion and repeated
   reconciliation. Require measured deadline cleanup or explicit UNKNOWN/invalid;
   never substitute process enumeration for required cgroup teardown.
7. **Retain reproducible evidence:** exact frozen identity, current source,
   protected receipts, fresh offline observations, build/signature/OCI/profile
   material, runner/launcher exits, independent oracle/settlement records and
   recomputed artifact hashes. Pass the evaluation-guest suite before G1; then
   reuse the unchanged profile for confirmatory trials. No runtime/language/
   kernel fallback, reduced-rights mode, or invented successful attestation.

## Guest evidence boundary

No authorized pinned guest channel was used in this recheck; **current guest
kernel, state, network, cosign presence, sudo state, mapping, running work and
delegation are unknown**. Oct 5 observations (kernel `6.8.0-146-generic`, ABI 4,
cosign absent, `sudo -n` exit 1, raw userns EPERM, unverified cgroup kill) remain
dated observations only. The Oct 4 VFH report remains PASS only for its recorded
source/input pins, with containment/G1 claims false.

Current source differs from the controller pin; the intended helper/enrollment
is missing and operator context is wrong; controller array/cleanup defects remain;
no current source was transferred, queued or executed; no fresh report/manifests
or frozen-profile guest tests were produced. Therefore neither a host dummy PASS,
a historical export/report, nor an unchanged verifier file hash can support
**current-source offline PASS** or **containment/G1 PASS**.
