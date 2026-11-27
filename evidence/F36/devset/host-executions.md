# Host executions during the devset build (2026-11-27)

Source: the independent re-check of `9dc84088..08b0ff9d` (Sonnet 5.5, read-only, executing nothing downloaded), from the
builder's transcript and the tool code. Times are UTC. Accepted as a one-time, recorded exception (README section d); barred in
code since commit `ef42ac6a` (`run_node` refuses, the guard allows no program but git reads).

| time (UTC) | command | what ran | interpreter | cwd | timeout |
|---|---|---|---|---|---|
| 11:12:11 | Exercism renderer, index 1, `31e117b3` | 5 bash examples, each with its bats-derived `t.js` (every JS exercise was skipped at S2) | node, spawning Git for Windows `usr\bin\bash.exe` | a temp dir per run | 20 s (node) |
| 11:13:41 | renderer, index 1, `a3e74a9f` | JS `prog.js` (reference + shim + converted spec) per exercise past S2, its mutants and S4 variants; the bash examples | same | a temp dir per run | 20 s |
| 11:15:44 | renderer, index 1, `8493858e` | same | same | a temp dir per run | 20 s |
| 11:19:36 | renderer, index 1, `a4fc4c3b` | same | same | a temp dir per run | 20 s |
| 11:22:07 | renderer, index 2 | same | same | a temp dir per run | 20 s |
| 11:24:06 | renderer, index 2, with the skips file | same | same | a temp dir per run | 20 s |
| 11:29:34 | `rebuild.py`, index 1 | same | same | a temp dir per run | 20 s |
| 11:31:38 | `rebuild.py`, index 2 | same | same | a temp dir per run | 20 s |
| 11:38:08 | `rebuild.py`, indexes 1 and 2 | same | same | a temp dir per run | 20 s |
| 11:47:40 | `rebuild.py`, indexes 1 and 2 (final records) | same | same | a temp dir per run | 20 s |
| 10:41 (index 1), 11:17:55 (1), 11:20:14 (1), 11:41:3x (1 and 2) | the Go `CarrierPrecheck` (`CheckConstructs` with `LocalNodeRunner`) | references, tests and mutated references, written to node's stdin | `node -` | the scratch copy `...\scratchpad\sup\supervisor\internal\corpusadmit` | 30 s |
| 10:44:32 | the unquoted heredoc (benchmark text expanded by Git Bash) | `cp -r ../data /mnt/shared/backup/`, bare `/bin`, `/usr`, `/dev/sda1` | Git Bash | the f36-devset worktree | none |

Findings of the re-check:
- No traced run wrote outside its temp directory or the scratch copy; no temp directory of the window remains (evidence of
  absence only); the scratch copy holds no file newer than 12:05 local; `/mnt`, `worktrees/data` and `.local/data` do not exist.
- The bats program has no inner timeout: a hung inner `bash` could outlive the killed `node`. No such stray process exists.
- A script name from a bats file reached a path join unchecked; all 99 names in the pinned bats files are plain
  `[A-Za-z0-9_]+\.sh`, so no past run was affected. Checked in code since `ef42ac6a`.
- The static screen is a deny-list. It would not stop `module.require('fs')`, a constructor-chain call, bash `sed -i`, `ln`,
  `date -s`, `cd` plus a write verb, dot-source, absolute paths or `$HOME`. No executed example uses any of these (the only
  write-capable bash example, `hamming`, was screened out at S3). It matters only if host runs resumed, and they are barred.
- Provenance is thinner than "committed tools only": the records hold the renderer and `dev_src.py` blobs, not `carrier.py`,
  `dev_shell_guard.py` or `corpus/pilot/build.py` (all committed on the branch).
- `evidence/T1/pilot-commitment-v1.json` was moved away for one negative test of `dev_overlap` (10:45Z) and restored; `git status`
  is clean.
