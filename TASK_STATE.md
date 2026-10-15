# TBound task state

Updated 2026-10-15. Implementation branch: codex/tbound-prototype.
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

## v2 restyle of the web chat UI (2026-10-15, uncommitted on codex/tbound-web-chat)

- Dark is the default theme (nothing stored, an unknown value, or refused storage). System follows the OS; Light and Dark stay selectable (`theme.ts`, `public/theme-boot.js`, `tokens.css`).
- Layout follows `docs/webchat/mockups/tbound-redesign-v2.html`: session title and source chips in the header row, claim strip beneath, theme switch in a "More options" disclosure, rail and inspector at 264 px and 340 px. Deliberately not in v2: "+ New session" (rail tests forbid it) and the document icon (no action).
- Verified: vitest 74 files, 1276 passed, 0 skipped; check:sinks and check:deps clean; dist reproducible (Windows); Go `-race` 60 top-level PASS, 0 SKIP on the same dist; ui-qa p8 12 of 12 PASS (`docs/webchat/qa/p8-ui-qa-receipt.json`).
- ui-qa edits are contract updates, not weakened checks: the theme walk opens the disclosure; E expects dark; D counts drawn header buttons; J follows the header; copy controls match by aria-label. Check G's stop is now asynchronous: the blocking `spawnSync` stop had stamped the first retries in one burst. The band is unchanged.
- QA history: p6 failed 6 of 12 (K real, fixed; C, D, E stale; J a locator error; G timing); p7 failed G only; p8 passes and is the committed receipt. The p6 and p7 receipts are not in the repo.
- Phone: out of scope per the owner. The phone sections of ui-qa (J and K) still run and pass; no phone design work.
- Open: the v3 mockup (`tbound-redesign-v3.html`) still shows the pre-restyle UI; it is being regenerated from the restyled UI in a separate commit.

## FX3 state of the web chat extension (2026-10-15; FX3 and FX4 are committed through 92a5927. Re-run `npm run repro` against the committed dist.)

- FX4 (2026-10-15; FX3 and FX4 are committed through 92a5927. Re-run `npm run repro` against the committed dist.): the phone composer reason wraps and the phone field is two lines tall; dock clearance 268 px; QA 12 of 12; v3 mockup regenerated (25 of 27 frames identical; gov-recovery-light-1440 has a 5-px corner difference). See extensions/webchat/docs/frontend.md, "Redesign review fixes, FX4".
- Footer: the last box of the workspace, under the composer, at every width (DOM order = drawn order; no CSS
  `order`). QA check J (chat head within 160 px of the header): 98 px at 390 and 360 (was 181), 53 px at 1440 to
  768 (was 98 to 144). Check K: 0 stops under the dock at 360x640, 390x844, 412x915 and 844x390.
- Browser QA (`p5`, gateway in WSL, Chrome 155, `docs/webchat/qa/p5-ui-qa-receipt.json`): PASS, 12 of 12 checks, 48 screenshots.
- Verified: Windows `gate.ps1 -Task verify` (vitest 74 files, 1258 tests, 0 skipped; sinks; deps) and `-Task go` exit 0;
  WSL Go 1.27.1: gofmt empty, `go vet` exit 0, `go test -race -v` 60 top-level PASS and 0 SKIP (114 subtests);
  legacy `node --test` 262 of 262 (Windows and WSL). Dist tree digest (repro form) `6d80b808…a585c5`.
- Open for the owner: the legacy footer's policy counts are derived from loaded items (with no governance panel
  it reads 0 allow / 0 deny / 0 unknown, where React reads unavailable). FX3 and FX4 are committed through 92a5927. Re-run `npm run repro` against the committed dist.
  Nothing is committed in this round. Details: `extensions/webchat/docs/frontend.md`, "Redesign review fixes, FX3".

## Current state of the web chat extension. Status at 0b6bdc1 (2026-10-15); FX4 code is 92a5927

- Exists: optional module `extensions/webchat/` (`tbound/webchat`, moved in 18f71e6). The core never
  imports it; `scripts/check-extension-boundary.sh` guards that (`make test`; CI `extension-boundary.yml`).
- Go gateway (`internal/webchat`, `cmd/tbound-webchat`): read-only, loopback. GET and HEAD on `/` (302)
  and `/favicon.ico` (204); other routes GET-only; other methods `405` with `Allow`. `--live` refused.
- React client at `/app/` (`frontend/`): P1 `263b6ca`, P2 `4700ca5`, P3 `58492c5`..`57e6406`. Default since
  `6d03657`: `/` is `302` to `/app/`. `/legacy/` is deprecated and serves `/` only if the React build fails.
- Run: `cd extensions/webchat && go run ./cmd/tbound-webchat --demo`, open `http://127.0.0.1:8791/`.
  Frontend gate: `make extension-webchat-frontend-verify` (Node 24.15.0).
