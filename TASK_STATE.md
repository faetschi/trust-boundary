# TBound task state

Updated 2026-10-17. Only `codex/tbound-prototype`; thesis first; goal active (all milestones incl. Research Claims).

- Committed this session: D14 gate b089a9d + 852f276/7520458; model pin bd6de94; R9 84cfaad; R10 56aadeb; evalscore 4a6a849; usdledger 95f6ab6; researchprofile 94484cf; evalcorpus 81abc77; effectoracle 1384e9a; drainproof fca4680; routematrix 0b46b74; calibration 74c6c7d; attacker a298f71; cleanup dc985e2/5ed2295/5bd16fd.
- Review fixes (independent review, all CONFIRMED findings fixed test-first): drainproof+calibration a081cbf; effectoracle b2d94ce; usdledger+researchprofile 721b968; routematrix ab783f1; evalscore+evalcorpus 97c6741; D14 clock-persist 484d806.
- Running: full-module race gate on 484d806 (fullgate2). Previous gate on 1384e9a: 1597 pass/8 skip/1 clean-room scope-artifact fail (piinstall reads adapter/src; passes on real tree). Skips unverified: real provider x2, dev fixture Pi, Landlock ABI8, D09 scanner, root ownership, cgroup limits, ext4 capacity.
- Not yet fixed from review (user decision or larger): evalscore compliant-C-success label; D14 terminal root-anchor/owner-file gating; hard-link/mmap blind spots in effectoracle; fd-relative watches; unlinked docs (infra/hyperv INSTALLATION-RECORD, RESTORE-ACCEPTANCE; infra/repro DISPOSABLE-CGROUP-TEARDOWN; sessionrepo FAILURE_MODES), ~12 tests not in CI, stale docs naming retired model (provider-durable-evidence, governed-setup, provider-conversation-bridge, 2026-10-12-cli-reproduction).
- Next increments (host): wire drainproof/calibration/attacker/routematrix into a pair runner plan; EFF-WS-05/06, NET, PROC oracles (NET/PROC need guest); B identity profile; analysis report tool joining evalcorpus+evalscore+usdledger.
- Open guest work: B2/B3 xattr calibration + signing, cells creation/systemd leaf/NodeRO, registration, E2E serve --pi, drain, checkpoint restore. Guest df4745b2... IP 172.18.155.19; authority unsigned; original/seed preserved.
- Research Claims LAST: G0/G1, frozen corpus/calibration/scoring, prices/cost plan (US$5, 1,935 units unknown feasible), registration decision, user/advisor review.
- User decisions pending: see OVERVIEW.md.

Constraints: Haiku5.5/max; preserve ten other-session dirty docs; explicit staging. No secret/key/vm_start.txt reads. Pinned Go 1.27.1 at /home/jeli2k/opt/go1.27.1 (default go is 1.22), private source/TMP under /home/jeli2k cleaned, logs F:, C:>=5GiB, no WSL shutdown. Archive with core.autocrlf=false. Canonical thesis MA Thesis/MSE_MA_Thesis/tbound/. Fully overwrite OVERVIEW; commit verified increments.
