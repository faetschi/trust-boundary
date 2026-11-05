# Minimal web chat: where everything went

The chat is quiet by default and the supervisor's readings are one control away. Nothing was removed. A reading that matters
stays on the row it belongs to (DENY, UNKNOWN, LOST, "reported", "no result"); the rest moved behind a labelled control.
Reference: T3 Code's chat (a centred reading column, a quiet header, a rounded composer with a facts row, a small card at the top
right). Screenshots: `shadcn-p2/` (before = the previous design pass, `shadcn-p1/after-*`).

## Where each reading is now

| Reading | Was | Now |
| --- | --- | --- |
| Source, demonstration, connection | three badges in the header | **status pill** ("Demonstration · unverified", plus the connection when it is not open, plus "unverified claim true" when the projection claims one). It opens a popover with all three. |
| G1, H1, Containment, Claim-bearing, "Presentation only", "Display only" | a strip under the header | the same popover, as a two-column list with both sentences |
| Governance decisions | a column that was always open, with a scrolling strip of tabs | the column starts collapsed as an **icon rail** (one mark per panel, a state dot on each). Opened, it is a vertical list of rows: mark, label, the first badge's words, a state dot. The header **Governance** control carries the worst reading of all five panels ("1 deny verdict", or "Publication failed" when no deny). A stored choice is kept. |
| Pi version, tools reading, session id, workflow id, status | a line above the chat and a chip in the composer | **Details card**: the Pi mark, version and status at its head; the ids are under Technical details. The composer shows the provider (the Pi mark and "Pi") and the permission ("Observed only · no approvals"); the version and "tools unavailable" are in its title and in text for assistive technology. |
| Observed posture counts | chips beside the filters | one quiet line under the filters, worst first, as words in their tone (so a lost or a refusal is never behind a click) |
| Observation streams, source binding | the sessions rail | Details card, Observation streams section (its closed line says "N unavailable or gapped") |
| Retained records, verdict counts, tokens, timing, build | a footer | Details card, Retention section |
| Other-sessions note, read-only note, composer reason | the rail and the composer | Details card, Read-only view section (the composer keeps its placeholder and an assistive-technology description) |
| Window summary | the chat head | the end of the chat toolbar |
| Per-message speaker ("Pi", "User") and "reported, not verified", record link | a boxed label row | in a view whose records agree (the usual case) the speaker and the reading are not drawn on each message: the view states it once above the transcript and the words stay in the page for assistive technology. A mixed view draws both on every message. The record link and the Copy icon show on hover or focus where a pointer can hover, always on touch. |
| Tool findings | chips in boxes | words in their tone on the trace line. The group line names a verdict as one ("1 DENY verdict", "1 denied"). |
| Trajectory kind, state, "unverified" | boxed chips | words, with a hairline between rows; the sequence-axis note folds under "About this axis"; the Turn grouping says what a turn is |

The **Details** control (an icon and the word "Details" in the header) pins the card open or shut. It is shown on request only. Once pinned open it stays open while the reader uses the page: Escape and a press elsewhere do not close it, and at a wide width the workspace makes room for it so it does not cover the transcript. Its rows are a mark, a name, the worst words of that reading and a state dot; the three governance rows open their Governance panel. Technical details is a closed disclosure at its foot.

## What an operator absolutely needs (and the card shows)

Read as someone driving Pi through tbound, not as an auditor:

1. **Is this real or a demonstration, and can I trust the words?** The status pill (always visible). It carries the source, the demonstration line, the connection and the claim boundary's worst reading.
2. **What is Pi doing, and is it done?** The harness and its session state ("Pi 0.87.1 · completed") and how much has happened ("1 turn · 18 records"). A turn is one prompt and everything Pi did in response.
3. **What did the supervisor allow, refuse or leave unknown?** Decisions (a DENY or UNKNOWN is the one reading that should never need a click), then **Publication** (did anything leave) and **Recovery** (is something waiting on me). These are the three governance rows in the details card; each opens its Governance panel.
4. **Can I act here?** "Observed only · no approvals" in the composer. Nothing in this client can approve, cancel or submit.

Everything an auditor needs but an operator does not (the session and workflow ids, the source binding, the stream list, retention, tokens and timing, the read-only notes, the build) is under **Technical details** in the card, closed, and in the Governance panels. The record-level honesty reading ("reported, not verified") is stated once above the transcript when every record agrees, and per message only in a view that mixes provenance.

## What did not change

- A policy ALLOW is never drawn as a success. A request with no result reads "no result". An unverified success reads "reported".
- DENY, UNKNOWN and LOST are never behind a click: they stay on the trace line and the group line.
- Colour is never the only cue: every state is a word, and the status dots keep their shapes.
- One polite live region, no inline style, the drawer is inert while open.

## Decisions to know about

- The composer's row follows T3's: provider mark and name, then the permission, then a send mark. Reasoning is not shown: the projection carries no reasoning level, and a picker with a made-up value would be dishonest. The composer has one grey, `aria-disabled` send mark, named "Send: not available in this build". It is not a picker and has
  no caret, because a caret on a control that does nothing would be a fake affordance. There is no attach control: nothing here
  can upload.
- The details card is not persisted: it is a session choice.
- A live session keeps one standing line in its panel ("Cancel, Publish and Discard are requests; the product service decides."). The full sentence is in the details card.
- The status pill's dot: "unknown" for a dropped connection or an unverified true claim (never the refusal square, which is a policy DENY), the dashed "reported" ring otherwise. Its words put the claim boundary's reading before the connection, and they wrap instead of being cut.
- Left as they were: the trajectory axis note stays folded under "About this axis".
- "pending" on a tool proposal is still the word the ledger uses; renaming it to "proposed" was advised and is not done here.