- Last verified (FX3, 2026-10-15): see the FX3 block above. Before FX3 (57e6406): vitest 896/896 on Linux;
  extension Go `-race` 54 top-level PASS; legacy node 258/258.
- Open: no screen-reader test; narrow drawer scrolls with the page; Jump-to-latest only in happy-dom;
  LOST shapes and the 1024 px drawer are covered by browser QA p5 (Chrome 155, 12/12;
  `extensions/webchat/docs/webchat/qa/p5-ui-qa-receipt.json`). No live projection, submission or browser auth.
- Open owner decisions (one list):
  (a) Lossy wording: lossy success counted under `reported`; separate posture entry (`frontend.md`, "Phase 3 review fixes").
  (b) Stream `complete` wording: code already neutral (idle).
  (c) Legacy UI removal (`/legacy/`, `assets/`, legacy tests) after sign-off.
  (d) Keep or remove the reserved `lost` state (`web-chat.md`, `web-chat-contract.md`).
  (e) `core/trajectory.ts` `toneOf` still folds `lost` into `deny` for legacy parity (the React rail uses `recordDot`; not recorded elsewhere).
- PR #11 is open and still a draft (checked 2026-10-15).
- Not verified here: GitHub Actions after `4700ca5` (none recorded); the `csp-smoke` job (dispatch-only,
  never run); browsers other than Chrome 155.
- Older sections marked "(superseded by the block at the top)" keep their receipts.

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

(superseded by the block at the top)

- Frontend thread delivers milestone 1: a new untrusted, read-only Pi web chat
  workspace. Additive only; no authority-owned file is edited. New code:
  extensions/webchat/internal/webchat (bounded contract, validation, bounded gateway
  store, GET-only/loopback HTTP+SSE handler, embedded workspace assets,
  fixtures) and extensions/webchat/cmd/tbound-webchat (loopback-only entrypoint).
  Docs: extensions/webchat/docs/web-chat.md and the proposed service contract
  extensions/webchat/docs/web-chat-contract.md. QA screenshots: extensions/webchat/docs/webchat/qa/.
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
- Increment 3 (2026-10-13): timeline finding filters (All/Denied/Unknown/Failed
  with per-filter counts and coss Toggle Group semantics), an observed-posture
  summary that separates policy ALLOW from execution success (an unverified
  success is counted and labelled "reported", never "verified"), and an explicit
  empty state when a filter matches nothing. Filtering is presentation-only.
  Adds filters.mjs with Node tests, render/toolbar tests and toolbar CSS.
- Verified: gofmt clean, build/vet green, focused tests and race green, 35 Node
  tests green (9 filter tests + 13 render tests). Screenshots and deterministic
  keyboard audits (tab order, Enter/Escape, filter selection with focus
  retention, ArrowRight within the group, live innerWidth) retained in
  extensions/webchat/docs/webchat/qa; both viewports verified.

## Web chat increment 2: receipt importer + coss/ui design system (2026-10-13)

(superseded by the block at the top)

- Round-1 Luna max review was cancelled before producing findings (user stop);
  round 2 was started against this state with the original brief and the new
  commit range.
- Added the typed post-run receipt importer (extensions/webchat/internal/webchat/receipt.go,
  --receipt / --receipt-demo). It reads only declared fields from the production,
  dev-fixture and native-fixture receipt shapes and projects a bounded replay
  observation. It refuses claim_bearing=true, established containment,
  unrecognized mode/schema, unregistered tools and trailing JSON. A per-tool
  result is succeeded only with a retained terminal outcome (observed Bash exit
  code or a generation-advancing approved delta); everything else is unknown and
  surfaced as a per-tool-outcome stream gap plus a warning. Transcript stays
  unavailable and no per-tool verdicts are fabricated. Real dev receipt import
  verified end to end in the running UI with no console errors.
- Adopted the coss/ui design-token vocabulary and component conventions in the
  workspace CSS (shadcn/Cal.com tokens incl. destructive/info/success/warning,
  Card/Badge/Alert/Empty/Skeleton/Kbd/Sidebar patterns). React, Base UI and
  Tailwind are deliberately NOT used: the thesis defers browser clients and
  requires a minimal web service, so the client stays zero-build ESM + CSS with
  tokens in :root for a future coss/ui adoption. No webfont is fetched.
- Accessibility fixes verified by DevTools keyboard audits: full tab order,
  Enter toggles a tool disclosure, Enter/Escape open and close the governance
  drawer, and the closed narrow-viewport drawer is inert + aria-hidden.
- Verified with pinned Go 1.27.1 in Linux: gofmt clean, build exit 0, vet exit 0,
  focused unit and race tests pass, node --test 22/22, full race suite green
  except the known evaluationindex docs-path environment failure. These are
  component/UI checks; they establish no claim.
