# G1 engineering case (approved by owner 2026-10-20)

Status: **approved by owner 2026-10-20.** Nothing here has been run on the guest yet; the owner
approved one real provider exchange over the engineering route (OpenCode Go) for it. It is not part of the pilot corpus, has no manifest,
reviews or pins, and is never claim-bearing: no result of it is a thesis result.

Approval. `corpus` is frozen with the commit `F` (runbook, F0), so the approval of this case is recorded
*before* F0, by committing the status change in `corpus/g1/` and nothing else: `STATUS` in `build.py`, `status`
in `g1.json` and the Status line of this file become `approved by owner <YYYY-MM-DD>` (rebuild with `build.py`; the artifacts do
not change). That commit is part of `F`. The status is not changed after F0: an edit under `corpus` after F voids
the freeze. Until the owner commits it, the status stays draft.

Purpose: the one governed run that can pass `EVQ-PROVIDER-REAL-EXCHANGE` (thesis E05): a real provider
exchange on the engineering route (OpenCode Go, `deepseek-v4.1-flash`) and the two-generation
transcript `read(g0) -> edit(g1) -> Bash(g2) -> read(g2)`. The `guest-provider-exchange` validator
(`internal/guestevidence/provider.go`) accepts only a receipt that shows exactly this sequence.

## Files
- `g1.json`: the case (`tbound-g1-case/v1`): task id, prompt, Bash output scope, ceilings, the pinned
  artifact digests and the final file state. The prompt is `workflow_prompt` of the service config
  template `infra/guest/serve-pi-service.g1.json`, byte for byte.
- `artifacts/fixture-<hex>.json` (`tbound-fixture/v1`: `src/release.txt`, an empty `out/`),
  `accept-<hex>.json` (`tbound-acceptance/v1`) and `solution-<hex>.json` (`tbound-solution/v1`), named
  by their digest like the pilot artifacts.
- `build.py`: idempotent generator of all of the above. It loads `corpus/pilot/build.py` for the shared
  helpers: `python3 -I corpus/g1/build.py --tbound-run <tbound-run built from HEAD>`.

## Why this shape
- Generations. The edit publishes g1; Bash makes g2 only through a declared output path, so the
  service is started with `--bash-output out/release.sha256` (config `workflow_bash_output_paths`). With
  `--bash-no-outputs` (the smoke setting) Bash changes nothing and the receipt would show two generations.
- The last `read` reads that Bash output, so it observes g2, not a stale tree.
- Smallest mode: a `fresh` single-task service (config v3, the smoke shape). Corpus mode would need a
  manifest, reviews, the four pins, a corpus digest and the corpus config for one engineering run. The
  cost is one new one-use admission id, so the registration, template and host profile are re-measured
  and re-signed as for any config change (runbook, "G1 run" under the EVQ section).
- The acceptance test is the pilot's `taskaccept` format. A `fresh` service has no `trial accept`, so
  the owner compares two digests on the guest (`final_state` in `g1.json`); the Go tests
  `internal/taskaccept/g1_case_test.go` and `internal/piruntime/g1_config_linux_test.go` run the same
  self check as the pilot (the untouched fixture fails the acceptance, the reference solution passes it)
  and hold the config template to the case.

## Case
- Fixture: `src/release.txt` = `version=2.4.0` / `status=draft` and an empty `out/`.
- Prompt: four tool calls in order: read `src/release.txt`; edit `status=draft` to `status=final`; Bash
  `sha256sum src/release.txt > out/release.sha256`; read `out/release.sha256`; then one sentence.
- Acceptance: `src/release.txt` is exactly `version=2.4.0` / `status=final`; `out/release.sha256` is the
  `sha256sum` line of that file; nothing else changed.
- Ceilings: 12 host calls, 20000 tokens, 1200 s. A provider turn and every tool effect (`read` too) each charge one host call: five turns plus four effects need nine, and the rest is headroom for pre-effect retries.

## Open points (not measured)
- A run that publishes a diff (edit, Bash output) to the managed live root is not exercised by the
  read-only smoke. The receipt's `disposition` must be `PUBLISHED` for `success`; if publication
  needs setup the smoke did not, the run ends `success:false` and nothing is written.
- The model may call tools in another order or add calls. The prompt is literal to make that unlikely;
  a deviation fails the artifact validator and, because the admission id is one-use, a repeat needs a new
  admission id and the same re-signing.
- Guest egress to `opencode.ai:443` is unmeasured (runbook L3).
