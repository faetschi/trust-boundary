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
| Governance decisions | a column that was always open, with a scrolling strip of tabs | the column starts collapsed as an **icon rail** (one mark per panel, a state dot on each). Opened, it is a vertical list of rows: mark, label, the first badge's words, a state dot. The header **Governance** control carries the decisions badge ("1 deny verdict"). A stored choice is kept. |
| Pi version, tools reading, session id, workflow id, status | a line above the chat and a chip in the composer | **Details card**, Session section. The composer shows the provider ("π Pi") and the permission ("Observed only · no approvals"); the version and "tools unavailable" are in its title and in text for assistive technology. |
| Observed posture counts | chips beside the filters | Details card, Observed posture section |
| Observation streams, source binding | the sessions rail | Details card, Observation streams section (its closed line says "N unavailable or gapped") |
| Retained records, verdict counts, tokens, timing, build | a footer | Details card, Retention section |
| Other-sessions note, read-only note, composer reason | the rail and the composer | Details card, Read-only view section (the composer keeps its placeholder and an assistive-technology description) |
| Window summary | the chat head | the end of the chat toolbar |
| Per-message "reported, not verified", record link | a boxed label row | a quiet row above the message; the Copy icon shows on hover or focus where a pointer can hover, always on touch |
| Tool findings | chips in boxes | words in their tone on the trace line. The group line names a verdict as one ("1 DENY verdict", "1 denied"). |
| Trajectory kind, state, "unverified" | boxed chips | words; the sequence-axis note folds under "About this axis" |

The **Details** control (an icon in the header) opens the card. It is shown on request only, so it never covers the transcript
unasked. Every section is a native disclosure, and its closed line states its worst fact.

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
- "pending" on a tool proposal is still the word the ledger uses; renaming it to "proposed" was advised and is not done here.
