# Phase 3: parity table (legacy UI tests to React tests)

Status: Phases 3a, 3b and 3c are in place, the polish pass is recorded in `frontend.md`
(Phase 3 polish), and the claim-boundary review fixes are recorded there too (Phase 3 review fixes,
2026-10-14; rows R1 to R8 at the end of this file). Each row maps one legacy test in
`internal/webchat/jstest/` to the React test that now asserts the same behaviour,
or names the agent that owns it. Owners: **3a** (this phase, shell and governance
panels), **3b** (Chat view, agent Q), **3c** (Trajectory view, agent R). A row marked
**partial** says what is asserted and what is not. Tests named here are in
`extensions/webchat/frontend/src/`. Paths below are relative to `src/`.

Legacy totals: `render.test.mjs` 37, `shell.test.mjs` 4, `tokens.test.mjs` 2, none
skipped. The core port (`store`, `stream`, `anchor` and the rest) is the separate
P2 table in `phase2-parity.md`.

The legacy tests run in Node with a DOM stub. The React tests run in happy-dom, which
is also not a browser. Nothing here is a visual or layout check. Those are in the
Phase 3a section of `frontend.md`.

## render.test.mjs (37)

| # | Legacy test | Owner | New test, or the reason it is not yet covered |
|---|---|---|---|
| 1 | renderAll completes without throwing and shows the transcript | 3b | Transcript rendering: `app/views/ChatView.test.tsx` "renders the transcript, with the user message and the tool block". |
| 2 | unverified success never renders with the confirmed-success chip | 3a + 3b | Partial. The panel side is `app/panels.test.ts` (publication and state readings). The transcript side: `app/views/ChatView.test.tsx` "shows an unverified success as reported, never as succeeded or in the success tone"; `app/views/chat/readings.test.ts` "shows an unverified success as reported, never succeeded and never the success tone". |
| 3 | unverified publication is presented as reported, not published | 3a | `app/Shell.test.tsx` "shows an unverified publication as reported, never as published"; `app/panels.test.ts` "shows an unverified publication as reported, never as published". |
| 4 | inspector renders bounded key/value rows and the claim boundary | 3a | Partial. Claim boundary: `app/Shell.test.tsx` "shows the claim boundary before any snapshot arrives". Rows: `app/panels.test.ts` (governance, provenance and qualification rows). The decision rows are asserted on the panel model, not on the DOM. Absent provenance and absent evidence read unavailable, never no: `app/panels.test.ts` "absent input reads unavailable, never no, zero, none, complete or ok"; `app/inspector/RecordInspector.test.tsx` "reads absent authority and verification as unavailable" (review fix 6). |
| 5 | unverified panel and session claims carry an explicit textual trust label | 3a | `app/panels.test.ts` ("reported by the source, not independently verified"; the session reading). |
| 6 | the sessions rail carries the unverified marker | 3a | Partial. `app/panels.test.ts` covers the session reading. The rail's DOM text is not asserted for a `completed` session. |
| 7 | the current session is a non-interactive row, not a dead control | 3a | `app/Shell.test.tsx` "keeps the rail read-only: it has no buttons, links or controls". |
| 8 | snapshot continuity gaps are surfaced as explicit notices | 3a | `app/workspaceStore.test.ts` "a server gap recovers from a fresh snapshot and says so"; `app/status.test.ts` (gap text, announced once). |
| 9 | loading state renders a spinner and skeleton rows | 3a | Not the same design. `ui/StateMessage.test.tsx` "marks a loading state busy": text plus `aria-busy`, with no spinner or skeleton. Precedence (review fix 5): `app/Shell.views.test.tsx` "projection states: loading, failure and recovery are never shown together". A first-load failure replaces the skeleton, and a recovery shows the skeleton only with no data. |
| 10 | an unloaded projection shows no session and no fabricated rows | 3a | `app/Shell.test.tsx` "shows no session and no rows before a projection is loaded". Every panel, rail and footer value reads unavailable before the first snapshot: `app/Shell.test.tsx` "before the first snapshot, every panel, rail and footer value reads unavailable" (review fix 6). |
| 11 | toolbar renders the filter toggle group with pressed state and counts | 3a + 3b | `ui/ToggleGroup.test.tsx` (pressed state, counts, one tab stop). The filter toolbar: `app/views/ChatView.test.tsx` "renders the filter toolbar with pressed state and counts". |
| 12 | toolbar posture separates policy ALLOW from reported execution | 3b | Posture: `app/views/ChatView.test.tsx` "separates policy ALLOW from reported execution in the posture badges"; the words and tones in `app/views/chat/readings.test.ts`. The footer's policy counts are in `app/Footer.test.ts`. |
| 13 | selecting the denied filter hides non-matching blocks and messages | 3b | Filter logic is core (`core/filters.ts`, P2). The view: `app/views/ChatView.test.tsx` "hides non-matching blocks and messages when the Denied filter is pressed". |
| 14 | a filter with no matches renders the empty-filter state | 3b | `app/views/ChatView.test.tsx` "renders the empty-filter state when no element matches". `ui/StateMessage.test.tsx` covers the empty-state primitive. |
| 15 | the ledger renders one record row per projected element, with a source chip | 3c | `app/views/TrajectoryView.test.tsx` "renders one record row per projected element, in sequence order, with a source chip". Primitive: `ui/Chip.test.tsx`. |
| 16 | the ledger never renders an unverified success as confirmed | 3c | `app/views/TrajectoryView.test.tsx` "never renders an unverified success as confirmed, and gives the reported tone to its dot"; "a verified success reads succeeded, and a lossy one takes the unknown tone and is not counted as confirmed". Primitives: `ui/Chip.test.tsx`, `ui/StateDot.test.tsx`. One gate on every surface (review fix 1, `app/verification.surfaces.test.tsx`): a streamed observed success in a fixture projection reads reported in the rows, the rail and the inspector. A verified lossy success reads `succeeded (output lossy, not confirmed)` on the unknown tone (review fix 2). |
| 17 | selecting a record swaps the aside to the record inspector for that record | 3a + 3c | Partial. The slot, the close control and the focus contract are in `app/Inspector.test.tsx` (3a). The 3c part: `app/views/TrajectoryView.test.tsx` "swaps the aside to the record inspector for the selected record, with a duration stated as unavailable", the drawer and focus-return tests under "selecting a record". The body sections are in `app/inspector/RecordInspector.test.tsx`. The aside is named "Record inspector" while a record is shown, and "Governance inspector" otherwise: `app/Shell.views.test.tsx` "is 'Record inspector' while a record is shown…". The drawer closes on leaving the narrow layout and does not reopen by itself (review fix 4): `app/Shell.test.tsx` "does not reopen after a widen and a narrow again". |
| 18 | the sequence rail declares itself ordinal and never draws a duration | 3c | `app/views/TrajectoryView.test.tsx` "has one lane each for Model, Policy and Tools, with an ordinal axis note"; "draws every segment as a weight class with no inline style (CSP)". |
| 19 | the footer marks unrecorded metrics unavailable instead of zero | 3a | `app/Footer.test.ts` ("marks the metrics the projection does not record as unavailable, never as zero"); `app/Shell.test.tsx` "shows the footer's untracked metrics as unavailable". Before the first snapshot the retained-window counts are unavailable too (review fix 6): `app/Footer.test.ts` "reads every retained-window count as unavailable before a projection is loaded". |
| 20 | the ledger ignores the timeline finding filter but honours the search | 3c | Search: `app/views/TrajectoryView.test.tsx` "narrows the ledger on search, and says how many of how many match" and "searches only allowlisted bounded fields: a policy digest is not searchable". The finding filter is a Chat control (3b), so the view cannot set it here. Its independence is `core/trajectory.test.ts` "the trajectory ignores the timeline finding filter". |
| 21 | grouping by tool and verdict reorganises the same records | 3c | `app/views/TrajectoryView.test.tsx` "groups by Turn, Tool or Verdict over the same records, and never drops one"; "keeps exactly one group button in the tab order, on the pressed group". |
| 22 | switching to the trajectory view leaves the projection untouched | 3a + 3c | Partial. `app/workspaceStore.test.ts` "sets the view, query and group, and selects a record as a toggle" checks the state. 3c: `app/views/TrajectoryView.test.tsx` "switching to the trajectory view and grouping or searching never changes the records" asserts the records and their count. |
| 23 | search narrows the ledger but never the governance rail | 3c | `app/views/TrajectoryView.test.tsx` "narrows the ledger but never the rail, which still shows the DENY and every retained record". |
| 24 | the skip link follows the visible view | 3a | `app/Shell.test.tsx` "switches the view with the tab and moves the skip link to the visible view". |
| 25 | assistant text renders markdown while the same text from the user stays literal | 3b | `markdown/react.test.tsx` (every AST node renders to its element; parity with the legacy serializer). `app/views/ChatView.test.tsx` "renders assistant text as markdown and the same text from the user as literal". |
| 26 | assistant markup from the model is literal, not a link or element | 3b | `markdown/react.test.tsx` "renders as inert DOM" and "renders as inert server markup" for every hostile payload. `app/views/ChatView.test.tsx` "renders assistant markup from the model as literal text, never as an element". The AST: `markdown/security.test.ts` (P2). |
| 27 | a streaming assistant message keeps its completed blocks across conversation renders | 3b | `markdown/react.test.tsx` "skips leading blocks whose node objects are unchanged, and renders the changed one" (render counter). `app/views/ChatView.test.tsx` "keeps the completed blocks of a streaming assistant message across renders" (DOM node identity). |
| 28 | the markdown container is pruned once its message leaves the retained window | 3b | `app/views/ChatView.test.tsx` "prunes the markdown container once its message leaves the retained window". |
| 29 | a window replaced without provable continuity shows an explicit history-not-loaded notice | 3a | Partial. The notice text is `app/status.ts` `WINDOW_GAP_TEXT`, and the window logic is in `core/store.ts` (P2). The DOM: `app/Shell.views.test.tsx` "shows a gap, a window gap and an eviction once each, in the chat view and in the trajectory view" (one copy in each view); `app/views/ChatView.test.tsx` "does not repeat the window notice…". |
| 30 | a history_evicted gap states the earlier-history fact in its own notice, without a duplicate | 3a | Partial. `app/status.test.ts` "announces a new gap once, and a new window gap once". The DOM count is asserted by `app/Shell.views.test.tsx` "shows a gap, a window gap and an eviction once each…" (one copy, in each view). |
| 31 | a loaded window with continuity shows no history-not-loaded notice | 3a | Partial. The window logic is in `core/store.ts` (P2). The transcript's absence is asserted by `app/views/ChatView.test.tsx` "shows no earlier-history notice for a loaded window with continuity". The shell's absence is not asserted. |
| 32 | the connection status reads connected only when open, and the slow note is text | 3a | `app/status.test.ts` ("names connected only for an open stream", the slow-handshake note); `app/workspaceStore.test.ts` "reads connecting until the stream opens, and connected only after it does". Under StrictMode the connection is announced once (review fix 7): `app/WorkspaceProvider.test.tsx` "speaks the connection exactly once under StrictMode". |
| 33 | an offline stream is labelled offline in text | 3a | `app/workspaceStore.test.ts` "goes offline without a retry timer"; `app/WorkspaceProvider.test.tsx` "goes offline when the window does". |
| 34 | every ledger row states its status in text, and never calls an ALLOW or an unverified success a success | 3c | `app/views/TrajectoryView.test.tsx` "states each row's status in words, and never calls an ALLOW or an unverified success a success"; "a policy ALLOW beside a lost result: no green or ok tone anywhere on the rail or in a dot"; "UNKNOWN verdict and UNKNOWN result stay unknown and are never promoted". Primitive: `ui/StateDot.test.tsx` (the dot is `aria-hidden`). |
| 35 | each rail lane names its segment tones in text, including an UNKNOWN segment | 3c | `app/views/TrajectoryView.test.tsx` "names each lane's segment tones in words"; "gives an UNKNOWN result its own unknown segment, named in words, never a lane that did not act"; "draws a message as idle in the Model lane". LOST has its own lane words, `LOST, not success`, and is never drawn as a DENY segment (review fix 3): `app/views/TrajectoryView.test.tsx` "a LOST result beside a DENY differs by tone and by words". |
| 36 | each toolbar is a single tab stop on its pressed button | 3a | `ui/ToggleGroup.test.tsx` "keeps exactly one button in the tab order". |
| 37 | transcript messages, tool blocks and ledger rows carry the anchor key the tail window pins to | 3b + 3c | `app/views/ChatView.test.tsx` "anchors each message and tool card with the key the tail window pins to". The ledger keys are 3c: `app/views/TrajectoryView.test.tsx` "carries the anchor key the tail window pins to, on every row". |

