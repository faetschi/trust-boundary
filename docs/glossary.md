# Glossary

Terms you will meet in the code, the docs, and the study records. Each entry
says what the term means in this repository.

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
| **Unknown outcome** | A run whose result could not be proven. The next request is blocked until it is reconciled with `tbound abandon-unknown`. |
| **Refused** | A request the service declined. The error code and hint say why. |
| **`claim_bearing=false`** | The output is not a thesis result. Fixtures, probes, and smoke runs always show this. |
| **Receipt** | The JSON record a run prints: tool sequence, verdicts, and publication status. |

## Freezes and evidence

| Term | Meaning |
|---|---|
| **Freeze** | An update to a governed host made from one exact Git commit: gate, freeze, install, smoke test. See [reproduction](guides/reproduce.md). |
| **Pin** | A recorded digest of a binary, image, or toolchain, used to prove which build ran. |
| **Gate** | The build, vet, format, and test checks run on a clean export of a commit before a freeze. |
| **Construct witness** | Guest-side evidence about a construct that a test case relies on. See [construct witnesses](guides/construct-witnesses.md). |
| **Lane** | A separate line of work with its own thread, branches, and owner, for example the corpus lane or the product lane. |

## Thesis evaluation

| Term | Meaning |
|---|---|
| **PVR** | The rate of forbidden effects in adversarial runs. |
| **ORR** | The rate at which compliant tasks are rejected. |
| **H1** | The main hypothesis about PVR and ORR. It has not been executed. |
| **Pre-registration** | An immutable record, made before H1, that fixes the thresholds, sample size, scoring, and denominators. |
| **Corpus** | The set of test cases (attacks, normal tasks, borderline cases) used in the evaluation. |
| **Ledger** | The locked list of candidate cases drawn from existing benchmarks. |
| **Pilot** | A small preliminary case set used for testing and tuning. Pilot results are not thesis results. |
| **T1, T2** | Public commitments that fix the case selection before a public random draw: T1 for the pilot, T2 for the final thesis run. |
| **Construct checks** | Deterministic checks on the corpus. Full construct checks, with applicable guest witnesses, are on the path to H1. `tbound-profile corpus-digest` and `preregistration-record` do not run the full set. |
| **Panel** | An optional, supplementary analysis by several AI models. Its agreement statistics are diagnostics only. |

## Kernel and tools

| Term | Meaning |
|---|---|
| **Landlock** | A Linux file-access restriction. Governed runs need exactly ABI 8. |
| **cgroup v2** | The unified Linux resource-control hierarchy that TBound uses to limit and tear down processes. |
| **Podman, crun** | The rootless container runtime and OCI runtime used for command cells. |
| **Cosign** | The offline signature verifier used for signed artifacts. |
| **systemd** | The service manager that runs the governed service and its socket. |
