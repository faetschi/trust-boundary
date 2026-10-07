# Native Pi autonomous setup attempt — 2026-10-07

## Updated operator inputs

The operator supplied the protected credential-file location and explicitly
authorized using TBound's secure loader for bounded synthetic-content testing.
Metadata-only inspection found the file present, regular, not a reparse point,
owned by the current Windows SID, with a protected DACL and one non-inherited
current-owner Allow ACE (`Read, Synchronize`). No key bytes were read, printed,
copied, mounted into a worker/guest, or included in this report. These are
metadata observations; the real loader must validate its opened handle again.

The operator selected `nvidia/nemotron-3.5-lightning:free` and authorized creating
the provider profile. The proposed first provider configuration is OpenRouter
Chat Completions at the fixed `https://openrouter.ai/api/v1/chat/completions`
endpoint, model selected by a trusted configuration field, not hardcoded inside
the Pi host or selected by Pi/browser input. Future model changes require a new
reviewed profile identity/digest rather than silently changing an admitted run.
Initial live-test budget: at most eight upstream requests per conversation,
bounded synthetic text/tool output, finite context/output token caps and wall
timeout; no retries/fallback or unbounded provider loop. Implement and test these
bounds before requesting that conversation. This is a design decision, **not an
implemented or registered frozen profile**. The provider inputs are now supplied;
the earlier report's missing-input observations were historical session state.

## Autonomous bounded installation attempt

User authorization covered attempting the reviewed setup, not bypassing its
privilege checks. A private driver validated exact disk SHA-256 against the
existing reviewed bootstrap/runbook pins for:

- `Bootstrap-TBoundHostAutomation.ps1`
- `Install-TBoundHostAutomation.ps1`
- `TBoundHostActions.ps1`
- `TBoundHostAccessPolicy.psm1`
- `Initialize-TBoundOperatorSsh.ps1`

All five comparisons passed. The driver evaluated only the captured,
hash-verified bootstrap with the exact source root and required operator SID
`S-1-5-21-2350865082-651554413-1548572510-1004`. Current identity still ended in
`1001`, administrator role enabled was false. The bootstrap's first guard refused
before staging or invoking the installer:

```text
BOOTSTRAP_REFUSAL=Run this bootstrap from an elevated Windows PowerShell session.
POST_PATH=C:\ProgramData\TBoundHostAutomationSource EXISTS=False
POST_PATH=C:\ProgramData\TBoundHostAutomation EXISTS=False
native driver exit: 1
```

Attempt driver retained at
`C:\Users\Admin\AppData\Local\Temp\opencode\tbound-attempt-reviewed-host-setup.ps1`.
No task trigger, VM stop/start, NIC change, installer execution, operator-key
generation/enrollment or guest transfer occurred. No repository infrastructure
source was edited or staged. The forbidden VM-start file was not accessed.

## Remaining unlock boundary

An already authorized elevated 64-bit Windows PowerShell 5.1 execution context
is required to run the hash-guarded bootstrap in
`infra/hyperv/TBOUND-HOST-AUTOMATION.md:35–55`. Administrator group membership
with a filtered token is not enough. The reviewed installer requires the pinned
VM to be Off; do not force-stop it or change policy just to get through setup.
The SID …-1004 initializer/enrollment and protected host receipts must then be
established under the reviewed procedure. User authorization alone cannot change
the current Windows token or provide another account's credentials. No UAC
approval/password is automated or bypassed.

Authorized signer/image/signature, offline verifier and cgroup-v2 settlement
remain separate previously recorded prerequisites. A locally invented signing
key is not operator signer authorization. No fresh guest probe is claimed after
host admission failed. See `docs/guest-profile-prerequisites-2026-10-06.md` and
`docs/containment-blockers-2026-10-05.md` for exact profile requirements.

Following the goal's stop condition for another unavailable protected prerequisite,
product implementation is stopped here. No native launcher, provider profile
implementation, multi-turn exchange or replacement fixture was delivered by this
attempt. Existing components and Pi-native integration feasibility are unchanged.
Containment remains `not-established`; G1=false. Resuming requires the privileged
execution boundary to become available, or an explicit revised scope authorizing
non-claim-bearing implementation while these external gates remain blocked.

## Verification scope

Only this addendum is intended for commit. Local unchanged-source regression and
post-commit byte checks qualify the documentation increment, not protected setup
or real-session acceptance.

- Linux isolated source: `/home/jeli2k/tbound-coordinator-nativesetupblocked.xulRtk`,
  source HEAD `c08b1cf`, no overlays; ext4, private TMPDIR mode 0700.
- Windows exact-copy snapshot `tbound-windows-a3bb82517c2a419098bd0027e2b80869`:
  `go build ./...` and `go test -count=1 ./internal/... ./e2e` passed (exit 0).
- `gofmt -l supervisor` was empty. Scoped diff check passed (line-ending warnings
  only); Go modules and adapter dependency files were unchanged. Adapter source
  was not touched, so npm verification is not applicable.
- Full Linux `go test -race -count=1 ./...` passed all packages (exit 0).
- Race-log SHA-256:
  `263c608f93e91766e765aab6ccb2dbe2ad79b7ef56a0e2167a5607a1f75e35a2`.
- Complete pre-test supervisor source-hash listing SHA-256:
  `deba3e1b218e252b1ff89568106d04e2e78e09f3dc6d5d68d26fb0ee25e21650`.
- Post-commit LF archive comparison against this tested snapshot is performed
  separately and reported alongside the commit identity. It must match every
  source byte and complete Go-file set, not merely selected implementation files.