- Follow-up increment: typed post-run receipt importer (receipt.go) with
  `--receipt <path>` and `--receipt-demo`. It projects the real development CLI
  fixture receipt (dev-fixture), the native-runtime fixture line and the
  production receipt schema into a bounded replay observation. A per-tool result
  is `succeeded` only where the receipt retains a terminal outcome (observed Bash
  exit, or an approved delta that advanced the shallow generation); other calls
  stay UNKNOWN and are surfaced as a per-tool-outcome stream gap. It refuses
  claim_bearing/containment claims, unknown modes/schemas and unregistered tools.
  Verified by importing the actual retained development receipt in the running
  workspace.
- coss/ui adopted as the presentation design system only (token vocabulary,
  badge/card/alert/empty/skeleton/kbd/sidebar conventions, labelled role="log"
  chat with aria-live off). React 19 + Tailwind 4 + Base UI is deliberately not
  added: the browser client stays a deferred non-claim-bearing demonstrator and
  the web service stays minimal (zero-build ESM + Go gateway).
- Re-verified: gofmt clean, build/vet green, focused tests and race green, 22
  Node tests green (adds receipt-importer Go tests and render/gap/loading tests).
  Screenshots and keyboard audits retained in extensions/webchat/docs/webchat/qa/.
- Second Luna max review round (tbwc-m1-review-2) running; round 1
  (tbwc-m1-review-1) was cancelled before producing findings.

## Web chat increment 3: finding filters and posture summary (2026-10-13)

(superseded by the block at the top)

- Added a timeline finding filter (All / Denied / Unknown / Failed) as a coss
  Toggle Group convention: aria-pressed segmented buttons with per-filter counts,
  Left/Right roving arrow-key focus, focus preserved across the re-render, a
  filter summary ("3 of 18 element(s) match Denied") and an explicit empty state
  when nothing matches. A filter keeps the correlated tool block together and
  hides transcript messages, so a matched finding always keeps its context; it
  never changes a projection, verdict or effect.
- Added an observed-posture row that separates policy verdicts (ALLOW/DENY/unknown
  decision) from execution results (verified/reported/denied/failed/unknown/
  lossy). An unverified success counts as "reported", never "verified".
- New pure module assets/filters.mjs with Node tests; render tests extended for
  the toolbar, the filtered view and the empty-filter state. Node suite is now 35
  tests. Docs updated (extensions/webchat/docs/web-chat.md sections 4.3 and 8.1).
- QA: fresh screenshots including fixture-denied-filter, refreshed keyboard audits
  with the filter and arrow-key checks and a self-describing viewport record.

## Web chat increment 4: second-review fixes (2026-10-13)

(superseded by the block at the top)

- Fail-open success verification closed: every item must declare a verification
  scope and the store no longer defaults it; a new `observed` authority is the
  only value a live projection may carry and it is refused for fixture/replay/
  receipt sources; the client gate now requires the exact pair
  (authority=observed AND verification_scope=structural-audit-only), so an empty,
  unavailable or unexpected value is never treated as verified.
- Untrusted claims now carry explicit textual trust labels (panel trust rows, a
  session "reported" marker, and a "reported, not verified" marker beside an
  unverified success chip), not colour alone.
- Receipt importer no longer invents per-tool success: only a settled Bash exit
  maps to succeeded; other calls stay UNKNOWN with the recorded generation
  transition as context. Adds STOPPED settlement mapping and a tool_sequence
  fallback for the production receipt shape.
- Robustness/security: bounded server ReadTimeout plus close-without-drain for
  body-bearing requests; deep-copied panel observation-time on ingress/egress and
  per-subscriber event clones; over-limit subscribers no longer receive a replay;
  input item sequences must be contiguous and the epoch is character-restricted
  (it is interpolated into the SSE id line); the client history is capped.
- UX/a11y: the current session is a non-interactive row; aria-expanded is set only
  while the panel is collapsible; focus leaves the panel before it is made inert;
  a dedicated status region announces gaps once; contrast uses the coss
  muted/warning token pair.
- Verified: gofmt clean, build/vet green, focused unit+race green, Node suite 38
  tests green; deterministic keyboard audits on both viewports (window-sized
  Chrome, self-describing innerWidth); full race suite green except the known
  evaluationindex docs-path environment failure.

## Web chat increment 5: third-review fixes (2026-10-13)

(superseded by the block at the top)

- Third Luna max review confirmed the fail-closed gate, textual trust markers,
  session row and uninitialised-store guard as fixed, and found 14 further gaps.
  All were addressed in 480e235: reconnect snapshot/event race (recovery closes
  the stream first), gap announcements keyed by identity, cursor/sequence
  consistency, retained-window/eviction disclosure, stricter identifier and
  control-character rules, lossy/pending classification, filter-summary wording
  (matches vs shown), correlatable activities, stream health on session events,
  production receipt no longer accepts the development bash_settlement field,
  subscription panel isolation, subscriber_limit treated as terminal, honest
  "timeout-bounded not drain-free" wording, filter summary no longer a live
  region, and the desktop Governance control hidden where it would be a no-op.
