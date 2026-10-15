# TBound — current state

Updated 2026-10-15. Branch: `codex/tbound-prototype`. Scope: thesis completion first.

| Milestone | State |
| --- | --- |
| Recovery foundations | Committed durable fencing, cumulative restart/retry accounting, historical checkpoint verification and bounded provider exchanges. |
| SQLite safety | Committed file ceilings and fail-closed read-only prechecks. Isolated Linux Go 1.27.1 unit/race checks each recorded 1,229 pass events, zero failures and eight skips. Physical D14 qualification remains open. |
| No-change settlement and catalog | Real release rescan and durable Store-seed binding committed. Catalog replay now has a cold-process fixture; targeted unit/race pass 13 top-level tests plus 43 subtests without skips. Command-package unit/race each pass 345 events with two existing skips. The replay fixture handwrites its initial checkpoint bind: full NO_DIFF verification and different-request admission are still unproved and deferred to the real deployment path. |
| ABI 8 deployment | Exact ABI 8, pinned tools, signed cell image, identities, mounts and units provisioned. Runtime source is root-owned and protected. Cgroup-layout and calibration-role code corrections active; actual-source xattr and writer-capability probes active separately. Hidden privileged xattrs are a concrete limitation; authority unsigned. No admitted deployment or governed E2E yet. |
| D14/D20 qualification | A logical owner headroom gate passes the reproduced grant-then-terminal failure and a positive terminal case. Its growth bound is under review against pinned SQLite before commit. Physical reserve/faults, prior-owner/cell drain and retained clean restart remain open. |
| Research Claims — last | Open. Finish deployment, E2E and storage/recovery evidence, then freeze corpus, independent effect oracles, calibration, scoring and model/cost plan. G1 requires denied-read and transient-mutation observations; release endpoint equality does not replace them. G1/H1 remain unestablished. |

Current gates exclude other active source increments; a combined immutable module gate remains necessary. An intermittent owner-incarnation refusal is still unexplained.

Engineering: OpenCode Go. Final research: OpenRouter DeepSeek V4.1 Flash, US$5 total cap. User/advisor construct review; independent runtime/effect oracles retained. Product expansion deferred. Other-session docs and original/seed VMs preserved.
