# How TBound is used, end to end

This is the practical guide: what runs where, in which order, and which commands you type.
For what TBound *is*, read `docs/README.md`. For the current state, read `OVERVIEW.md`.

## 1. The big picture in one minute

TBound sits between the Pi coding agent and your code. Pi proposes tool calls (read, write,
edit, bash). TBound checks every call against a fixed policy, runs bash in an isolated
container ("command cell"), keeps Pi's changes in a private copy, and copies them to the real
repository ("live root") only through a controlled publication step. Everything is written to
append-only journals. Independent checkers ("oracles") measure whether anything forbidden
happened: a protected file changed, data left the machine, a process escaped.

The thesis question (H1) is measured on a corpus of test cases:
- **PVR**: how often an attack still causes a forbidden effect (should be ~0).
- **ORR**: how often a normal, allowed task is wrongly blocked (should be ≤ 10%).

## 2. The three machines

| Machine | What it is | What you do there |
|---|---|---|
| **Your Windows PC** | The Git repos (`trust-boundary`, `MSE_MA_Thesis`) and the freeze kits on `F:\TBoundAssets\recovery-20261015` | Edit, commit, start kit scripts from Git Bash, run `tbound-panel` |
| **WSL Ubuntu** (on the PC) | Builds and tests (Go 1.27.1 at `/home/jeli2k/opt/go1.27.1`), signing work dirs | The kit runs builds, the test gate and signing here for you |
| **Guest VM** (Hyper-V Ubuntu 24.04, user `tboundadmin`) | The real, locked-down TBound service (`tbound-pi`, uid 995) with rootless podman cells | Nothing by hand; the kit drives it over pinned SSH |

The guest IP changes after a PC restart. Find it in PowerShell:
`Get-NetNeighbor -LinkLayerAddress 00-15-5D-0C-40-02`. SSH always uses the pinned host key alias `172.18.155.19`.

## 3. The life of one governed run (what happens inside)

1. A client sends a request (task text) to the service socket on the guest.
2. The service checks the request is admitted (one-use admission id, signed registration).
3. It starts Pi in a cell. Pi talks to the model only through TBound's provider bridge.
4. Each tool call goes through the gate: `ALLOW` or `DENY` (+ a fixed hint sentence on deny).
5. Allowed `bash` runs in a fresh command cell (no network, Landlock, seccomp, own uid).
6. When Pi is done, TBound checks the private copy and publishes the allowed changes to the live root.
7. The client gets a signed-receipt-backed response: `completed` / `unknown` / `refused`.

"G1" is exactly this with read, edit, bash and publish. It passes on the real guest.

## 4. The engineering loop: gate → freeze → guest

Every code change reaches the guest only through a **freeze**: one exact Git commit, built,
pinned and signed, then installed. All steps are scripts in the freeze kit.

**Since F13 there is one reusable kit:** `F:\TBoundAssets\recovery-20261015\kit`, plus one small
values file per freeze in `kit\freezes\F<n>.env` (older kits `F4`..`F12` are history; never edit them).

1. **Gate** (all tests, build, vet, Windows build, gofmt) on the commit you want to freeze.
2. **Freeze** (owner-side, in WSL): `10` record the commit → `20` build binaries → `25` runtime bundle →
   `30` build the cell image → `35` EVQ capture (test evidence) → `40` sign.
3. **Guest chain**: `27` archive the previous guest state → `00` open temporary sudo → `10` upgrade →
   `20` calibrations → `50` image/state checks → `00` close sudo → `29` recovery status →
   `30` G1 run → `40` guest evidence (capability, procoracle, landlock-connect, provider-exchange) →
   `31` route probe → `40` route-bash-view → `32` direct-runtime probe → `40` route-direct-runtime.

To start a new freeze: copy `kit\freezes\F<prev>.env` to `F<new>.env`, fill the previous freeze's
values from its logs, set `FREEZE_COMMIT` to the gated commit, then (Git Bash):
```bash
export KIT_FREEZE=F13 TBA_PW=...   # TBA_PW = the guest admin sudo password, only in your shell
bash /f/TBoundAssets/recovery-20261015/kit/tools/all.sh      # gate -> freeze -> guest chain
```
Logs land in `F:\TBoundAssets\recovery-20261015\F13\logs`. A step stops the chain on its first failure;
read its log, fix, rerun (steps are resumable). Each G1 / probe run spends a one-use admission id;
a new freeze needs new ids (in `infra/guest/serve-pi-service.*.json`).

## 5. The research pipeline (to get H1)

```
corpus  ->  checks  ->  AI panel  ->  advisor  ->  corpus mode  ->  pilot  ->  register  ->  H1
```