- Residual risk stated in extensions/webchat/docs/web-chat.md §6: the projection bounds
  operator-supplied prose but does not content-redact it; a production
  projection must emit structured safe summaries.
- Verified: Node suite now 40 tests; Go adds cursor/sequence, correlation,
  path-like-id, control-character, subscription-isolation and subscriber-limit
  tests; gofmt/build/vet/focused/race green; full race green except the known
  evaluationindex docs-path environment failure.
- A fourth verification round found residual gaps in these fixes (a reconnect
  refetch loop, an uncloned stream slice, a reset eviction count, and remaining
  reference-validated ids). Those were fixed in the follow-up commit: the client
  refetches on re-open without resubscribing and buffers events during the fetch,
  `subscriber_limit` closes the stream, `cloneEvent` copies the stream slice,
  `evicted` is monotonic, and result/provenance identifiers use the strict
  pattern.
- Rounds 5 and 6 completed the loop: restored the activity-kind check (lost in a
  refactor), buffered gaps during a refresh, counted snapshot-omitted elements as
  evicted, routed receipt generation/containment text through identifier
  filtering, emitted the subscription session projection before the item replay,
  carried the "reported" marker in the sessions rail, made activity kinds strict
  tokens, gave the subscription session event the current cursor (avoiding a
  false history_evicted gap on reconnect), asserted SSE event ordering, and
  stopped rendering stream health in the success colour. No round found a path
  that renders an unverified or unavailable observation as confirmed success.

## Web chat increment 6: Trajectory view (2026-10-14, codex/tbound-web-chat)

(superseded by the block at the top)

- Design reference: the DeepSeek Harness trajectory view
  (packages/client/ui-trajectory) was analysed in full
  (extensions/webchat/docs/web-chat-ui-plan.md). The harness client is 63 packages / 1,918 files /
  581 `.tsx` components over React 18, Cordis 4 and a CSS-module bundler, and its
  trajectory tab is a leaf of that composed runtime, so adopting it would have
  moved the web server onto a Node host and inverted the trust direction. Its
  *design* was ported instead; the client stays zero-build ESM/CSS over the
  loopback Go gateway. Appendix A of that document records the measurement.
- Implemented: the three-region shell (rail / workspace / inspector), the
  `Chat | Trajectory` view ring, source + density tokens, the grouped record
  ledger (`Turn`/`Tool`/`Verdict`), the record inspector (Summary / Payload /
  Decision / Result / Evidence / Timing), search over allowlisted bounded fields,
  the ordinal sequence rail, and an honest footer. New pure module
  `assets/trajectory.mjs` with 20 unit tests.
- Honesty rules enforced in code and covered by tests: the status dot mirrors the
  chip tone so the two cannot disagree; an unverified execution outcome takes the
  `reported` tone, never `ok`; `timingOf` can never return a duration; the rail's
  axis is the record sequence and says so; segment widths are bucketed CSS
  classes because the CSP forbids inline styles; the footer marks unrecorded
  metrics `unavailable` instead of zero; search reads only allowlisted bounded
  fields, so raw paths and result bytes are unmatchable; the trajectory ignores
  the timeline finding filter and cannot change it. No `Context` chip is rendered
  because the projection has no context item type.
- The trajectory view consumes the existing `tbound-webchat-observation/v1`
  contract unchanged: no new field, endpoint or supervisor capability was
  required.
- Added `TestStaticAssetsAreClosedAndComplete`, which checks the shell, the
  module graph and the closed asset allowlist against each other (the check that
  would have caught a missing `/static/trajectory.mjs` route).
- Verified: gofmt/build/vet clean; focused unit and race green; Node 65/65; full
  `go test -race ./...` green except the pre-existing `internal/evaluationindex`
  docs-path failure of a copied `supervisor/` tree. Deterministic keyboard audits
  and screenshots for both viewports refreshed in extensions/webchat/docs/webchat/qa/ (the mobile
  audit is run at a real 390 px window, because the CDP metrics override does not
  take effect here).

## Trajectory review round (2026-10-14)

(superseded by the block at the top)

- Haiku 5.5 xhigh review of 14a1772 found 10 issues; all substantiated ones are
  fixed: policy ALLOW now has its own neutral tone (never the success colour),
  lossy output never takes `ok`, the sequence rail is built from all retained
  records (search narrows only the ledger), the inspector drawer opens on record
  select at <=1180 px, Close/Escape return focus to the row, eviction clears a
  dangling selection, `role=list` removed, rail lanes labelled, skip link follows
  the visible view. A verified decision still carries no marker (latent: no
  production path emits `observed` yet).
- Tests added for verified success, UNKNOWN verdict/result, ALLOW beside lost,
  rail vs search, skip link, eviction. Node 72/72; Go 1.27.1 build/vet/gofmt clean
  and `-race` green for internal/webchat and cmd/tbound-webchat only (full
  `./...` not re-run). `handler.go` gofmt drift from 14a1772 fixed.
