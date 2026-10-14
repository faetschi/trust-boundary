# Phase 3: parity table (legacy UI tests to React tests)

Status: Phase 3a (foundation) in progress. Each row maps one legacy test in
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
| 1 | renderAll completes without throwing and shows the transcript | 3b | Transcript rendering. Placeholder: `app/views/ChatView.tsx`. |
| 2 | unverified success never renders with the confirmed-success chip | 3a + 3b | Partial. The panel side is `app/panels.test.ts` (publication and state readings). The transcript side belongs to 3b. |
| 3 | unverified publication is presented as reported, not published | 3a | `app/Shell.test.tsx` "shows an unverified publication as reported, never as published"; `app/panels.test.ts` "shows an unverified publication as reported, never as published". |
| 4 | inspector renders bounded key/value rows and the claim boundary | 3a | Partial. Claim boundary: `app/Shell.test.tsx` "shows the claim boundary before any snapshot arrives". Rows: `app/panels.test.ts` (governance, provenance and qualification rows). The decision rows are asserted on the panel model, not on the DOM. |
| 5 | unverified panel and session claims carry an explicit textual trust label | 3a | `app/panels.test.ts` ("reported by the source, not independently verified"; the session reading). |
| 6 | the sessions rail carries the unverified marker | 3a | Partial. `app/panels.test.ts` covers the session reading. The rail's DOM text is not asserted for a `completed` session. |
| 7 | the current session is a non-interactive row, not a dead control | 3a | `app/Shell.test.tsx` "keeps the rail read-only: it has no buttons, links or controls". |
| 8 | snapshot continuity gaps are surfaced as explicit notices | 3a | `app/workspaceStore.test.ts` "a server gap recovers from a fresh snapshot and says so"; `app/status.test.ts` (gap text, announced once). |
| 9 | loading state renders a spinner and skeleton rows | 3a | Not the same design. `ui/StateMessage.test.tsx` "marks a loading state busy": text plus `aria-busy`, with no spinner or skeleton. |
| 10 | an unloaded projection shows no session and no fabricated rows | 3a | `app/Shell.test.tsx` "shows no session and no rows before a projection is loaded". |
| 11 | toolbar renders the filter toggle group with pressed state and counts | 3a + 3b | `ui/ToggleGroup.test.tsx` (pressed state, counts, one tab stop). The filter toolbar itself is 3b. |
| 12 | toolbar posture separates policy ALLOW from reported execution | 3b | Posture is in Chat. The footer's policy counts are in `app/Footer.test.ts`. |
| 13 | selecting the denied filter hides non-matching blocks and messages | 3b | Filter logic is core (`core/filters.ts`, P2). The view is 3b. |
| 14 | a filter with no matches renders the empty-filter state | 3b | The view is 3b. `ui/StateMessage.test.tsx` covers the empty-state primitive. |
| 15 | the ledger renders one record row per projected element, with a source chip | 3c | Ledger. Primitive: `ui/Chip.test.tsx` (source chips). |
| 16 | the ledger never renders an unverified success as confirmed | 3c | Ledger rows. Primitives: `ui/Chip.test.tsx`, `ui/StateDot.test.tsx`. |
| 17 | selecting a record swaps the aside to the record inspector for that record | 3a + 3c | Partial. The slot, the close control and the focus contract are in `app/Inspector.test.tsx`. The record body is 3c (`app/inspector/RecordInspector.tsx`). |
| 18 | the sequence rail declares itself ordinal and never draws a duration | 3c | Sequence rail. |
| 19 | the footer marks unrecorded metrics unavailable instead of zero | 3a | `app/Footer.test.ts` ("marks the metrics the projection does not record as unavailable, never as zero"); `app/Shell.test.tsx` "shows the footer's untracked metrics as unavailable". |
| 20 | the ledger ignores the timeline finding filter but honours the search | 3c | Ledger. |
| 21 | grouping by tool and verdict reorganises the same records | 3c | Ledger grouping. |
| 22 | switching to the trajectory view leaves the projection untouched | 3a + 3c | Partial. `app/workspaceStore.test.ts` "sets the view, query and group, and selects a record as a toggle" checks the state; it does not assert that items are unchanged. |
| 23 | search narrows the ledger but never the governance rail | 3c | Ledger search. |
| 24 | the skip link follows the visible view | 3a | `app/Shell.test.tsx` "switches the view with the tab and moves the skip link to the visible view". |
| 25 | assistant text renders markdown while the same text from the user stays literal | 3b | React mapping of the core AST (`src/markdown/`, P2). |
| 26 | assistant markup from the model is literal, not a link or element | 3b | Markdown security. The AST is covered by `src/markdown/security.test.ts` (P2). |
| 27 | a streaming assistant message keeps its completed blocks across conversation renders | 3b | Incremental render. The AST is covered by `src/markdown/incremental.test.ts` (P2). |
| 28 | the markdown container is pruned once its message leaves the retained window | 3b | Retained-window pruning. |
| 29 | a window replaced without provable continuity shows an explicit history-not-loaded notice | 3a | Partial. The notice text is `app/status.ts` `WINDOW_GAP_TEXT`, and the window logic is in `core/store.ts` (P2). A DOM assertion for this notice is not written yet. |
| 30 | a history_evicted gap states the earlier-history fact in its own notice, without a duplicate | 3a | Partial. `app/status.test.ts` "announces a new gap once, and a new window gap once". The DOM notice count is not asserted. |
| 31 | a loaded window with continuity shows no history-not-loaded notice | 3a | Partial. The window logic is in `core/store.ts` (P2). The DOM absence is not asserted. |
| 32 | the connection status reads connected only when open, and the slow note is text | 3a | `app/status.test.ts` ("names connected only for an open stream", the slow-handshake note); `app/workspaceStore.test.ts` "reads connecting until the stream opens, and connected only after it does". |
| 33 | an offline stream is labelled offline in text | 3a | `app/workspaceStore.test.ts` "goes offline without a retry timer"; `app/WorkspaceProvider.test.tsx` "goes offline when the window does". |
| 34 | every ledger row states its status in text, and never calls an ALLOW or an unverified success a success | 3c | Ledger row labels. Primitives: `ui/StateDot.test.tsx` (the dot is `aria-hidden`), `app/panels.test.ts` (the reading words). |
| 35 | each rail lane names its segment tones in text, including an UNKNOWN segment | 3c | Sequence rail lanes. |
| 36 | each toolbar is a single tab stop on its pressed button | 3a | `ui/ToggleGroup.test.tsx` "keeps exactly one button in the tab order". |
| 37 | transcript messages, tool blocks and ledger rows carry the anchor key the tail window pins to | 3b + 3c | Anchor logic is core (`core/anchor.ts`, P2). The DOM keys are 3b and 3c. |

## shell.test.mjs (4)

| # | Legacy test | Owner | New test, or the reason it is not yet covered |
|---|---|---|---|
| S1 | the timeline filter and the trajectory grouping are toolbars with a label | 3a | `ui/ToggleGroup.test.tsx` "is a labelled toolbar of buttons that state their pressed state and counts". The Chat and Trajectory toolbars are 3b and 3c, and they use this primitive. |
| S2 | the shell declares no on/off switch: every control is a button or a tab | 3a | `ui/ToggleGroup.test.tsx` "is a button group, not a switch". `app/Shell.test.tsx` covers the theme toolbar. |
| S3 | each view has a text-labelled jump control, hidden until records arrive below a scrolled reader | 3b + 3c | Jump-to-latest is in the views. The label and the arithmetic are core (`core/anchor.ts`, P2). |
| S4 | the ledger is a focus target for the jump control, as the transcript already is | 3b + 3c | The views own their focus targets. The shell's tab panels are focusable (`ui/Tabs.tsx`, `tabIndex=-1`). |

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