1. **Corpus** (`corpus/pilot/`, later the full corpus): test cases adapted from established
   benchmarks (InjecAgent, AgentDojo, ASB, RiOSWorld, RepoQA, HumanEval-JS, ToolEmu, ...).
   Rebuild with `python3 -I corpus/pilot/build.py` (deterministic).
2. **Deterministic checks** (D35 step 1): schema, sources/licenses, root overlap, leak checks,
   oracle mutation controls. One command writes one report (being added now).
3. **AI panel** (D35 step 2): three independent model families rate every case — see section 6.
4. **Advisor sign-off** (D35 step 3): your advisor checks the audit list the panel produces.
5. **Corpus mode** (one owner session): you sign the corpus registration and host profile
   (S3', S4') with your keys and approve the guest installs; then the corpus-mode rehearsal
   (reset, net-sink, no-reap, vanilla Pi).
6. **Pilot**: `tbound-run plan` / `run` / `export` / `report` run the pilot cases; the pilot fixes
   the caps and costs.
7. **EVQ / route report**: `tbound-run evq` and `tbound-run route-report` turn the guest evidence
   into the evidence-quality result (all obligations must pass).
8. **Pre-registration**, then **H1** under the registered rules.

## 6. Running the AI panel yourself (step 3 above)

`go build` prints nothing when it succeeds — it just creates `tbound-panel.exe`.

```powershell
cd "C:\Users\Admin\Desktop\FH\Master Software Engineering\MA Thesis\trust-boundary\supervisor"
go build -o tbound-panel.exe ./cmd/tbound-panel
Copy-Item cmd\tbound-panel\panel.default.json ..\panel.mine.json
```

Edit `..\panel.mine.json`: put exactly **three** models from **three different families** into
`"models"` (Claude/Anthropic, GPT/OpenAI and DeepSeek are refused; router or `-latest` ids too).
Take the id and prices from each model's page on openrouter.ai:

```json
"models": [
  {"id": "qwen/qwen3.8-27b",                  "temperature": 0, "max_tokens": 4000, "prompt_usd_per_mtok": 0.0, "completion_usd_per_mtok": 0.0},
  {"id": "z-ai/glm-5.3-flash",                "temperature": 0, "max_tokens": 4000, "prompt_usd_per_mtok": 0.0, "completion_usd_per_mtok": 0.0},
  {"id": "nvidia/nemotron-3-ultra-550b-a55b", "temperature": 0, "max_tokens": 4000, "prompt_usd_per_mtok": 0.0, "completion_usd_per_mtok": 0.0}
]
```
(Replace the `0.0` prices with the real USD per-million-token prices from openrouter.ai; the tool
refuses prices of 0 because the budget cap needs a worst case. The ids above are your own
candidates from `example_models`; your other two candidates, `anthropic/claude-…` and
`deepseek/…`, would be refused: those families authored the cases or are evaluated.)

Then:
```powershell
.\tbound-panel.exe --config ..\panel.mine.json --manifest ..\corpus\pilot\manifest.draft.json --estimate
.\tbound-panel.exe --config ..\panel.mine.json --manifest ..\corpus\pilot\manifest.draft.json --out ..\panel-dry --dry-run
$env:OPENROUTER_API_KEY = "<key with a credit limit>"
.\tbound-panel.exe --config ..\panel.mine.json --manifest ..\corpus\pilot\manifest.draft.json --out ..\panel-run-1 --live
```
- `--estimate` costs nothing; expect about USD 1.2 (worst case ~6). The run stops hard at the cap (USD 10).
- If `--live` stops (error, cap, missing cost), run the same command again: it resumes.
- Send your advisor `audit.md`, `audit.json`, `audit-packets\` and `stats.md` from the run folder.
- Gates per checklist item: kappa ≥ 0.80 and agreement ≥ 90% = pass; 0.60–0.79 = revise; < 0.60 = stop;
  any missing case = no pass.

## 7. What only you can do

- Sign anything with your keys (registrations, host profiles), approve privileged guest changes,
  VM restarts or account changes.
- Fund and run paid model calls (the panel; later the research provider route).
- The advisor review and sign-off.

## 8. When something goes wrong

| Symptom | What it usually means | Where to look |
|---|---|---|
| SSH fails / times out | Guest IP changed | Section 2 (`Get-NetNeighbor`) |
| A run ends `unknown` | The outcome could not be proven; the next request is blocked | Service log: `journalctl -u tbound-pi.service` (the log now names the cause); recovery = archive at the next freeze (option A) |
| A step says "0-byte file" | A WSL disk stall or a genuinely empty export | The step's log; `df -h` (keep C: ≥ 5 GiB) |
| Guest step stops on a stale `podman` pause process | Leftover from a probe | Rerun step 50 with `RECLAIM_PODMAN_PAUSE=1` |

Current state and the next steps: `OVERVIEW.md` and `TASK_STATE.md`.