- Browser QA (fixture mode, Chrome over CDP): all six checks pass at 1440/1024;
  evidence and hashes in extensions/webchat/docs/webchat/qa/fixture-trajectory-capture.json. Caveat:
  Close was focused programmatically, not reached by Tab.
- Harness client survey recorded as Appendix B of extensions/webchat/docs/web-chat-ui-plan.md.
- Known: `/favicon.ico` 404 (no route); a stale `tbound-webchat.exe` (older
  build) holds port 8791 outside this worktree.

## Harness-survey increment (2026-10-14)

(superseded by the block at the top)

- Implemented from extensions/webchat/docs/web-chat-ui-plan.md App. B (patterns only, MIT upstream,
  nothing copied): safe stable-prefix assistant markdown (markdown.mjs: DOM-only,
  no HTML parsing, http/https/mailto only, 64 KiB/512 blocks/depth 4, indexed
  look-ahead so adversarial input is linear); snapshot-first reconnect (stream.mjs
  and backoff.mjs: 500 ms x2 cap 10 s, 50-100% jitter, STABLE_OPEN_MS 5 s, 3 s
  non-cancelling slow-handshake text, 15 s attempt timeout with abort, offline
  suspend, gap storms backed off); explicit windowGap cleared only by a
  contiguous covering snapshot; invalid items trigger recovery, never "duplicate";
  roving toolbars and status words in aria-labels; `--state-*` alias token tier
  (36/36 computed colours identical, tokens.test.mjs); anchored tail (anchor.mjs,
  client-only "N new records" from applied events only); UNKNOWN has its own rail tone.
- stop-sequence.mjs (two-press Escape cancel) is a pure state machine and is NOT
  wired: there is no cancel API (milestone 3). Wording never says cancelled/stopped/settled.
- Deferred: diff viewer (no bounded diff field in the projection; contract needs
  listed in extensions/webchat/docs/web-chat.md s12); slash-menu listbox (no composer). Proposal for
  the core thread: additive snapshot `anchor` (extensions/webchat/docs/web-chat-contract.md s8).
- Two Haiku 5.5 xhigh review/fix rounds: 6 defects found in 9aaa789 (ReDoS-like
  emphasis scan, hung snapshot, gap-storm reconnects, id-less item dropped, windowGap
  cleared without contiguity, offline flag), all fixed test-first.
- Verified: Node 258/258, 0 skipped; Go 1.27.1 build/vet/gofmt clean, -race green
  for internal/webchat and cmd/tbound-webchat (30 PASS, 0 SKIP). Full ./... not re-run.
  Not browser-verified: markdown styling, online/offline listeners, tail/jump button.
- Known: hung EventSource handshake (after the snapshot) is not timed out.
- Browser QA of 3146d1b (Chrome 155 over CDP, fixture/demo server, evidence in
  extensions/webchat/docs/webchat/qa/fixture-increment2-capture.json): 27 PASS, 0 FAIL — markdown
  (16 hostile payloads, no sinks, CSP clean), tail/jump (simulated fake EventSource:
  the demo has no live events), reconnect backoff/offline/slow-handshake, toolbar and
  ledger labels. Not verified: real streaming end to end, screen reader, non-Chrome,
  mobile. Open low items: gap wording "evicted from the bounded client window"
  (render.mjs:234), unreachable gateway shows "projection error: Failed to fetch"
  (render.mjs:226), the jump pill can cover the last code block, record rows lack
  roving tabindex.

## Web chat Phase 0: optional extension extraction (2026-10-14, codex/tbound-web-chat)

(superseded by the block at the top)

- The web chat is an optional standalone extension. Moved with `git mv` (renames
  staged, history kept, nothing committed): `extensions/webchat/` is module
  `tbound/webchat` (`go 1.21`, no dependencies). Code is in
  `extensions/webchat/internal/webchat/` and `extensions/webchat/cmd/tbound-webchat/`;
  docs are in `extensions/webchat/docs/`. QA JSON receipts keep their original
  paths and hashes (they describe the pre-move tree).
- Core isolation: `supervisor/` lost exactly the two webchat packages (28 -> 26).
  gofmt, build and vet are clean; `go test -race ./...` passes with the same 7
  environment skips and no FAIL. PASS count fell by 30, the webchat count.
- Extension: gofmt, vet and `go test -race` green (30 Go PASS, 0 skip). Node
  `--test` 258/258, 0 skip. Node v24.15.0 in WSL; CI pins 24.21.0.
- Boundary guard `scripts/check-extension-boundary.sh` (`--self-test`: 8 cases)
  runs in core `make test`. Separate CI workflows: `webchat-extension.yml`
  (path-filtered to `extensions/webchat/**`) and `extension-boundary.yml`
  (unfiltered). `ci.yml` is unchanged.
