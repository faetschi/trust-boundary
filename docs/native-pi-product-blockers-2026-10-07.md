# Native Pi product-slice admission — 2026-10-07

**STOPPED at prerequisite admission; no product slice delivered.** Native Pi
launch, real multi-turn provider acceptance, and the companion governance viewer
are not claimed. Containment remains `not-established`; G1=false. The user's
explicit instruction requires stopping when approved credentials or protected
prerequisites are unavailable, rather than substituting a fixture.

## Source and observation scope

- Checkout: `C:\Users\Admin\Desktop\FH\Master Software Engineering\MA Thesis\trust-boundary`.
- Independently observed branch: `codex/tbound-prototype`.
- Starting HEAD: `c048e7d` (publication implementation `a977766` already accepted).
- Fresh host observation: `2026-10-07T14:57:48.7771910Z`.
- Read-only checks only: current identity/elevation, presence booleans for named
  provider configuration, four exact protected paths, and five exact task queries.
- No credential value/path was printed; no credential file was opened or searched
  for. No provider request, Pi launch, guest connection, task trigger, UAC prompt,
  signing, installation, VM/NIC transition, or privileged setup was attempted.
- User frontend plan, modified docs and Hyper-V sources remain unchanged by this
  increment. The excluded VM-start file was neither accessed nor altered.

## Fresh blockers and required unlocks

### B1 — approved provider configuration unavailable to this session

Only nonempty/empty booleans were observed through
`[Environment]::GetEnvironmentVariable(<name>, 'Process')`:

| Configuration | Configured |
| --- | --- |
| `TBOUND_OPENROUTER_API_KEY` | false |
| `TBOUND_OPENROUTER_API_KEY_FILE` | false |
| `TBOUND_OPENROUTER_MODEL` | false |
| `TBOUND_RUN_REAL_PROVIDER_TESTS` | false |

The existing loader **code is available**, not absent:
`supervisor/internal/broker/openrouter/credentials.go:26–65`. It requires a model
and credential source, then uses the platform secure-file implementation when a
file is configured. It was not invoked. The observations establish only that
its inputs are unavailable in this session; they do not establish that no key
exists elsewhere. No unrequested filesystem/key-store search was performed.

`adapter/profile.json` pins Pi 0.87.1 and four tool schemas; it is not a registered
frozen provider/runtime profile. `docs/experiment-manifest.md` explicitly calls
itself candidate/not G1 and freezes nothing. Historical genuine single-write
evidence in `docs/provider-durable-evidence.md` is not current multi-turn native
Pi acceptance or operator approval for a newly invented profile.

**Unlock input:** the operator must identify/authorize the approved loader and
protected credential handle/path (not key bytes in chat), and supply the reviewed
frozen provider profile identity/digest, endpoint/protocol, exact model, allowed
synthetic-content conversation and request/token budget. The historical approved
model is `nvidia/nemotron-3.5-lightning:free`; no other model is inferred as approved.
Real-provider opt-in is enabled only for the separately authorized bounded run,
not ordinary verification. Credentials must remain in the trusted broker; do not
copy them into Pi, workers, guests or evidence.

### B2 — protected host automation missing and operator context not admitted

Current Windows identity SID:
`S-1-5-21-2350865082-651554413-1548572510-1001`.
`WindowsPrincipal.IsInRole(Administrator)` returned **false**.
The reviewed controller requires SID ending **1004**; this is a source requirement,
not a currently installed principal attestation.

`Test-Path -LiteralPath` returned **false** for every exact path:

```text
C:\ProgramData\TBoundHostAutomation
C:\ProgramData\TBoundHostAutomation\TBoundHostActions.ps1
C:\ProgramData\TBoundHostAutomation\profile.json
C:\ProgramData\TBoundHostAutomation\operator-public
```

Each `schtasks.exe /query /tn <exact task>` returned native exit **1**:

```text
\TBound\Inspect
\TBound\Disconnect
\TBound\StopOffline
\TBound\ConnectOff
\TBound\StartTrustedMaintenance
```

No task was run or installed. These match the earlier absence observations in
`docs/guest-profile-prerequisites-2026-10-06.md`; other task namespaces are not
substitutes.

