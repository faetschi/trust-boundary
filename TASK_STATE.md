# TBound task state

Audit checkpoint (2026-10-18): owner requires findings before any further removals. Only GPT 6.1-Sol subagents. Restored eight live recovery verifier methods accidentally deleted in the interrupted cleanup; existing cleanup remains uncommitted. Read-only reports: `../review-artifacts/test-audit-20261018-{cmd,internal,adapter}.md`. Ten private internal helpers and adapter probe reply type are candidates only; exported compatibility APIs retained. Two earlier negative-test removals have unproven replacement coverage. Focused cmd/tbound race check: exit 0, 511 pass records, 3 skips, 0 failures; adapter: 17 pass/1 skip; Python closure: 9 pass. Full current-tree gate remains unverified; see reports for exact commands, source hashes and skips. Do not resume removals before owner reviews findings.

Updated 2026-10-17. Only `codex/tbound-prototype`; thesis first. Goal active.

- Gate (freeze candidate d2f6b06): 43 pkgs, 2933 pass / 17 skip / 0 fail, clean-room incl. infra (F:/TBoundAssets/recovery-20261015/fullgate4-20261018). Skips: 7 procoracle real-cgroup tests (pass in delegated scope, procoracle-r2 gate), 2 root-only credential tests (passed in root run), rest need real provider/guest/root.
- Running: narrow Codex r2 on netoracle v2 (f7fdd61) + procoracle v2 (d2f6b06); then FREEZE + preregistration record + signing session.
- Signing-session add-on (needs owner approval): runner reaches the client as tbound-client (uid 994, nologin) and sudo is revoked at Z1, so propose a dedicated ssh key for tbound-client with restrict + forced command running only `tbound serve --pi` with validated args (needs a non-nologin shell or Match User ForceCommand); privileged guest write.
- TODO after those land: runner provenance must enforce per-key OracleRefs (version+report) for NET/PROC keys too (today only WS keys; oracleVersionOK assumes wsoracle version).
- Codex reviews done through 1a4cf40 except the receipt-anchor increment. Trust decision: executors are trusted evidence producers; runner checks consistency, service receipt anchors exemptions.

Decisions (2026-10-17, owner):
- PROTOTYPING: live tests use OpenCode Go engineering route only; no OpenRouter calls until the owner switches.
- Guest: rows 4+3 done as Node ro bind mount + unit templates (d99ced8); D2 cell leg out of scope (in-place root-writer calibration instead); xattr claim bounded (`root-attested-bounded-no-hidden-trusted/v1`); NOPASSWD revoked before first cell launch, never restored; cell assets at /opt/tbound/cell; bundle LF; cell image rebuilt + cosign re-signed at frozen commit; cosign verify ignores tlog (trust = pinned key + image digest).
- E2E smoke: read-only smoke task, engineering route, admission smoke-2026-10-17 (`infra/guest/serve-pi-service.smoke.json`).
- Research: pre-registration = digest-pinned repo freeze commit, reviewers = owner + advisor; P = stub (no model); per-case order (C then 4 P/C replays); reruns only with outage evidence, max one, original kept, rerun scored; rerun headroom 5% (97 units); admission = signed corpus manifest, id `case_id/role/attempt`.
- Earlier: compliant C-success = VALID_PASS; dead owner + absent scope + no receipt = not drained.

Known flake: TestD14PreEffectTerminalHeadroomGate failed once (ErrRestartRequired at slack 22); stress investigation stalled (race x200 >54 min, no output) and was stopped 2026-10-17; not reproduced, no code changed.

Decisions round 8 (2026-10-17): signing session right after receipt anchor + its Codex review; unit = each execution (1,935 plan kept; EC:64 wording to align in thesis); corpus: coordinator drafts pilot-quota corpus first for owner review, then thesis corpus for owner+advisor review; ledger rollback anchor decided later (OpenRouter switch).

Open owner decisions: ledger head-hash anchor owner (at OpenRouter switch); corpus review sign-off.

Next: receipt anchor + Codex review; then freeze commit, preregistration record, and owner-approved signing session + E2E smoke over OpenCode Go; then guest executors (GovernedC), NET/PROC oracles, corpus content, pilot (quota profile pilot).

Constraints: preserve the ten other-session docs and `docs/tbound-product.md`; explicit `git add`; no secret/key/vm_start.txt reads; pinned Go 1.27.1 at /home/jeli2k/opt/go1.27.1 (default 1.22); clean-room gates archive `supervisor docs adapter/src infra`, short private TMPDIR; logs on F:; C: >= 5 GiB; no `wsl --shutdown`; every privileged guest write, signing, or VM state change needs explicit approval; sign only with the owner's keys.
