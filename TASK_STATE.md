# TBound task state

Updated 2026-10-17. Only `codex/tbound-prototype`; thesis first. Goal active.

- Gate: full module on 82e6348 = 40 pkgs, 2462 pass / 10 skip / 0 fail (clean-room incl. `infra/`; logs F:/TBoundAssets/recovery-20261015/fullgate3-20261017). Skips need real provider / guest / root.
- Running: service follow-ups (journal O(n^2), unknown-terminal operator resolution, pilot quota profile, recovery-binding v3, umask-022 catalog test); D14 headroom-gate flake investigation.
- Codex (gpt-6.1-sol high) adversarial review after each increment; it crashed for a while, now works. Not yet reviewed by Codex: runner f82b789, rerun/attacker be53c0e, corpus admission + r2 fixes 82e6348.

Decisions (2026-10-17, owner):
- PROTOTYPING: live tests use OpenCode Go engineering route only; no OpenRouter calls until the owner switches.
- Guest: rows 4+3 done as Node ro bind mount + unit templates (d99ced8); D2 cell leg out of scope (in-place root-writer calibration instead); xattr claim bounded (`root-attested-bounded-no-hidden-trusted/v1`); NOPASSWD revoked before first cell launch, never restored; cell assets at /opt/tbound/cell; bundle LF; cell image rebuilt + cosign re-signed at frozen commit; cosign verify ignores tlog (trust = pinned key + image digest).
- E2E smoke: read-only smoke task, engineering route, admission smoke-2026-10-17 (`infra/guest/serve-pi-service.smoke.json`).
- Research: pre-registration = digest-pinned repo freeze commit, reviewers = owner + advisor; P = stub (no model); per-case order (C then 4 P/C replays); reruns only with outage evidence, max one, original kept, rerun scored; rerun headroom 5% (97 units); admission = signed corpus manifest, id `case_id/role/attempt`.
- Earlier: compliant C-success = VALID_PASS; dead owner + absent scope + no receipt = not drained.

Known flake: TestD14PreEffectTerminalHeadroomGate failed once (ErrRestartRequired at slack 22); stress investigation stalled (race x200 >54 min, no output) and was stopped 2026-10-17; not reproduced, no code changed.

Open owner decisions: ledger head-hash anchor owner (rollback detection); ledger unit vs request; P/C unit definition (EC:64 vs budget); corpus content and review; freeze commit timing; signing session (5 signatures + 2 key reads, runbook `infra/guest/PROVISIONING-SIGNING.md`).

Next: runbook update (v4 corpus config, research route files, genesis, B4); Codex reviews of pending increments; then owner-approved provisioning/signing session on the guest (172.22.198.27, HostKeyAlias 172.18.155.19) and E2E smoke over OpenCode Go.

Constraints: preserve the ten other-session docs and `docs/tbound-product.md`; explicit `git add`; no secret/key/vm_start.txt reads; pinned Go 1.27.1 at /home/jeli2k/opt/go1.27.1 (default 1.22); clean-room gates archive `supervisor docs adapter/src infra`, short private TMPDIR; logs on F:; C: >= 5 GiB; no `wsl --shutdown`; every privileged guest write, signing, or VM state change needs explicit approval; sign only with the owner's keys.
