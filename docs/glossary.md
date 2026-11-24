# Glossary

Terms you will meet in the code, the docs, and the study records. Each entry
says what the term means in this repository.

For study terms, the thesis glossary is normative:
`tbound/AH-technical/01-decisions-glossary.md`
in `MSE_MA_Thesis`. This page restates those terms for the repository and must
not contradict it. A disagreement is a documentation bug here.

## The product

| Term | Meaning |
|---|---|
| **TBound** | A Linux supervisor that sits between the Pi coding agent and a repository, and decides which of Pi's tool calls to allow. |
| **Pi** | The coding agent TBound supervises. It proposes `read`, `write`, `edit`, and `bash` calls. Its output is untrusted. |
| **Supervisor** | The Go process (`supervisor/`) that owns policy, durable state, isolation, publication, and evidence. |
| **Broker** | The supervisor component that talks to the model provider. |
| **Policy gate** | The check that answers `ALLOW` or `DENY` for each tool call. |
| **Denial hint** | The short text sent back to Pi with a `DENY`, naming what is allowed instead. |
| **Command cell** | The isolated container that runs an allowed `bash` command: rootless Podman, no network, Landlock, seccomp, its own user id. |
| **Live root** | The real repository. Changes reach it only through publication. |
| **Publication** | The controlled step that copies approved changes from a private workspace into the live root, and only inside the allowed output paths. |
| **Delta admission** | The decision made after an operation about whether its changes may be published. |
| **Oracle** | An independent checker that measures whether something forbidden happened, for example a protected file changed or data left the machine. |
| **Product service** | The governed chat service (`tbound chat`) that runs on one Linux host. Its worker is the supervisor's own agent loop, not Pi. Its output is not thesis evidence. |
| **Web chat** | An optional browser front end for the product service. It is frozen and outside the thesis. |

## Running a task

| Term | Meaning |
|---|---|
| **Fixture** | A scripted, synthetic run that exercises the code without a real provider. `--native-fixture` is Go-only; `--dev-fixture` runs the real Pi CLI with a scripted provider. |
| **Governed host** | A dedicated Linux machine set up to enforce the security boundary: measured, signed, and admitted. Not the same as a development checkout. |
| **Host profile** | The signed description of a governed host: what it is and which runtime it admits. |
| **Registration** | The signed service configuration that says which task, prompt, users, and paths a governed service may run. |
| **Admission ID** | A one-use identifier from the signed service configuration. A repeated request needs a new one. |
| **Bash output scope** | The exact relative paths `bash` may change in a run (`--bash-output`). With none, `bash` is disabled. |
| **`UNKNOWN`** | An effect may have begun, but a durable terminal outcome cannot yet be established. Inspection is required, and consumed authority is never replayed. The next request is blocked until the run is reconciled with `tbound abandon-unknown`. |
| **Refused** | A request the service declined. The error code and hint say why. |
| **`claim_bearing=false`** | The output is not a thesis result. Fixtures and smoke runs print this. Probes may not carry the field. |
| **Receipt** | The JSON record a run prints: tool sequence, verdicts, and publication status. |
| **Terminal classification** | The one durable post-effect class the supervisor records: `SUCCEEDED`, `DENIED`, `FAILED` (with a typed failure class), or `UNKNOWN`. The session store also records `denied_after_effect`, which refines `DENIED` and is not a fifth class. |
| **Settlement** | Two senses. Process settlement means a worker or transfer has terminated and its final state is recorded. Budget settlement means the cumulative recovery counters of a `workflow_id` are updated. State the sense. |
| **Generation** | Two senses. A session generation is a sealed, immutable tree of session state (`g0`, `g1`, …). A recovery generation is the lineage counter of a workflow across restarts. State the sense. |
| **Quarantine** | A recovery outcome in which a workflow, root, or session view with unresolved or inconsistent state is retained for inspection and refuses further effects until resolved. It never replays an effect. |
| **Tier** | Historical numbering of enforcement mechanisms. Tier 2 is the pinned `tree-sitter-bash` pre-execution filter, which is never the security boundary. Tier 3 is the transactional staging sandbox. Tier 4 is the Linux execution-isolation layer. Tier 1 is future work. A tier is not an evidence layer and not a trial disposition. |