## shell.test.mjs (4)

| # | Legacy test | Owner | New test, or the reason it is not yet covered |
|---|---|---|---|
| S1 | the timeline filter and the trajectory grouping are toolbars with a label | 3a | `ui/ToggleGroup.test.tsx` "is a labelled toolbar of buttons that state their pressed state and counts". The Chat and Trajectory toolbars are 3b and 3c, and they use this primitive. |
| S2 | the shell declares no on/off switch: every control is a button or a tab | 3a | `ui/ToggleGroup.test.tsx` "is a button group, not a switch". `app/Shell.test.tsx` covers the theme toolbar. |
| S3 | each view has a text-labelled jump control, hidden until records arrive below a scrolled reader | 3b + 3c | The chat view: `app/views/ChatView.test.tsx` "counts the records applied while a reader is away, and offers Jump to latest"; "counts only applied records: a duplicate or a gap never adds to the count"; "follows new records for a reader at the tail". The control is not rendered until a count exists, rather than hidden; the observable state is the same. The arithmetic is `core/anchor.ts` (P2). Partial: the scroll position on a real page is checked in the browser QA only (`frontend.md`, 3b). The ledger (3c): `app/views/TrajectoryView.test.tsx` "shows no count while at the tail, and counts live records once the reader is away"; "a duplicate of a record already applied adds nothing to the count"; "announces a settled burst of arrivals once, through the status region". The pin is kept by the store across a view switch, so the count carries into the other view: `app/Shell.views.test.tsx` "carries the count into the trajectory view and back, and clears it at the tail" (polish). |
| S4 | the ledger is a focus target for the jump control, as the transcript already is | 3b + 3c | The transcript: `app/views/ChatView.test.tsx` "moves the reader to the tail, and focus to the transcript, when Jump is pressed". The ledger (3c): `app/views/TrajectoryView.test.tsx` "Jump to latest scrolls to the tail and moves focus to the ledger". The ledger is a named region with `tabIndex=-1`. |

