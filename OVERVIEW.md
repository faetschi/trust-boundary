# TBound — current state

Updated 2026-10-18. Branch `codex/tbound-prototype`. **Code freeze `08eef85`**: clean-room gate 42 packages, 3085 pass / 18 skip / 0 fail (build, vet, gofmt, race, Windows vet clean); final Codex adversarial review FREEZE-OK. Green results verify code only: G1 false, H1 unexecuted, containment not established. Prototyping uses OpenCode Go only; no OpenRouter calls yet.

| Milestone | Current state |
| --- | --- |
| System (service, recovery, signed profiles) | Code complete, reviewed, frozen. Drain proofs, `abandon-unknown` only after reconciliation, corpus-manifest admission, bounded xattr authority, reproducible cell image with owner cosign signing, publication with a per-path identity ledger (`RENAME_NOREPLACE`/`RENAME_EXCHANGE`, displaced object verified). Not yet installed on the guest. |
| Measurement | Frozen. Oracles for workspace (wsoracle v7, E04 seven-point lifecycle evidence), network (netoracle v3, guest sink 127.0.33.1:8080), process (procoracle v3, continuous monitor). Runner: 1,935-unit plan, crash-safe journal, guest executor over SSH, root-only read-only evidence channel. |
| Experiment | Tools ready (`corpus-digest`, `preregistration-record`). Pilot corpus draft (21 cases, `corpus/pilot/`) awaits your review; the pre-registration record follows that review. Nothing run yet. |
| Guest (ABI 8) | At 172.30.8.106 (DHCP; pinned key via HostKeyAlias). Node mount and service units installed. **Waiting on you:** signing session per `infra/guest/PROVISIONING-SIGNING.md` (build from `08eef85`, 5 signatures, client key, evidence key, netsink unit, NOPASSWD revoke), then the first E2E smoke over OpenCode Go. |

Stated limits (thesis must say so): bounded xattr claim; PROC-02 misses crossings shorter than the poll gap; one cell per uid; no transparency log for the image signature; E04 cannot see effects made and undone between two lifecycle points; executors and the evidence channel are trusted.

Next: signing session + smoke run (owner), pilot corpus review → pre-registration record, pilot run, thesis corpus (owner + advisor), G0/G1. Test-code removals paused until you review the test-audit findings (`../review-artifacts/test-audit-20261018-*.md`).

Engineering: OpenCode Go. Final research (later): OpenRouter DeepSeek V4.1 Flash, US$5 cap, peak $0.30/$1.20 per M tokens.