- ADR-0001 (`extensions/webchat/docs/adr-0001-optional-extension.md`) records the
  dependency rule and the Phase 1+ direction (React + TypeScript, own components,
  CSS Modules, committed Vite build). P1 has not started.
- Verification caveat: the pinned Go toolchain's private TMPDIR must be under
  `$HOME`, not `/tmp`. Under `/tmp` the audit-journal ancestor check fails 7
  core packages (mode 0777 ancestor). The `make` binary is not installed in WSL,
  so the Makefile recipes were run verbatim under `sh`.
- Claim boundary unchanged: G1=false; containment not-established; H1 unexecuted.

## Vision, design spec and extraction (2026-10-14)

(superseded by the block at the top)

- Web chat is an optional standalone extension (18f71e6): extensions/webchat, own Go module, boundary guard + CI; core gates need no Node and no extension.
- docs/modular-architecture-vision.md: VISION/FUTURE-WORK only. Headless/remote serve is post-thesis (02-1 s4.1 and 06-1 put service-manager modes out of scope; a surviving-owner design is needed). SO_PEERCRED over forwarded sockets is UNVERIFIED (experiment protocol written, not run). Cancel = request; the settled result comes from the supervisor. To reconcile with the core thread: web-chat-contract.md s1 orders turns/cancel before snapshot/streaming; the extension keeps read-first.
- extensions/webchat/docs/design: DeepSeek-like design language (structure only, no brand), computed WCAG AA table (calculation, not a browser render).

## Phase 1 React/TypeScript scaffold (2026-10-14)

(superseded by the block at the top)

- extensions/webchat/frontend: Vite 8.3.4, React 19.3.0, TypeScript 7.0.2, Vitest 5.0.4, exact pins,
  88 locked packages (3 runtime), ignore-scripts, 0 npm audit findings; sole install-script
  entry is the darwin-only optional fsevents (accepted explicitly in check-deps.mjs).
- dist/ committed and reproducible (clean WSL rebuild == committed, tree sha256 85f7686c...;
  Windows build byte-identical). The new UI is served under /app/ from a route table derived
  from dist/.vite/manifest.json; the vanilla UI remains the default at /.
- CSP header unchanged. Opt-in csp-smoke (Chrome 155 over CDP): PASS on / and /app/, and a
  broken-page negative control FAILS as it should. Not a required gate (manual CI job).
- Go: 36 PASS, 0 SKIP (+6 app tests); Node 258/258. make targets not executed (no make here);
  CI workflows parsed but not run on GitHub.

## Phase 2a: DOM-free logic ported to strict TypeScript (2026-10-14, uncommitted)

(superseded by the block at the top)

- frontend/src/core: contract.ts (closed vocabularies from contract.go/validate.go), validate.ts
  (strict mirror of validate.go; the only JSON-to-typed boundary), and ports of format, filters,
  store, trajectory, anchor, backoff, stream, stop-sequence, api. Not imported by main.tsx.
- Parity net: 159 legacy tests ported 1:1 (same names and assertions; counts in phase2-parity.md),
  plus 55 new (fixtures 36, boundary 8, differential 9, purity 2). Core 214 tests, 0 skipped.
- Differential: 505,366 comparisons on seeded corpora, 0 mismatches. Mutation proof: a window-gap
  mutant in a /tmp copy is caught (store run 77 step 169); an equivalent mutant is not, as it should not be.
- Legacy bugs found, not fixed: prototype keys read inherited properties (recordDot 'constructor');
  preview() can leave a lone surrogate. Go: decodeDocument downgrades authoritative/observed instead
  of refusing them, which contradicts web-chat.md 4.1.
- Gates (WSL): npm run verify 0; npm run repro 0 (tree 85f7686c...); npm run test 313/313;
  legacy node --test 258/258; Go build/vet/gofmt/test -race 0.
- Not done: UI port (Phase 3); markdown.test.mjs (56) is agent L's Phase 2b. Other uncommitted
  work in this worktree (handler.go, app.go, gate scripts) was left as found.

## Phase 3a foundation (2026-10-14)

(superseded by the block at the top)

- React shell under /app/: tokens (light/dark/system, design-language.md), primitives
  (Chip, StateDot with shape+text, Tabs, ToggleGroup, Disclosure, LiveRegion, ...),
  WorkspaceProvider (snapshot-first stream, reconnect, gaps), shell, governance panels,
  inspector drawer. Chat and Trajectory view bodies are still placeholders (3b/3c).
- Verified on WINDOWS (WSL hung, see below), not Linux: vitest 513/513 (43 files, 0 skipped),
  tsc clean, check:sinks/check:deps + self-tests, Go build/vet/test for extensions/webchat
  and core build, legacy node suite 258/258, boundary guard, csp-smoke PASS against the
  React shell in Chrome 155 (canary produced 4 violations; 7 header probes). dist/ built
  on Windows and byte-identical to P's Linux-built JS/CSS/manifest; the Linux repro and
  Go -race gates must be RE-RUN once WSL is back before calling this green per AGENTS.md.