## tokens.test.mjs (2)

| # | Legacy test | Owner | New test, or the reason it is not yet covered |
|---|---|---|---|
| T1 | every state alias is declared in :root and names a palette token or a literal | 3a | `styles/tokens.test.ts` "alias tier" (every `var()` a component reads is declared in `tokens.css`). Contrast of every state pair: `styles/tokens.test.ts` "contrast". |
| T2 | state chips, dots and segments read only the state aliases, never the palette | 3a | `styles/tokens.test.ts` "takes colour only from alias tokens", for every component stylesheet, and "reads no static ramp token and no literal colour". |

## Deliberate deviations from the legacy UI

These are changes of behaviour, not gaps. Each one follows the spec or a legacy rule
that the legacy code did not fully apply.

1. **Unverified publication.** The legacy panel showed the raw word `published` with
   a reported chip and the marker "reported, not verified". The React panel shows the
   word `reported`, with the sentence "reported by the source, not independently
   verified". The spec says an unverified publication is `reported`, not `published`.
2. **Absent governance counts.** The legacy panel rendered an absent count as `0`
   (its own comment says it must not). The React panel renders "unavailable" with a
   reason. Zero is shown only for a count the snapshot carries.
3. **Drawer focus.** The legacy drawer did not move focus into itself on open. The
   React drawer moves focus to the inspector heading. Closing returns focus to the
   opener: the toggle, or the record row when one was selected.