**Unlock input/action:** authorized operator setup must install the reviewed,
protected helper/task bundle, enroll the SID …-1004 operator and its protected
SSH/host-identity metadata, and provide the reviewed installed profile/receipt.
Review the previously reproduced native Git/SSH option-vector, LF-export and
pre-queue cleanup defects before executing the controller. Existing untracked
controller sources are not adopted, patched or installed by this increment.

### B3 — frozen containment/signing/settlement not established

No fresh guest probe was attempted after B1/B2 failed. Therefore the following
remain **previously recorded, unestablished prerequisites**, not new absence
measurements: authorized signer/signed image and in-image entrypoint, offline
cosign verification, exact static security policy, writable delegated cgroup-v2
kill/settlement lifecycle and surviving independent observation. See
`docs/containment-blockers-2026-10-05.md` and the Oct 6 prerequisite report.

**Unlock input/action:** supply the authorized image/manifest/signature and
verification-key identities, available offline verifier, frozen entrypoint and
policy digests, approved cgroup delegation/teardown design and required access.
Then run bounded, source-pinned profile/descendant-settlement verification under
the admitted operator. Do not relax userns/Landlock/seccomp requirements or
replace signatures with digest presence. Current-source offline guest evidence
must be regenerated; historical WSL/guest tests cannot qualify the new product.

## Product work deliberately not substituted

`supervisor/cmd/tbound/main.go:74–96` still has only the explicit synthetic smoke
listener and refuses an unconfigured runtime. The proposed `tbound serve --pi`
is not implemented. Pi's public `InteractiveMode` makes native integration
feasible but does not close direct shell/config/session/extension actions.

No fake-provider native launcher was offered as the required real session. No
new viewer, observation contract or replay UI was implemented after admission
failed. The requested sanitized, unmistakably REPLAY-labeled frontend work remains
possible as a **separately authorized, non-claim-bearing workstream**; it must not
silently replace this stopped product objective. Resume the product only after
the operator unlock inputs are available and admitted, or explicit user revision
separates frontend-only work from the blocked real-session requirement.

## Documentation-increment verification

Only this blocker report is intended for commit. An isolated archive of the
unchanged committed supervisor is checked with the full Linux race suite in
private ext4/mode-0700 TMPDIR, plus the exact-copy Windows build/internal/e2e
checks, formatting, unchanged modules and post-commit byte comparison. These
local component checks do **not** test a new product, contact a provider, qualify
the guest, or turn any blocker into a pass. Adapter source is untouched; npm
verification is not applicable to this documentation-only increment.

Verification outcomes (all exit 0; unchanged source, no product acceptance):

- Linux source archive: `/home/jeli2k/tbound-coordinator-nativepiblocker.ouokwz`,
  source HEAD `c048e7d`, no overlays; ext4, TMPDIR mode 0700.
- Full `go test -race -count=1 ./...`: passed all supervisor packages.
- `gofmt -l supervisor`: empty, including the working-tree check.
- Windows exact-copy snapshot:
  `C:\Users\Admin\AppData\Local\Temp\opencode\tbound-windows-3a2def199e5c4ebfa50fca3ef1e61ba0`;
  `go build ./...` and `go test -count=1 ./internal/... ./e2e`: passed.
- `git diff --exit-code HEAD -- supervisor/go.mod supervisor/go.sum
  adapter/package.json adapter/package-lock.json`: no changes.
- `git diff --check -- supervisor adapter docs infra/hyperv TASK_STATE.md`: clean
  (line-ending conversion warnings only). Scope excludes the forbidden file;
  preserved user sources were not corrected or adopted.
- Race-log SHA-256:
  `9145a51573fabcd8e58fe0a156f8fbe48a23a8e1e06758652651672b35fd4917`.
- Pre-test complete supervisor source-hash listing SHA-256:
  `97d9cd5d54d2c158e6df116bbc459bfd698b55e29264cfa2c289e42e7a78465f`.

The coordinator performs the post-commit LF-archive/source-byte comparison
against this exact snapshot and reports its outcome with the commit identity.
No passing product acceptance is implied by these local tests or this report.
