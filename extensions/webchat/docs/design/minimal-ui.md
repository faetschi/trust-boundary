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
| Pi version, tools reading, session id, workflow id, status | a line above the chat and a chip in the composer | **Details card**, Session section. The composer shows the provider ("π Pi") and the permission ("Observed only · no approvals"); the version and "tools unavailable" are in its title and in text for assistive technology. |
| Observed posture counts | chips beside the filters | one quiet line under the filters, worst first, as words in their tone (so a lost or a refusal is never behind a click) |
| Observation streams, source binding | the sessions rail | Details card, Observation streams section (its closed line says "N unavailable or gapped") |
| Retained records, verdict counts, tokens, timing, build | a footer | Details card, Retention section |
| Other-sessions note, read-only note, composer reason | the rail and the composer | Details card, Read-only view section (the composer keeps its placeholder and an assistive-technology description) |
| Window summary | the chat head | the end of the chat toolbar |
| Per-message "reported, not verified", record link | a boxed label row | a quiet row above the message; the Copy icon shows on hover or focus where a pointer can hover, always on touch |
| Tool findings | chips in boxes | words in their tone on the trace line. The group line names a verdict as one ("1 DENY verdict", "1 denied"). |
| Trajectory kind, state, "unverified" | boxed chips | words; the sequence-axis note folds under "About this axis" |

The **Details** control (an icon and the word "Details" in the header) opens the card. It is shown on request only, so it never covers the\ntranscript unasked. It behaves as the status pill's popover does: opening it moves focus into it, Escape or a press outside closes it,\nand Escape returns focus to the control. It follows the header in the DOM, so Tab reaches it from its control. Every section is a\nnative disclosure, and its closed line states its worst fact; Session is open first (the Pi version, session id and workflow id are the\naudit identifiers).

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
