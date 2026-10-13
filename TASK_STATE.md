# TBound task state

Updated 2026-10-13. Implementation branch: codex/tbound-prototype.
Commit reviewed, verified increments; use explicit staging, no blanket reset.
Receipts and detailed logs: ../review-artifacts/tbound-resume-2026-10-13/.

## Claim boundary

- CLI-first read/write/edit/bash using official Pi CLI 0.87.1 and mediation
  extension; no expanded provider tool surface or unmediated fallback.
- G1=false; containment is not-established; H1 unexecuted. No qualified
  successful public production launch or provider exchange through it.
- Hyper-V development guest ABI4; WSL component host ABI1; D09 requires exact
  ABI8. No VM created, provisioned, registered or qualified in this work.
- Thesis draft2c7bf4d is isolated on codex/tbound-recovery-protocol in
  ../MSE_MA_Thesis; main unchanged. Draft51 recovery trials (8 lifecycle plus
  9 incidents, x3) is not frozen/registered; E07 conformance remains separate.

## Completed increments

| Commit | Scope |
|---|---|
| a281699 | Windows ACL/SID bounds |
| 178acdd | Official CLI RPC and worker lifecycle |
| 385982d / 91440d5 | D09 preflight and consumer proof before bootstrap |
| 11da49b / 2b5673e | UNKNOWN launch/admission cleanup ownership retention |
| 22c7cfd / 282bb5e | Exact ABI gates and minimal Ubuntu guest setup recipe |
| ace9592 / 3d30fcf | Independent crash markers and trusted audit hooks |
| 47a6d73 | Guarded public D09 launch and single-owner drained RPC stdout |
| 18c9e0c | Installer rejects incomplete updates before replacing tools |
| abb32ce | Doctor reports live exact-ABI prerequisite |
| 5293d2c | Actionable full profile paths and presence-only diagnostics |
| da09eea | Durable scoped post-operation admission and cross-journal replay |
| b1ff84c | Operator scope flags through authenticated service requests |

RPC failures require prompt command and exact active request ID. Close failures
remain observable. UNKNOWN retains cleanup/admission owners and blocks
publication. Launch proof is journaled before the admitted prompt is sent.
Minimal Ubuntu Server ISO is the supported local guest recipe; setup artifacts
are preparation only. Media custody and exact ABI checks precede registration.

## Current post-operation admission increment

- Applied Store-owned protected delta-admission journal, full verified input/
  candidate manifests and exact scope policy, before generation promotion.
  Primary outcome references the separate decision. DENY retains evidence
  without advancing gN/ledger; UNKNOWN quarantines without effect replay.
- Canonical v2 intent requires explicit scope; evidence/bundle are v2. Recovery
  and replay verify exact cross-journal references and consume records once.
  Legacy roots missing admission evidence are refused without inference.
- Host authority derives typed scopes and records v2 preauthorization. Durable
  executor clones canonical Bash scope; nil disables Bash, [] permits no changes.
  Terminal-origin verification now accepts only the matching v2 scoped record.
- D09 verifies reconstructed Store evidence; execution-only receipt digest
  stays distinct from the scope-inclusive Store argument digest.
- Real regressions reproduced DENY accepting forged next generation/order and
  evidence-reference omission/aliasing; corrected package/race checks pass.
  Suspected event-outcome acceptance gap was disproved: audit already rejects
  the malformed frame; explicit framing checks are defense in depth only.
- Final full Linux unit/race: each972 passes, 0 failures, 7 skips, 25 packages
  passed; vet clean. Windows294 passes, 0 failures,13 skips; vet clean.
  Logs: delta-admission-final-{unit,race}.jsonl / -vet.txt in isolated Linux logs;
  Windows log in review-artifacts. All 28 tested source files hash-match the WSL copy; committed da09eea.
  Receipt: delta-admission-receipt-da09eea.json.
- Store/host and final independent Luna max safety/integration/thesis reviews
  accepted. Final reviewer checked versioned origin/test consumers; stale
  status documentation has been reconciled.
- Operator --bash-output / --bash-no-outputs wiring is committed b1ff84c;
  Linux CLI unit/race each230 passes,0 failures,2 skips; vet clean. Seven
  tested source files hash-match; operator-scope-receipt-b1ff84c.json retained.
  Windows compiled source is unchanged from da09eea. Scope travels through signed-UID-authenticated service
  requests and payload journal; the provider cannot set it. Nil disables Bash.
  Independent Luna max review accepted. Focused race tests plus same-ID/different-
  scope socket conflict test pass with no rerun or journal change. CLI docs synced.

## Remaining milestones

1. Admission gate checkpoint completed as da09eea.
2. Store promotion-before-primary-outcome cut committed09a1901. Extend the
   cut through actual governed authorization and provider result-release gates.
