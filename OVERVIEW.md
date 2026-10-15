# TBound — current state

Updated 2026-10-15. Branch: `codex/tbound-prototype`. Scope: thesis completion first.

| Milestone | State |
| --- | --- |
| Recovery foundations | Committed durable owner fencing, cumulative restart/retry accounting, historical checkpoint verification and bounded provider exchanges. |
| SQLite safety | Committed per-connection file ceilings and fail-closed read-only prechecks. Isolated Linux Go 1.27.1 unit/race checks each recorded 1,229 pass events, zero failures and eight skips. Physical D14 qualification remains open. |
| No-change settlement | Real lease close now rescans the live root and binds its release digest to the durable Store seed. Parent verified source/archive hashes, native exits and unchanged test snapshots. Focused checks pass without skips; full unit/race each pass 26 packages. Full cold catalog re-admission and actual service execution remain open; follow-up active. |
| ABI 8 deployment | Exact ABI 8 measured over pinned SSH. Pinned runtime, signed cell image, separate identities, mounts and service units provisioned. Cgroup bootstrap, runtime ownership and xattr calibration are being measured. No admitted registration or governed E2E yet. |
| D14/D20 qualification | Production-shaped testing exposed insufficient terminal ledger headroom after effect admission; correction active. Isolated ext4 reserve feasibility is recorded but does not qualify production storage. Physical faults, prior-owner/cell drain and retained clean restart remain open. |
| Research Claims — last | Open. Finish deployment, E2E and durability/recovery evidence, then freeze research corpus, independent effect oracles, calibration, scoring and model/cost plan. G1 and H1 are not established. |

An intermittent owner-incarnation refusal remains unexplained; subsequent passing runs do not establish a fix. Component gates exclude other active source increments.

Engineering provider: OpenCode Go. Final research: OpenRouter DeepSeek V4.1 Flash, US$5 total cap. User/advisor construct review; independent runtime/effect oracles retained. Broader product features deferred. Other-session documentation changes and original/seed VMs preserved.
