# TBound task state

Current checkpoint, 2026-10-23. History before this date: git log of this file and `../review-artifacts/TASK_STATE-history-20261023.md`.

## Where we are

- Branch `codex/tbound-prototype`. Last freeze on the guest: **F12 = eed7722** (kit `F:\TBoundAssets\recovery-20261015\F12`, guest IP 172.24.152.73).
- On the real guest (F11/F12, correct Landlock TSYNC): **G1 passed** (read, edit, bash, publish), **route probe passed**, **direct-runtime probe passed** (launch proof, Pi not started; ends `unknown` by design).
- EVQ dry run on F12: **18/22** passed (17 capture + adapter). Not passed: 3 guest artifacts were stale (kit bug, now fixed), capability stale, netsink missing (needs a trial reset). Route report: 3 routes covered, bash-view stale. "21/22 with fresh artifacts" is a forecast, not a result.
- Merged since F12 (not yet on the guest): static denial hints + C-DH code (C-DH parked, default-off), f5-review item 6 (retry-margin budgets), adapter-tests reporter pin, tbound-op audit fixes (seed-live symlink safety, base64 counts). Reset/cleanup fixes merged (podman via the SVC setpriv line without --root; reset --check no longer refuses after a no-reap run; reviewed). Gate on 841d326: 4526 pass / 0 fail / 26 skip (the extra skip is the ABI-8 Landlock thread test, guest-only).
- Audit fixes merged: adaptive receipt fields kept, empty bash scope survives recovery, Landlock setup errors fail instead of skip, the TSYNC thread test is a required guest test, dead code removed (fix/audit-go, reviewed); tbound-op fixes (fix/audit-tbound-op, reviewed).

## Decisions (owner, 2026-10-23)

- **C-DH parked until after H1.** Thesis keeps Configuration C with the static permitted-alternative table. C-DH code stays merged but off; no chooser runs or OpenRouter spend before H1. Thesis amendment (D26 amendment 2026-10-23 (3)) in progress.
- **Goal (adopted from the direction review):** pilot BEFORE registration.

## Goal, in order

1. Confirm the scope cut with the owner; align D26 and the residual-channel claim (Â§6.3 hostile stimuli vs Â§11 scripted bash) with the advisor.
2. Finish the pilot reviews and case corrections (manifest.draft.json still has TODO reviewers/decisions); freeze the pilot protocol, provider, outage rules and initial safety ceilings.
3. Freeze and pin the selected code; install it and repeat G1 and both probes with fresh evidence.
4. Enter corpus mode; verify reset, net-sink, no-reap and V; require complete EVQ and route reports (every obligation must pass; the report commands exit 0 even when incomplete).
5. Run the engineering, compliant and adaptive-cap pilots; derive caps, costs and calendar needs from retained records.
6. Build and independently review the full 299/100/20 corpus; complete every required channel witness and preserve the cold partition.
7. Freeze the final artifact, corpus, caps, analysis and feasible funding; complete the remaining gates and immutable H1 registration.
8. Run H1 under the registered rules; report support, failure or unsupported evidence without changing denominators.

## Needs the owner

- Advisor: pilot corpus review (manifest TODOs), D26 scope cut, the residual-channel claim.
- Pre-registration decisions (protocol, provider, outage rules, ceilings) and runbook decisions D1-D8, incl. the S3'/S4' signing with the owner keys.
- Approvals for corpus mode: tbound-netsink account/unit, pinned nft rule, root forced-command trial key (account changes before registration); Spike B (reset, netsink evidence, P A-03, P C-02 no-reap, cleanup reset, compliant C, V, reboot/reload).
- First corpus-mode rehearsal must watch: reset --check passes after P C-02 and the reset kills the survivor; `podman rm -f` after conmon is killed; no uid-995 pause process left in root's SSH cgroup (`ps -u 995 -o pid,cgroup,cmd`); kernel-7 zombie-leader kill; run the 5 real-cgroup tests with TBOUND_REAL_CGROUP_TESTS=1 in a delegated scope.

## Pilot decisions 10/14 implementation (isolated worktree, 2026-10-23)