3. Complete trusted lifecycle/provider/Store/checkpoint cuts and independent
   oracles. LC04/05/06 remain incomplete end to end; hooks are component evidence.
4. Qualify clean registered ABI8 host, signed CLI closure/profile, service/source
   admission, containment/settlement and real end-to-end publication/recovery.
5. Freeze corpus, calibration, oracle matrix and analysis before H1 registration.
6. Standalone fresh installation, recovery/upgrades, release custody and support.

## Verification and preservation

- READ-ONLY Luna max preparation/review; parent applies, tests, commits.
  Native delegation avoids prior T3 approval-required PowerShell prompt loop.
- Isolated Linux source: /home/jeli2k/tbound-resume-20261013.kRuoVd/source;
  Go: /home/jeli2k/tbound-go1.27.1.zkiVXZ/bin; private short TMPDIR:
  /home/jeli2k/tb.n2XBZV. Use umask077; long paths exceed AF_UNIX limits.
- Preserve supervisor/tbound and tbound.exe. Build only to private explicit paths.
  Never access vm_start.txt, provider/admin credentials or real signing keys.
  No real provider/headline run or unauthorized host provisioning.
- Exclude pre-existing deletions: docs/native-pi-product-blockers-2026-10-07.md,
  docs/next-evaluation-unlocks-2026-10-07.md,
  docs/tbound-readiness-status-2026-10-08.md,
  docs/thesis-gap-ledger-2026-10-05.md, docs/verifier-blockers-2026-10-05.md.

## Current Store crash-boundary increment

- Native typed-write helper exits73 after actual ALLOW candidate promotion/sync
  but before primary outcome append. Independent synced marker, g1 bytes/scan,
  primary unresolved intent and full admitted manifests bind the actual cut.
- Fresh recovery subprocess returns no usable Store and ErrQuarantined; primary/
  admission journals and marker stay unchanged. Replay uses accepted seed g0
  plus actual journals, no invented candidate ledger, and returns quarantine with
  no accepted transition or operation. Minimal child environment avoids credentials.
- Focused race x3 passed; Store package unit/race each85 passed, zero failures/
  skips; vet clean. Test source matches isolated Linux copy. Independent Luna
  review accepted; stronger callback-dispatch sentinels adopted and verified.
  Final small re-review accepted with no blockers. Committed09a1901; receipt
  promotion-crash-receipt-09a1901.json. This test-only change does not affect Windows.
- Thesis status sync accepted and committed03e74e5 on existing
  codex/tbound-recovery-protocol; main ea543c5 unchanged. G1/H1 and draft51 claims
  unchanged. No power-loss, registered-trial or qualified-host evidence claimed.
- Next slice: governed authority/result-release subprocess crash harness using
  actual offline provider conversation, SessionOperationAuthority and durable
  executor. Check capture/decision -> durable authorization -> primary intent,
  ALLOW admission/g1, unresolved outcome and no provider_tool_result; fresh
  recovery quarantines with sentinels/no replay. store_crash_patch prepares
  an apply-ready READ-ONLY test patch. No new production hook is needed.
  LC04/05 and post-outcome/checkpoint cuts remain later end-to-end work.

## Governed crash / frontend checkpoint

- Actual governed typed-write crash test now applied: offline provider capture
  and deterministic ALLOW -> protected v2 operation authorization -> effect
  intent -> admitted/promoted g1 -> crash before primary outcome/result release.
  Fresh process quarantines with no callbacks; all three journals stay unchanged.
- Required pinned Linux build, vet and formatting checks passed. Focused race3
  passed without skips; full race978 pass,0 fail,7 existing skips. Source hash
  matches tested copy. Independent Luna safety/integration/thesis review accepted
  the narrow process-crash claim. No G1/H1 or power-loss evidence implied.
- Parallel frontend_thesis_exploration reads AH-technical requirements;
  frontend_surface_inventory found no web frontend, existing headless Pi RPC
  and final CLI receipts. A live user panel requires event forwarding; existing
  tool-result text/post-run receipt can support a labelled offline visual demo.
- external_review_alignment evaluates user's WP0-WP6 and qualification sequence,
  including exact recovery and H1 counts. No research denominator is frozen.
## Review evaluation / frontend direction

- Governed crash test committed0e39619, receipt governed-crash-receipt-0e39619.json.
  Narrow offline authority/result-release process-crash coverage accepted; all
  required Linux checks green with seven unchanged environment-dependent skips.
- External review broadly agrees: components implemented != G0/G1/G2/G3/G4
  qualification. WP6 writing underway; empirical analysis awaits data. Draft51
  recovery proposal conflicts with stale48 roadmap/concept; neither registered.
  H1 adversarial299 is separate and needs paired calibration +100 compliant cohort.
