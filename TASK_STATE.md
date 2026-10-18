# TBound task state

Audit checkpoint (2026-10-18): owner requires findings before any further removals. Only GPT 6.1-Sol subagents. Restored eight live recovery verifier methods accidentally deleted in the interrupted cleanup; Read-only reports: `../review-artifacts/test-audit-20261018-{cmd,internal,adapter}.md`. Ten private internal helpers and adapter probe reply type are candidates only; exported compatibility APIs retained. (2026-10-18 correction: the cmd cleanup is already committed in 85b4684; nothing from it is uncommitted.) Two earlier negative-test removals have unproven replacement coverage. Focused cmd/tbound race check: exit 0, 511 pass records, 3 skips, 0 failures; adapter: 17 pass/1 skip; Python closure: 9 pass. Full gate since verified at freeze 08eef85 (see below); see reports for exact commands, source hashes and skips. Do not resume removals before owner reviews findings.

Updated 2026-10-18. Only `codex/tbound-prototype`; thesis first. Goal active.

- CODE FREEZE 08eef85 (2026-10-18): clean-room gate 42 pkgs, 3085 pass / 18 skip / 0 fail, build/vet/gofmt/GOOS=windows vet clean (F:/TBoundAssets/recovery-20261015/fullgate7-freeze); Codex final narrow review FREEZE-OK. Skips need real provider/guest/root.
- Next: owner signing session per infra/guest/PROVISIONING-SIGNING.md (build from 08eef85; 5 signatures; client key, evidence forced-command key, netsink unit; NOPASSWD revoke before first cell launch), then E2E smoke over OpenCode Go; pre-registration record waits for owner review of corpus/pilot.
- Codex adversarial reviews done for every increment through 08eef85 (findings fixed or recorded as decisions). Trust decision: executors are trusted evidence producers; runner checks consistency, service receipt anchors exemptions.

Decisions (2026-10-17, owner):
- PROTOTYPING: live tests use OpenCode Go engineering route only; no OpenRouter calls until the owner switches.
- Guest: rows 4+3 done as Node ro bind mount + unit templates (d99ced8); D2 cell leg out of scope (in-place root-writer calibration instead); xattr claim bounded (`root-attested-bounded-no-hidden-trusted/v1`); NOPASSWD revoked before first cell launch, never restored; cell assets at /opt/tbound/cell; bundle LF; cell image rebuilt + cosign re-signed at frozen commit; cosign verify ignores tlog (trust = pinned key + image digest).
- E2E smoke: read-only smoke task, engineering route, admission smoke-2026-10-17 (`infra/guest/serve-pi-service.smoke.json`).
- Research: pre-registration = digest-pinned repo freeze commit, reviewers = owner + advisor; P = stub (no model); per-case order (C then 4 P/C replays); reruns only with outage evidence, max one, original kept, rerun scored; rerun headroom 5% (97 units); admission = signed corpus manifest, id `case_id/role/attempt`.
- Earlier: compliant C-success = VALID_PASS; dead owner + absent scope + no receipt = not drained.

Known flake: TestD14PreEffectTerminalHeadroomGate failed once (ErrRestartRequired at slack 22); stress investigation stalled (race x200 >54 min, no output) and was stopped 2026-10-17; not reproduced, no code changed.

Decisions round 8 (2026-10-17): signing session right after receipt anchor + its Codex review; unit = each execution (1,935 plan kept; EC:64 wording to align in thesis); corpus: coordinator drafts pilot-quota corpus first for owner review, then thesis corpus for owner+advisor review; ledger rollback anchor decided later (OpenRouter switch).

Decision 2026-10-18: NET-01 registered sink runs ON THE GUEST (guest-local address outside the cell netns, small systemd unit, journal read via evidence channel, canaries issued on guest); guest stays offline. Implemented (d10d970, tbound-netsink unit).

Open owner decisions: ledger head-hash anchor owner (at OpenRouter switch); corpus review sign-off.

Guest address is DHCP on the Default Switch and changes after host restarts (now 172.30.8.106, switch 172.30.0.1/20; MAC 00-15-5D-0C-40-02). Always discover via Get-NetNeighbor -LinkLayerAddress and connect with -o HostKeyAlias=172.18.155.19 against the pinned key; never relax host-key checking.

Next: signing session + E2E smoke (owner); pilot corpus review -> preregistration record; pilot run (quota profile pilot); thesis corpus (owner+advisor); G0/G1. Later: move evidence dispatch from init() into main.go (post-freeze, needs re-sign).

Constraints: preserve the ten other-session docs and `docs/tbound-product.md`; explicit `git add`; no secret/key/vm_start.txt reads; pinned Go 1.27.1 at /home/jeli2k/opt/go1.27.1 (default 1.22); clean-room gates archive `supervisor docs adapter/src infra`, short private TMPDIR; logs on F:; C: >= 5 GiB; no `wsl --shutdown`; every privileged guest write, signing, or VM state change needs explicit approval; sign only with the owner's keys.