- `feat/pilot-20-compliant` in `C:/Users/Admin/AppData/Local/Temp/tb-p20`, based on `fb6dec0`.
  Decision 14: `9501371` introduces explicit scripted caps, requiring zero token/money dimensions;
  real-provider caps still require positive values. Decision 10: 20 distinct compliant tasks (5/tool),
  33 cases overall, 75 artifacts; human reviews and the four environment/provider/profile/policy pins stay draft.
- Source tree verified: `bff58c36b5fc0ec84359d411ef645089d975b426` (before this log-only update).
  WSL private copy `/home/jeli2k/tmp/p20.huny`, private `TMPDIR` there, `umask 077`,
  `GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local`, Go `/home/jeli2k/opt/go1.27.1/bin/go`:
  `go test ./internal/evalcorpus/ ./internal/corpusadmit/ ./internal/attacker/ ./internal/providerbridge/ ./internal/runner/ ./cmd/tbound-run/ ./cmd/tbound/ -count=1 -json`
  exit 0 (1,804 passing tests/subtests, 8 environment-dependent skips); `go vet ./...` exit 0;
  `go test ./cmd/tbound-profile/ -count=1` exit 0. Logs: `tests.jsonl`, `vet.log`, `profile.log` in that private copy.
  The supplied tar list needed one extra fixture copied: `adapter/src/result-hints.ts`.
  The final review-status-independent corpus artifact test was then copied into that private tree and
  `go test ./internal/corpusadmit/ -count=1` passed (exit 0).
- Skips: pinned Pi offline fixture, real-provider durable exchange, foreign credential ownership,
  and five real-cgroup tests. No new test skipped; no guest execution or human review performed.
- Two `python3 -I corpus/pilot/build.py --tbound-run /home/jeli2k/tmp/p20.1a2w/tbound-run`
  runs produced identical bytes for all 76 generated files (manifest + 75 artifacts).
  Snapshot SHA-256 (sorted relative filenames, NUL, bytes, NUL):
  `b3cab61f629965e307f0efed365ecbe72ebe05c619442e9f109686d47f279b2c`.
  Local read-only awk/sort stdout, Node assertions/tests, formatter JSON, and in-memory SQLite semantics passed.
- Canonical draft manifest digest (production `evalcorpus.Digest`):
  `sha256:8dd74b674200b65e7d3fcb11f2284a7578be64663d2812ae878f88a60be24008` ->
  `sha256:157173a4219ce90cd13ce7a665eef0534ecedb9a2c9536fcfc0dc4f4b01ae9d2`.
  Attacker pin: `sha256:fbba81e7c5ce395366c43a0fa630b622bd0fe5a12fff68ff7a93009da8337e0e` ->
  `sha256:57c3c54b048432ae5e6cdcaba3a1b4764e45a8cca2b67acaba549c1d45627993`.
  Twelve new fixture/acceptance/solution digest triples; all original case-level digests unchanged.
  This is an unsigned draft, not an admitted corpus digest or thesis result. No push.

## Working rules

- Freeze loop: gate -> freeze -> archive previous guest state (option A) -> install -> guest chain. Kit lessons: no BOM/LF only; decode base64 event data, never grep it; step 40 requires each evidence artifact's commit to equal the freeze commit; stale uid-995 podman pause processes need `RECLAIM_PODMAN_PAUSE=1`; the dr request ends unknown by design and must be archived before the pilot cycle.
- Next freeze: prefer one tested script version plus a small per-freeze values file over copying the ~40-file kit again.
- Thesis limitations to record: kernel 7.0 CLONE_INTO_CGROUP bug; on a single-uid rootless host cell ownership cannot tell image root from the cell user; audit chain has no external anchor; every guest run before F11 had thread-only Landlock (flag bug) and is not evidence.
- Parked for after the thesis: C-DH / decision-model hints (docs/design/denial-hints.md stage 2, docs/design/denial-hint-providers.md); web chat extraction.

Constraints: explicit `git add`; never push; no secret/key/vm_start.txt reads; Go 1.27.1 at /home/jeli2k/opt/go1.27.1; clean-room gate via `F12/tools/gate.sh`; logs on F:; C: >= 5 GiB; no `wsl --shutdown`; every privileged guest write, signing, or VM state change needs explicit approval; sign only with the owner's keys.