- New dev dependency: happy-dom 21.0.1 (pinned, 97 locked packages, 0 audit findings).
- Fixed a flaky wall-clock bound in the markdown streaming perf test (work counter is now
  the real assertion).
- Environment: the WSL VM hung (wsl --shutdown also hangs; vmmem not killable without admin).

## Linux re-verification of Phase 3a (2026-10-14, after WSL was restarted)

(superseded by the block at the top)

Export of HEAD 58492c5 into WSL (Go 1.27.1, Node 24.15.0, private TMPDIR under $HOME):
- extensions/webchat: gofmt clean, vet 0, `go test -race -count=1 -v` 39 PASS 0 FAIL 0 SKIP;
  legacy node suite 258/258 0 skipped.
- frontend: `npm ci --ignore-scripts` 0 vulnerabilities; tsc 0; vitest 513/513 (43 files);
  check:sinks and check:deps with self-tests OK; a clean Linux rebuild of dist/ is byte-identical
  to the committed dist/ (which was built on Windows), so the Windows/Linux builds agree.
- core supervisor: gofmt clean, build 0, vet 0, `go test -race -v ./...` 487 PASS, 7 SKIP (same set
  as before), only FAIL = internal/evaluationindex on the git-archive export (it reads
  ../../../docs/...json, which the export omits); the same package passes (ok, 1.1s) in the real
  worktree.
- Boundary guard rule (c) now tolerates a trailing CR in go.mod (a Windows autocrlf checkout or
  `git archive` export produces CRLF); self-test 8/8.

## Phase 3b/3c: Chat view, Trajectory view, record inspector (2026-10-14)

(superseded by the block at the top)

- Chat (3b): React markdown mapper over the AST (no innerHTML; HTML-equal to the legacy
  serializer on the golden/hostile corpora), transcript role=log with aria-live=off,
  tool-call cards (ALLOW is a permission chip; unverified success is 'reported'; UNKNOWN/lost
  distinct; lossy chip), legacy filter set, posture badges, tail window.
- Trajectory (3c): toolbar, search over allowlisted fields, sequence rail built from ALL
  records, ledger with status in words and a roving tab stop, record inspector with the
  always-present Timing 'unavailable' section; drawer + focus return.
- LiveRegion source scan now allows role=log only together with aria-live=off in the same tag
  (self-tested); data-role no longer false-positives.
- Verified on LINUX (WSL restarted): vitest 721/721 (49 files), tsc 0, sinks/deps + self-tests,
  go test -race 39 PASS 0 SKIP, legacy node 258/258, boundary guard, Linux rebuild == committed
  dist. Windows: csp-smoke PASS twice (Chrome 155, observer live), QA screenshots p3b/p3c at
  1440/1024/390 light+dark with receipts in docs/webchat/qa/.
- Polish items found here (duplicate notices, repeated tool words, unverified chip noise,
  tail count across view switches, inspector name, first-load scroll at 390 px) were addressed
  in the Phase 3 polish pass below. Jump-to-latest is covered only in happy-dom (the demo has
  no live arrivals).

## Phase 3 polish (2026-10-14, uncommitted)

(superseded by the block at the top)

- Items: one notice copy, above the tabs, with a pointer in the transcript; repeated tool-name
  words removed (`chat/headline.ts`); one provenance line for uniform views and per-record marks
  for mixed ones (`chat/provenance.ts`); the tail pin kept in the workspace store across view
  switches; inspector named "Record inspector" while a record is shown; first-load scroll at
  390 px fixed (chat waits for records, as trajectory does). Each has a test that was red first.
  Details: `extensions/webchat/docs/frontend.md`, "Phase 3 polish".
- Windows (`gate.ps1`): typecheck 0; `src/app` 215/215; verify 52 files, 752 passed, 0 skipped;
  build 0; go 0; csp-smoke PASS, observer live (Chrome 155; the gateway was built with Windows
  `go`, so the toolchain was not checked). Browser QA: `scrollY` 0 after load at 390 and 1440,
  and after view switches. Receipt: `docs/webchat/qa/p3-polish-qa-receipt.json`.
- Linux (WSL, Go 1.27.1, Node 24.15.0; export of the working tree, 606 files): webchat go test
  -race 39 PASS, 0 SKIP; supervisor 488 PASS, 0 FAIL, 7 SKIP (the same environment skips;
  488 is one above the recorded 487, not reconciled); vitest 752/752; sinks 0 findings; deps OK;
  legacy node 258/258, 0 skipped; boundary guard 8/8 and OK; source hash of `frontend/src`
  (90 files) matches the QA receipt; Linux `dist/` is byte-identical to the Windows build.
- RED: `npm run repro` exits 1. HEAD's committed `dist/` (pre-polish) differs from the rebuild
  of the current source, and the CI check "committed dist matches working tree" fails the same
  way. Both should pass after the rebuilt `dist/` is committed (index.html `6e50d038…`,
  manifest `b53e6afe…`, `index-K5jt8CUl.js` replaces `index-CRVk53EO.js`). Not yet run after
  that commit.