- Frontend is demonstrator/product work, not a claim gate. Active branch has no
  UI; Pi remains headless RPC. No human override/per-tool approval conversion.
- READ-ONLY frontend_existing_companion_review inspects sibling installer viewer
  as a design reference; no sibling modifications or global Git trust changes.
  frontend_surface_inventory prepares a bounded local receipt viewer first:
  post-run import of actual sanitized typed receipt, explicit offline/non-claim-
  bearing/UNKNOWN state; no HTTP/control endpoints/raw journal or secret exposure.
  Live governance projection and authenticated stream remain future integration.
## PR10 reuse integration (user-authorized)

- Draft https://github.com/faetschi/trust-boundary/pull/10 linked to this thread.
  Source installer worktree branch feat/tbound-distribution b6ee58f; target
  codex/tbound-prototype pushed through0e39619. No merge/published qualification.
- User asked reuse instead of duplicate frontend. New static viewer preparation
  stopped. Existing browser companion is shared-history code removed by target;
  this source PR alone does not restore it. Exact reviewed restoration pending.
- Isolated checkout ../trust-boundary-pr10 branch codex/tbound-distribution-pr10
  merges target into source; only Makefile/docs/install.md conflict. Original
  installer checkout untouched. Native READ-ONLY Luna reviewers prepare conflict
  files, installer safety assessment and minimum frontend reuse file set.
- Preserve new user file docs-next-work.txt; no blanket staging/reset.

## Priority and product direction (2026-10-13)

- User prioritizes docs-next-work.txt: complete governed lifecycle/crash cuts,
  reconcile recovery protocol documents, qualify an exact-ABI8 clean guest,
  complete signed Pi closure/service admission, then independent publication/
  containment evidence and research registration. G1=false; H1 unexecuted.
- Frontend product target is an interactive web chat wrapper around the official
  Pi CLI/RPC harness, with transcript/tool activity and tbound policy, provenance,
  publication and recovery observations. Existing authenticated governance must
  own chat/session actions. No second effect authority or per-tool approval path.
- User requests a new presentation design from scratch. Existing typed viewer
  backend may be reused; its current read-only page is groundwork, not the final
  product frontend. Work is secondary to the core priorities above.
- Companion preparation remains isolated on codex/tbound-governance-companion.
  Review blocks unverified replay success presentation and two HTTP boundedness
  gaps (SSE flush deadline and request bodies). No companion commit/PR yet.
- Installer PR10 remains draft/conflicting with security blockers. No install,
  merge, clean-host qualification or public production launch occurred.
- Frontend-only agent handoff: docs/frontend-product-agent.md. User will start
  a separate thread/worktree on codex/tbound-web-chat; this thread stays on core
  docs-next-work priorities. No new top-level thread was launched here.

## Web chat frontend increment (2026-10-13, codex/tbound-web-chat)

- Frontend thread delivers milestone 1: a new untrusted, read-only Pi web chat
  workspace. Additive only; no authority-owned file is edited. New code:
  supervisor/internal/webchat (bounded contract, validation, bounded gateway
  store, GET-only/loopback HTTP+SSE handler, embedded workspace assets,
  fixtures) and supervisor/cmd/tbound-webchat (loopback-only entrypoint).
  Docs: docs/web-chat.md and the proposed service contract
  docs/web-chat-contract.md. QA screenshots: docs/webchat/qa/.
- Claim boundary unchanged: G1=false, containment not-established, H1
  unexecuted, claim-bearing=false. Fixtures are non-claim-bearing and clearly
  labelled FIXTURE/REPLAY/UNAVAILABLE. Live projection, persistent multi-turn
  sessions, turn submission, cancellation, and browser authentication are NOT
  implemented; --live fails closed and the composer is disabled and labelled.
- Success is never presented as confirmed unless verified: documents are
  downgraded to diagnostic/unverified-input, authoritative authority is refused,
  and the client gates success styling on verification (unverified success
  renders as a "reported" chip plus an explicit unverified marker).
- The three previously reviewed companion defects are fixed and test-locked:
  unverified replay success presentation; SSE write deadline cleared before
  flush; unbounded/slow GET request bodies (bodies are rejected, never read).
- Verified in Linux with pinned Go 1.27.1: gofmt clean, build ./... exit 0, vet
  exit 0, focused unit and race tests pass, node --test 20/20 pass. Full
  go test -race ./... passes except internal/evaluationindex, which only fails
  because the private Linux copy excluded the repository docs/ tree (passes from
  the full checkout). These are component/UI checks; they establish no claim.
- Critical Luna max review delegated (async) against the increment; findings to
  be applied in a follow-up commit. Milestones 2-3 (read and interactive
  integration) need supervisor service changes owned by the core thread; the
  gateway consumes the proposed projection and implements none of it.
