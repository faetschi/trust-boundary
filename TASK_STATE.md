# TBound task state

Updated 2026-10-17. Only `codex/tbound-prototype`; thesis first. Goal active.

- Gate: last full module 1a4cf40 = 2702 pass / 10 skip / 0 fail (agent clean-room). Skips need real provider / guest / root.
- Running: service receipt publication anchor (head+count) for runner PublicationFree.
- Codex reviews done through 1a4cf40 except the receipt-anchor increment. Trust decision: executors are trusted evidence producers; runner checks consistency, service receipt anchors exemptions.

Decisions (2026-10-17, owner):
- PROTOTYPING: live tests use OpenCode Go engineering route only; no OpenRouter calls until the owner switches.
- Guest: rows 4+3 done as Node ro bind mount + unit templates (d99ced8); D2 cell leg out of scope (in-place root-writer calibration instead); xattr claim bounded (`root-attested-bounded-no-hidden-trusted/v1`); NOPASSWD revoked before first cell launch, never restored; cell assets at /opt/tbound/cell; bundle LF; cell image rebuilt + cosign re-signed at frozen commit; cosign verify ignores tlog (trust = pinned key + image digest).
- E2E smoke: read-only smoke task, engineering route, admission smoke-2026-10-17 (`infra/guest/serve-pi-service.smoke.json`).
- Research: pre-registration = digest-pinned repo freeze commit, reviewers = owner + advisor; P = stub (no model); per-case order (C then 4 P/C replays); reruns only with outage evidence, max one, original kept, rerun scored; rerun headroom 5% (97 units); admission = signed corpus manifest, id `case_id/role/attempt`.
- Earlier: compliant C-success = VALID_PASS; dead owner + absent scope + no receipt = not drained.

Known flake: TestD14PreEffectTerminalHeadroomGate failed once (ErrRestartRequired at slack 22); stress investigation stalled (race x200 >54 min, no output) and was stopped 2026-10-17; not reproduced, no code changed.

Open owner decisions: ledger head-hash anchor owner (rollback detection); ledger unit vs request; P/C unit definition (EC:64 vs budget); corpus content and review; freeze commit timing; signing session (5 signatures + 2 key reads, runbook `infra/guest/PROVISIONING-SIGNING.md`).

Next: receipt anchor + Codex review; then freeze commit, preregistration record, and owner-approved signing session + E2E smoke over OpenCode Go; then guest executors (GovernedC), NET/PROC oracles, corpus content, pilot (quota profile pilot).

Constraints: preserve the ten other-session docs and `docs/tbound-product.md`; explicit `git add`; no secret/key/vm_start.txt reads; pinned Go 1.27.1 at /home/jeli2k/opt/go1.27.1 (default 1.22); clean-room gates archive `supervisor docs adapter/src infra`, short private TMPDIR; logs on F:; C: >= 5 GiB; no `wsl --shutdown`; every privileged guest write, signing, or VM state change needs explicit approval; sign only with the owner's keys.
