# TBound task state

Updated 2026-10-16. Only `codex/tbound-prototype`; thesis first; goal active (all milestones incl. Research Claims).

- Done+committed: D14 gate b089a9d + fixes 852f276/7520458; model pin bd6de94 (stale runtime-bundle digest in abi8-runtime-provisioning receipt: re-measure/re-sign at provisioning); R9 84cfaad; R10 56aadeb; evalscore 4a6a849 (20 OPEN, rule_satisfied false); usdledger 95f6ab6; researchprofile 94484cf; evalcorpus 81abc77 (17 OPEN); effectoracle 1384e9a (14 OPEN); cleanup dc985e2/5ed2295/5bd16fd.
- Running Haiku agents: full-module race gate on HEAD (fullgate), drain-verifier read-only plan. Commit only after verified report.
- Prior gate: 624 pass/8 skip/0 fail clean-room on 0664c25+D14; skips unverified (Landlock ABI8, D09 scanner, cgroup, root, ext4 capacity, dev fixture, real provider x2). New gate pending.
- Open component work: E06 route matrix, registered effect oracles (beyond E04), P/C calibration harness, B/V runners, attacker library, corpus content (L, human review), real drain verifier, D14 physical/fault tests, root terminal anchor gating, incarnation refusals unexplained (don't weaken fences).
- Cleanup candidates left for user: 4 unlinked docs (infra/hyperv INSTALLATION-RECORD, RESTORE-ACCEPTANCE; infra/repro DISPOSABLE-CGROUP-TEARDOWN; sessionrepo FAILURE_MODES), ~12 tests not in CI, empty dirs incl. space-named paths under supervisor/. docs/status.md link to empty review-artifacts is broken (other session's file).
- Open guest work: B2/B3 xattr calibration + signing, cells creation/systemd leaf/NodeRO, registration, E2E serve --pi, drain, checkpoint restore. Guest df4745b2... Running, IP 172.18.155.19; authority unsigned; original/seed preserved.
- Research Claims LAST: G0/G1, frozen corpus/calibration/scoring, prices/cost plan (US$5, 1,935 units unknown feasible), registration decision, user/advisor review. Final OpenRouter DeepSeek V4.1 Flash; engineering OpenCode Go.
- User decisions pending: see OVERVIEW.md.

Constraints: Haiku5.5/max; preserve ten other-session dirty docs; explicit staging. No secret/key/vm_start.txt reads. Pinned Go 1.27.1 at /home/jeli2k/opt/go1.27.1 (default go is 1.22), private source/TMP under /home/jeli2k cleaned, logs F:, C:>=5GiB, no WSL shutdown. Archive with core.autocrlf=false. Canonical thesis MA Thesis/MSE_MA_Thesis/tbound/. Fully overwrite OVERVIEW; commit verified increments.
