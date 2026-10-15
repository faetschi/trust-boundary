# TBound — current state

Updated 2026-10-15. Branch: `codex/tbound-prototype`. Priority: thesis completion.

| Milestone | Current state |
| --- | --- |
| Recovery foundations | Durable fencing, cumulative retry/restart accounting, historical checkpoint verification and bounded provider exchanges committed. |
| SQLite safety | File ceilings and fail-closed read-only prechecks committed. Isolated Linux Go 1.27.1 unit/race checks each recorded 1,229 pass events, zero failures and eight skips. Physical D14 remains open. |
| Settlement and catalog | No-change release rescan, Store-seed binding and cold catalog replay committed. Full NO_DIFF verification and different-request admission remain unproved; the replay fixture handwrites its initial checkpoint bind. |
| ABI 8 deployment | Exact ABI 8 measured; pinned tools/image, identities, mounts and service units provisioned. Code now separates the empty delegated parent, runner leaf and cells sibling, and distinguishes calibration roles. Isolated race checks pass with one scanner-context skip and two command env skips; the scanner-context follow-up is active. Cells creation, guest layout, xattr authority, signed admission and governed E2E remain open. Actual-source xattr probes are active. |
| D14/D20 qualification | Logical owner headroom gate reproduces and fixes grant-then-terminal failure, but its bound is under pinned-engine review before commit. Physical reserve/faults, real owner/cell drain and retained restart remain open. Minimal production reserve wiring is being mapped. |
| Research Claims — last | Open. Complete deployment, E2E and durability/recovery evidence, then freeze corpus, independent effect oracles, calibration, scoring and model/cost plan. G1 needs denied-read and transient-mutation evidence; endpoint equality is insufficient. G1/H1 unestablished. |

A combined immutable module gate is still needed after active source increments. Intermittent owner-incarnation refusals remain unexplained. Hidden privileged xattrs remain a concrete qualification limitation; completeness authority is unsigned.

Engineering: OpenCode Go. Final research: OpenRouter DeepSeek V4.1 Flash, US$5 total cap. User/advisor construct review; independent runtime/effect oracles retained. Broader product features deferred. Other-session docs and original/seed VMs preserved.
