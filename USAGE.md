# Using TBound

A practical guide from installation to a governed run and an evaluation. It is
general: it works on a native Linux host, or on Windows with a Linux VM. For
details, follow the links into `docs/`.

## 1. What TBound does

TBound sits between the [Pi](https://pi.dev) coding agent and your repository.

- Pi proposes tool calls (`read`, `write`, `edit`, `bash`). TBound checks every
  call against a fixed policy and answers `ALLOW` or `DENY`; a denial comes with
  a short hint naming what is allowed instead.
- Allowed `bash` commands run in a fresh, isolated container ("command cell"):
  rootless Podman, no network, Landlock, seccomp, its own user id.
- Pi works on a private copy. Its changes reach the real repository ("live
  root") only through a controlled publication step, and only inside the
  allowed output paths.
- Every step is written to append-only journals with signed evidence.
- Independent checkers ("oracles") measure whether anything forbidden happened:
  a protected file changed, data left the machine, a process survived or escaped.

TBound is a Linux system. It does not turn Windows or macOS into a security
boundary.

## 2. Where to run it

| Setup | Supported for | Notes |
|---|---|---|
| **Native Linux host** | Building, tests, the development fixtures, and the governed service | Needs the host requirements below |
| **Windows + Linux VM** (e.g. Hyper-V, VirtualBox) | The governed service runs **inside the VM** | Treat the VM as the Linux host; reach it over SSH with strict host-key checking (pin the key, the DHCP address can change) |
| **Windows + WSL** | Building, the test suite, the development fixtures | Not a governed host: WSL lacks the required Landlock ABI and a pure cgroup v2 setup |

Host requirements for the governed service:

- Linux kernel with **Landlock ABI 8** and **cgroup v2** (unified hierarchy).
- **systemd**, **rootless Podman** (4.9+) with **crun**.
- A dedicated, unprivileged service user, and an ext4 filesystem for the managed
  live root.
- **Node.js 22.19+** and the **Pi CLI 0.87.1** (`npm install --global @earendil-works/pi-coding-agent@0.87.1`).
- To build: **Go 1.27.1**.

## 3. Install

```sh
# from a Linux checkout of this repository
make build VERSION=dev
bash install/install.sh --version dev --local-dist dist --no-path
export PATH="$HOME/.local/share/tbound/bin:$PATH"
tbound-doctor --pi "$(command -v pi)"    # advisory health check
```

Details: [docs/install.md](docs/install.md), [docs/doctor.md](docs/doctor.md).

## 4. Try it without a governed host

Two fixtures run without any setup. Their receipts say `claim_bearing=false`:
they show the flow, not a security boundary.

```sh
cd supervisor && go build -o "$HOME/.cache/tbound-build/tbound" ./cmd/tbound
"$HOME/.cache/tbound-build/tbound" serve --pi --native-fixture   # Go-only
"$HOME/.cache/tbound-build/tbound" serve --pi --dev-fixture      # real Pi CLI, scripted provider
```

The `--dev-fixture` needs a few environment variables (Pi, Node, adapter path,
a private ext4 directory): see [docs/launch.md](docs/launch.md).

## 5. Set up a governed host (one time)

This is the part that makes TBound a real boundary. In short:

1. **Prepare the host**: dedicated service user, rootless Podman storage for it,
   the managed live-root filesystem, systemd socket + service units.
2. **Build and sign the cell image** that runs Pi and the `bash` cells.
3. **Measure and sign** (offline, with your own Ed25519 admin key that never
   leaves your machine): the **host profile** (what this host is), the **cell
   template**, and the **managed live-root registration** (which directory may
   be written, by whom).
4. **Install** the signed files and the pins with the operator dispatcher
   (a narrow, root-owned `sudo` command), then start the socket.

The full procedure, with every measured field: [docs/governed-setup.md](docs/governed-setup.md)
and the guest runbooks in [infra/guest/](infra/guest/README.md) (including
`PROVISIONING-SIGNING.md`).

## 6. Run a governed task

The client sends a task to the local service. The output scope says which paths
`bash` may change; read/write/edit are checked by the policy on their own.

```sh
tbound serve --pi --task-id TASK_ID \
  --task-request-id UNIQUE_REQUEST_ID \
  --bash-output out/result.txt \
  --task "approved task text"
```

- `--bash-output PATH` (repeatable): exact relative paths `bash` may change.
  `--bash-no-outputs`: `bash` may run but change nothing. Neither: `bash` is off.
- The answer is one of: `completed` (with a receipt: tool sequence, verdicts,
  publication status), `refused` (with a stable error code and a hint), or
  `unknown` (the outcome could not be proven; see section 8).

Details: [docs/launch.md](docs/launch.md), [docs/production-pi-session.md](docs/production-pi-session.md),
[docs/publication-contract.md](docs/publication-contract.md).

## 7. Updating a governed host ("freeze")

Never copy binaries to a governed host by hand. Every update goes through a
freeze of one exact Git commit:

1. **Gate**: build, vet, cross-build, format check and the full test suite with
   the race detector, on a clean export of the commit.
2. **Freeze**: build the binaries and the cell image, record their digests
   ("pins"), capture the test evidence, sign.
3. **Install**: archive the previous service state, install the new binaries,
   image and pins, re-run the host checks.
4. **Smoke**: run one governed task that reads, edits, runs `bash` and publishes.

Each request uses a one-use admission id from the signed service config, so a
repeated run needs a new id. Scripted freeze kits implement these steps; see
[infra/guest/README.md](infra/guest/README.md).

## 8. Operating

| Situation | What to do |
|---|---|
| Health check | `tbound-doctor --pi "$(command -v pi)"` |
| A run ended `unknown` | Read the service log (`journalctl -u tbound-pi.service`): it names the cause. The next request is blocked until the unknown run is reconciled (`tbound abandon-unknown`, or the operator verb `abandon`) or the service state is archived before a fresh install. |
| `refused` | The error code and hint say why (e.g. a prior run is not settled, the request is not admitted). |
| Guest/VM unreachable | Its DHCP address changed; look it up and reconnect with the pinned host key. |

## 9. Evaluating TBound (research workflow)

The thesis evaluates TBound on a corpus of test cases (attacks, normal tasks,
borderline cases) and two measures: **PVR** (how often an attack still causes a
forbidden effect) and **ORR** (how often an allowed task is wrongly blocked).

1. **Corpus**: cases adapted from established benchmarks (with source, license
   and the adaptation recorded per case); a pilot set in `corpus/pilot/`
   (rebuild deterministically with `python3 -I corpus/pilot/build.py`).
2. **Deterministic checks** of every case (schema, sources/licenses, overlaps,
   leaks, oracle mutation controls).
3. **AI construct-review panel**: three independent model families rate every
   case blind, then against its claimed labels; agreement is measured with
   Fleiss' kappa (`tbound-panel`, below).
4. **Human sign-off** of the panel's audit list by a non-author reviewer.
5. **Corpus mode** on the governed host (signed corpus registration), then the
   **pilot** and the full runs with `tbound-run plan | run | export | report`.
6. **Evidence quality**: `tbound-run evq` and `tbound-run route-report` turn the
   host evidence into the evidence-quality result; every obligation must pass.
7. **Pre-registration**, then the confirmatory run.

### Running the AI panel

`go build` prints nothing when it succeeds; it just creates the program.

```sh
cd supervisor
go build -o tbound-panel ./cmd/tbound-panel          # tbound-panel.exe on Windows
cp cmd/tbound-panel/panel.default.json ../panel.json
```

Put exactly **three** models from **three different families** into `"models"`
in `panel.json` (OpenRouter ids; routers and `-latest` aliases are refused; the
families that authored the cases or are evaluated are refused too). Each entry
needs real, non-zero prices so the spend cap has a worst case:

```json
{"id": "vendor/model-a", "temperature": 0, "max_tokens": 4000,
 "prompt_usd_per_mtok": 0.30, "completion_usd_per_mtok": 1.20}
```

```sh
./tbound-panel --config ../panel.json --manifest ../corpus/pilot/manifest.draft.json --estimate
./tbound-panel --config ../panel.json --manifest ../corpus/pilot/manifest.draft.json --out ../panel-dry --dry-run
export OPENROUTER_API_KEY=...        # a key with a credit limit; never stored by the tool
./tbound-panel --config ../panel.json --manifest ../corpus/pilot/manifest.draft.json --out ../panel-run --live
```

- `--estimate` and `--dry-run` cost nothing. `--live` stops hard at `budget_usd`.
- Rerunning the same command with the same `--out` resumes an interrupted run.
- Gates per checklist item: kappa ≥ 0.80 and agreement ≥ 90% = pass;
  0.60–0.79 = revise; below 0.60 = stop; any missing case = no pass.
- Give the reviewer `audit.md`, `audit.json`, `audit-packets/` and `stats.md`.

Full guide: [docs/panel-run.md](docs/panel-run.md).

## 10. Where to read more

- [docs/README.md](docs/README.md): what each document covers.
- [docs/architecture.md](docs/architecture.md): components and trust boundaries.
- [docs/status.md](docs/status.md): what is verified and what is not.