4. **Skip link.** The legacy link always pointed at the chat transcript. The React
   link points at the visible view, as the spec asks.
5. **Tab keys.** Arrow keys move and select between tabs (automatic activation), and
   Home and End jump to the ends. The legacy code handled only the arrows.
6. **Source badge.** Shows the source word, and "· unverified" when the projection is
   not a verified one. The legacy badge showed the word alone.
7. **Pending chip.** Amber, as `web-chat.md` section 4.4 requires. The pending dot is
   a grey hollow ring, so the two differ in colour as that section describes. The
   legacy pending chip's colour was not checked against this rule.
8. **Theme.** Light by default, with a system, light and dark toggle. The legacy UI
   was dark only.

## Not verified by these tests

- Layout, focus rings on screen, forced-colours rendering and the no-flash theme
  start. These need a browser. See `frontend.md`, Phase 3a.
- Screen-reader output. The announcer is tested as a store and as a DOM node, not
  with a screen reader.

## Claim-boundary review fixes (2026-10-14)

Rows for the eight review findings. Each names the test that reproduced the finding (RED, before the
fix) and the file that now holds it. The GREEN run is `gate.ps1 -Task verify` (Windows), in
`frontend.md`, "Phase 3 review fixes".

| # | Finding | Owner | Test (RED before the fix) |
|---|---|---|---|
| R1 | one verification gate: a streamed observed success in a fixture projection reads reported on every surface | 3b + 3c | `app/verification.surfaces.test.tsx` "the trajectory row, its dot and the sequence rail read the streamed success as reported, never ok"; "the record inspector reads independently verified: no, with the state as reported". Gate: `app/verification.test.ts`. |
| R2 | a verified lossy success is never the ok tone; it reads `succeeded (output lossy, not confirmed)` | 3b + 3c | `app/views/ChatView.test.tsx` "never gives a verified lossy success the ok tone"; `app/views/TrajectoryView.test.tsx` "a verified success reads succeeded, and a lossy one takes the unknown tone"; `app/views/chat/readings.test.ts`. |
| R3 | LOST is in the unknown family with its own shape, never a DENY; the words `LOST, not success` stay | 3c | `app/views/TrajectoryView.test.tsx` "a LOST result beside a DENY differs by tone and by words"; `ui/StateDot.test.tsx` "draws LOST in the unknown family with a diamond of its own"; `ui/Chip.test.tsx` "LOST is in the unknown family, with a shape of its own"; contrast rows in `styles/tokens.test.ts`. |
| R4 | the drawer does not reopen after a widen and a narrow | 3a + 3c | `app/Shell.test.tsx` "does not reopen after a widen and a narrow again"; "a record selected at narrow width opens the drawer; after a widen and a narrow the drawer is closed and focus is not in it". |
| R5 | loading and error are never shown together; a recovery shows the skeleton only with no data | 3a + 3b + 3c | `app/Shell.views.test.tsx` "projection states: loading, failure and recovery are never shown together" (four tests). |
| R6 | an absent value reads unavailable with its reason, never no, zero, none, complete or ok | 3a + 3b + 3c | `app/panels.test.ts` "absent input reads unavailable, never no, zero, none, complete or ok"; `app/Footer.test.ts` "reads every retained-window count as unavailable before a projection is loaded"; `app/Shell.test.tsx` "before the first snapshot, every panel, rail and footer value reads unavailable"; `app/Shell.views.test.tsx` "before the first snapshot, no count reads as zero"; `app/inspector/RecordInspector.test.tsx` "reads absent authority and verification as unavailable". |
| R7 | StrictMode does not announce the connection twice | 3a | `app/WorkspaceProvider.test.tsx` "speaks the connection exactly once under StrictMode" (the control without StrictMode passes before and after). |
| R8 | docs: the stale LiveRegion note and the stale 3b and 3c statements are corrected, each dated | docs | `frontend.md`, the Phase 3c gates note, "Open" in 3b, the 3c chips paragraph and deviation 1, and the polish receipts. |