## Freezes and evidence

| Term | Meaning |
|---|---|
| **Freeze** | An update to a governed host made from one exact Git commit: gate, freeze, install, smoke test. See [reproduction](guides/reproduce.md). |
| **Pin** | A recorded digest of a binary, image, or toolchain, used to prove which build ran. |
| **Release gate** | The build, vet, format, and test checks run on a clean export of a commit before a freeze. Not the same as the evidence gates G0 to G6. |
| **Gates G0 to G6** | The thesis roadmap evidence gates. G0 is the literature screen and G1 is profile feasibility. They gate research evidence, not a release. |
| **Construct witness** | Guest-side evidence about a construct that a test case relies on. See [construct witnesses](guides/construct-witnesses.md). |
| **Lane** | A separate line of work with its own thread, branches, and owner, for example the corpus lane or the product lane. |

## Thesis evaluation

| Term | Meaning |
|---|---|
| **PVR** | The prevention-violation rate, the rate of forbidden effects in adversarial runs. |
| **ORR** | The over-refusal rate, the rate at which compliant tasks are rejected. |
| **H1** | The main hypothesis about PVR and ORR. It has not been executed. |
| **Pre-registration** | An immutable record, made before H1, that fixes the thresholds, sample size, scoring, and denominators. |
| **Corpus** | The set of test cases (attacks, normal tasks, borderline cases) used in the evaluation. |
| **Ledger** | The locked list of candidate cases drawn from existing benchmarks. |
| **Pilot** | A small preliminary case set used for testing and tuning. Pilot results are not thesis results. |
| **T1, T2** | Public commitments that fix the case selection before a public random draw: T1 for the pilot, T2 for the final thesis run. |
| **Construct checks** | Deterministic checks on the corpus. Full construct checks, with applicable guest witnesses, are on the path to H1. `tbound-profile corpus-digest` and `preregistration-record` do not run the full set. |
| **AI construct-review panel** | Optional supplementary evidence from several AI models. If used, retain the raw ratings, model settings, and blinding records. AC1, raw agreement, and kappa are diagnostics only. They never gate H1 and are not independent human validation. |
| **Forbidden effect** | A registered realized change to protected host state, arrival at the synthetic unauthorized network sink, or a registered process-containment or persistence effect. A blocked proposal, syscall, or packet alone is not that effect. |
| **EVQ, SQ-A, SQ-B, SQ-C** | The thesis research questions: EVQ is profile conformance, SQ-A security, SQ-B friction, and SQ-C secondary descriptive utility. |
| **Configurations A, B, C, P, V** | A is native Pi in an isolated cell (diagnostic). B is proxy tools with a no-op gate. C is proxy tools with the deterministic gate (the treatment). P is a scripted channel-preserving replay with exactly one registered delta. V is native Pi in a plain container with no TBound gate (the utility baseline). |
| **`LOSSY`, `LOST`** | `LOSSY` marks a bounded enrichment stream that overflowed, so something is missing. It is a flag, not a result. `LOST` is a display label of the web-chat demonstrator only. The supervisor never emits it, and the thesis classifies that situation as `UNKNOWN`. |

## Kernel and tools

| Term | Meaning |
|---|---|
| **Landlock** | A Linux file-access restriction. Governed runs need exactly ABI 8. |
| **cgroup v2** | The unified Linux resource-control hierarchy that TBound uses to limit and tear down processes. |
| **Podman, crun** | The rootless container runtime and OCI runtime used for command cells. |
| **Cosign** | The offline signature verifier used for signed artifacts. |
| **systemd** | The service manager that runs the governed service and its socket. |