- Not committed: commit needs the user's go-ahead. Then re-run `npm run repro`.
- Open: no screen-reader test; Jump-to-latest only in happy-dom (no live arrivals in the demo).

## Phase 3 review fixes (2026-10-14)

(superseded by the block at the top)

- Fixed (test-first): one shared verification gate (live + structural-audit projection AND item
  evidence) used by Chat, ledger, rail, inspector; a lossy success never takes the ok tone;
  LOST uses the unknown family with a diamond (distinct from DENY by shape and words); drawer no
  longer reopens on widen-then-narrow; loading and error no longer render together; absent
  provenance/panel data reads 'unavailable' not 'no'/0; StrictMode no longer duplicates the
  connection announcement.
- Verified on LINUX (working-tree copy, WSL): vitest 802/802 (54 files, 0 skipped), tsc 0,
  sinks/deps + self-tests, go -race 39 PASS 0 SKIP, legacy node 258/258, boundary guard, Linux
  rebuild == committed dist; csp-smoke PASS against a Linux-built gateway hosted in WSL (Chrome 155
  on Windows, observer live).
- Open for the core/boundary owner: parseEvent does not check the snapshot source (a streamed
  observed record in a fixture/replay projection is refused by validate.ts only for snapshots);
  core isVerified(item) judges from the item alone (the app compensates with judgedItem).
- Not browser-checked: LOST shapes, 1024 px drawer, StrictMode; no screen-reader testing.

## Core claim gate + v3-check fixes + React default (2026-10-14)

- React UI is the default at / (302 to /app/; legacy at /legacy/ with a deprecation notice; if the
  React build is unavailable / serves the legacy page). v3 mockup committed (6d03657); legacy and v2
  kept until sign-off.
- Core (A1): streamed items are admitted against the snapshot's source exactly as the Go validator
  does (24-row table); refusals trigger snapshot recovery with an invalid_item notice; one
  isVerifiedIn gate (legacy item-only check renamed isItemEvidenceOnly and grep-guarded). Deviations
  D1-D3 recorded in docs/phase2-parity.md. Closes the two open Phase 3 review items.
- UI fixes (A3): the open drawer no longer covers header controls; stream 'complete' is neutral;
  header claim values come from the projection ('unavailable' when absent; display-only line;
  warning chip for a non-live projection claiming true); three distinct notice labels; a lossy
  success never shows a green verified chip; /favicon.ico answers 204.
- Verified on LINUX: vitest 896/896 (60 files), go -race 54 PASS 0 SKIP (extension), legacy node
  258/258, sinks/deps + self-tests, boundary guard, Linux rebuild == the Windows-built dist.
- Known leftovers: the narrow drawer scrolls with the page (not pinned); stream 'complete'
  wording is a decision for the owner; no screen-reader testing.

## Governed continuation refusal / recovery-count checkpoint

- Store recovery consumes primary/admission journals; it does not restore the
  provider conversation. The fresh constructor has no history/generation/counter
  replay. A pending-only checker would still allow unsafe reuse of settled traces.
- Implemented ErrProviderRecoveryRequired: NewProviderConversation checks its
  held provider journal before source verification/credentials/broker creation.
  Any non-empty trace or Trace failure refuses construction; diagnostic causes
  are retained. Live AdmitPrompt is unchanged. Missing/closed/corrupt trace tests
  and actual constructor audit-frame tests passed after behavioral red evidence.
- Added actual durable-executor success cut (Store outcome/g1 durable), then
  process exit before ResultingExecutor appends provider_tool_result. A fresh
  Store reopen preserves g1; an effect-lifecycle sentinel detects replay. The
  same constructor helper refuses the retained journal; all journal/marker bytes
  stay unchanged. Recovery verifiers are test stubs: this proves the helper gate,
  not a recovered production authority or full constructor restart.
- Independent Luna review accepted the interim gate and narrow crash evidence.
  Required pinned Linux build/vet/format checks passed; full race986 passed,
  zero failures,7 unchanged environment skips. Focused race3 passed without skips.
  All201 Go/module files match the isolated tested source. Production binder
  currently creates fresh run-scoped Store/provider journals rather than resuming.
- Recovery documentation sync committed6834181 on codex/tbound-recovery-protocol:
  six active summaries use proposed51=17x3, still unadopted/unfrozen/unregistered;
  core workload up to1935, conditional combined1986. H1 remains299/100 and E07
  separate conformance. Independent review accepted; archives/normative draft
  unchanged. Receipt recovery-count-sync-receipt-6834181.json.
- Remaining restart work: recovered ManagedStore/D06 authority binding and exact
  provider history/generation/counter replay. The current APIs cannot establish
  a full recovered production session. G1/H1 remain unchanged.