The LiveRegion scan that allows `role="log"` only with `aria-live="off"` was changed in commit 57bcd95,
not in the review pass. Its self-test is in `ui/LiveRegion.test.tsx`.

## Fidelity-check fixes, A3 (2026-10-14)

Rows for the six defects the v3 fidelity check found. Each names the test that reproduced the defect (RED, before the fix) and the file that now holds the fix. The receipts are at the end of this section.

| # | Defect | Test (RED before the fix) | Fix now in |
|---|---|---|---|
| A3-1 | At 1180 px and below, the open governance drawer covered the header's theme switch and the Governance toggle. | `app/Shell.test.tsx` "places the drawer in the body that follows the header, positioned at the top of that body": the body had no `position: relative`. Happy-dom has no layout, so the browser hit-test is in the receipts. | `app/Shell.module.css`: at the narrow breakpoint the workspace body is the drawer's containing block, and it clips the closed drawer. `app/Inspector.module.css`: the open drawer is absolute at the body's top, which is the header's bottom edge. |
| A3-2 | The rail showed a completed stream with the success dot and the word "complete". | `app/Rail.test.tsx` "reads a complete stream as complete (source did not claim completeness), on the neutral tone…", for fixture, replay and live (RED: the wording and the dot). | `app/Rail.tsx`: `complete` takes the `idle` tone and reads "complete (source did not claim completeness)". |
| A3-3 | The header claim strip hard-coded G1, H1, containment and claim-bearing. | `app/ClaimBoundary.test.tsx` (before load, loaded, mismatch, absent panel, missing field); `app/Shell.test.tsx` "never shows a projected true as a pass…" (RED: literal values, no display-only line). | `app/ClaimBoundary.tsx` (new): the four values from `qualificationRows`, unavailable with a reason, the display-only line, and the "unverified projection claims true" chip on a true G1 or claim-bearing value from an unverified projection. `app/panels.ts`: `qualificationRows` takes the reason for an absent panel. |
| A3-4 | Two notices shared the label "Retained window": the window gap and the eviction count. | `app/Shell.views.test.tsx` "names the continuity gap, the window and the eviction as three different notices" (RED: both named "Retained window"). | `app/status.ts`: `WINDOW_GAP_LABEL` "History not loaded" and `EVICTED_LABEL` "Records no longer retained". `app/Shell.tsx` uses both. |
| A3-5 | In a mixed view, a verified lossy success showed a green "verified" chip beside "succeeded (output lossy, not confirmed)". | `app/views/ChatView.test.tsx` "gives a verified lossy success in a mixed view a neutral evidence marker, never the green verified chip" (RED: `["verified", "ok"]` present). `app/views/chat/readings.test.ts` "marks a verified lossy success as verified evidence…" (RED: no such reading). | `app/views/chat/readings.ts`: `LOSSY_EVIDENCE_READING`, "verified evidence, output lossy", on the idle tone. `app/views/chat/ToolCard.tsx` uses it when the result is lossy. |
| A3-6 | Every page load requested `/favicon.ico` and logged a 404. | `internal/webchat/favicon_test.go`, "TestFaviconAnswersNoContentForGetAndHead" (RED: GET 404, HEAD 405). | `internal/webchat/handler.go`: `/favicon.ico` answers 204 to GET and HEAD, with the same security headers and Host/Origin pinning. Other methods get 405 with `Allow: GET, HEAD`. |

### What a reader will notice

- The header claim values use the drawer's labels: "Containment" and "Claim-bearing". The literal values are gone. Before the first projection, each value reads `unavailable (no projection loaded)`.
- The open drawer is anchored to the workspace body, not the viewport, so it scrolls with the page. This is a behaviour change, and it was not checked with the page scrolled.
- `App.test.tsx` (first paint) now asserts the unavailable reading instead of `not-established`. That file was outside the A3 list; the change follows from A3-3.
- The favicon allowance in `extensions/webchat/scripts/csp-smoke.mjs` (`isAllowedFavicon404`) is now dead. Remove it in a later pass.
- Unchanged: the toggle's `aria-expanded`, Escape closing the drawer with focus returned to the toggle, and the focus order. The existing tests cover them.

### Receipts (2026-10-14)

- Windows, `gate.ps1 -Task verify`: exit 0. Typecheck clean. Vitest: 60 files, 896 tests passed, 0 skipped (the run reports no skip count). `check:sinks`: 0 findings. `check:deps`: OK. The run includes A1's edits as they stood at that time.
- Windows, `gate.ps1 -Task go`: exit 0. `go vet` clean, both packages `ok`, `gofmt -l` empty.
- WSL, go1.27.1 linux/amd64, `TMPDIR=$HOME/tmp-tbwc-a3`, umask 077: `go build ./...` exit 0; `go vet -mod=readonly ./...` exit 0; `gofmt -l .` empty; `go test -race -mod=readonly -count=1 -v ./...` exit 0, 132 PASS, 0 FAIL, 0 SKIP. The four favicon tests pass. sha256: `internal/webchat/handler.go` 4046cbefaa1b99f0cc8ec07157c43b4de19016ba2f9bff40af4b7b1641119158; `internal/webchat/favicon_test.go` 97e1a083bde2d8dc883753ccadec17ea492e17b28d54fd293dd5fa4078ef1372.
- Browser, Windows (preview tool, not a test): a Vite dev server from the gate's scratch copy, with no gateway and no `dist/`. At 1024×768 with the drawer open, `elementFromPoint` at the centre of Governance, Light, Dark and System returns each control, and the drawer top equals the header bottom (165 px). With the old placement forced in the page (`position: fixed; top: 0`), the same probe found the drawer over all four. At 700×800 the drawer top equals the header bottom (225 px), and there is no horizontal overflow with the drawer closed or open.

### Not done in this pass

- The v3 mockups refresh and the `dist/` rebuild, which belong to other passes.
- Stale HEAD wording: `extensions/webchat/README.md` line 30 and `extensions/webchat/docs/web-chat.md` lines 337 and 343 say only `/` answers HEAD. `/favicon.ico` now does too. Not edited here.
