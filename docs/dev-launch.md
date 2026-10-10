# Development launch: `tbound serve --pi --dev-fixture` (2026-10-08)

## What this route is

`tbound serve --pi --dev-fixture` is the explicit, **non-claim-bearing**
development launch of the governed Pi workflow. It:

- launches the **real pinned Pi 0.87.1 SDK worker** (`adapter/src/governed-pi-worker.ts`)
  over the real inherited-FD channels;
- drives the real Go **provider conversation bridge** with a scripted **offline**
  provider (no network, no credentials);
- runs the exact **E05-shaped four-tool workflow** through the real
  `DurableExecutor` and session repository:

  ```text
  read(g0) -> edit(g1) -> bash(g2) -> read(g2)
  ```

- writes one bounded strict-JSON receipt to stdout.

It is Linux-only. The `--dev-fixture` flag is opt-in and never reaches the
production `serve --pi` admission path.

## Opt-in environment

All four variables are required and must be absolute paths; the route refuses
with an `opt-in` error if any is missing, and never falls back to the Go-only
`--native-fixture` or a stock Pi CLI.

```text
TBOUND_DEV_FIXTURE_NODE          pinned Linux Node binary (v24.15.0)
TBOUND_DEV_FIXTURE_ADAPTER       adapter source root (contains src/)
TBOUND_DEV_FIXTURE_NODE_MODULES  pre-existing pinned node_modules (no install)
TBOUND_DEV_FIXTURE_ROOT          private ext4 mode-0700 root owned by the UID
```

Example:

```sh
cd supervisor
PRIVATE="${TMPDIR:-$HOME}/tbound-dev-fixture"
mkdir -p "$PRIVATE"
chmod 700 "$PRIVATE"
go build -o "$PRIVATE/tbound" ./cmd/tbound
TBOUND_DEV_FIXTURE_NODE=/home/jeli2k/node-v24.15.0-linux-x64/bin/node \
TBOUND_DEV_FIXTURE_ADAPTER="$(cd .. && pwd)/adapter" \
TBOUND_DEV_FIXTURE_NODE_MODULES="$(cd .. && pwd)/adapter/node_modules" \
TBOUND_DEV_FIXTURE_ROOT="$PRIVATE" \
"$PRIVATE/tbound" serve --pi --dev-fixture
```

## Receipt shape

The receipt stays claim-free. Observed fields on a passing run:

```json
{
  "mode": "dev-fixture",
  "claim_bearing": false,
  "provider_exchange": false,
  "containment": "not-established",
  "settlement": "UNKNOWN",
  "publication": "not-attempted",
  "worker_ready": true,
  "prompt_admitted": true,
  "turn_completed": true,
  "proposal_count": 4,
  "provider_turns": 5,
  "tool_call_count": 4,
  "tool_result_count": 4,
  "initial_generation": "g0",
  "final_generation": "g2",
  "generations": ["g0", "g1", "g2"],
  "tool_lineage": [
    {"sequence": 1, "tool": "read",  "generation_from": "g0", "generation_to": "g0"},
    {"sequence": 2, "tool": "edit",  "generation_from": "g0", "generation_to": "g1"},
    {"sequence": 3, "tool": "bash",  "generation_from": "g1", "generation_to": "g2"},
    {"sequence": 4, "tool": "read",  "generation_from": "g2", "generation_to": "g2"}
  ],
  "bash_settlement": {
    "tool_call_id": "dev-call-bash-g1",
    "generation_to": "g2",
    "exit_code": 0,
    "exit_observed": true,
    "command_containment_status": "not-established",
    "command_runner_profile": "dev-wsl-non-claim-bearing:...non-claim-bearing..."
  }
}
```

`publication` is `"not-attempted"`. The dev route does **not** publish: the
existing `workflow.Finalizer` rejects lease-ending workflows such as
`read -> edit -> bash -> read`, and this route fabricates no publication and no
settler. See `docs/governed-session-composition.md` for the lease-origin
publication blocker.

## The Bash step: sandbox-first, bounded fallback

The dev route selects its Bash runner at launch:

1. **Preferred — the reviewed sandbox runner.** `newDevelopmentFixtureCommandRunner`
   first constructs `sessionlaunch.NewDevelopmentCommandRunner`, which adapts the
   existing `internal/sandbox` namespace/Landlock/seccomp cell to the
   sessionrepo `CommandRunner` seam. When the development sandbox is available
   (as observed on this WSL host: Landlock ABI 1, seccomp-bpf, user/mount/pid/
   net namespaces), this runner is used and records its measured,
   non-claim-bearing `dev-wsl-non-claim-bearing:<mechanisms>` profile.
2. **Fallback — the bounded, non-contained runner.** Only when the sandbox
   cannot be established does the route fall back to
   `devFixtureCommandRunner` (`supervisor/cmd/tbound/dev_fixture_bash_linux.go`).
   That runner runs `/bin/bash -lc` directly under the supervisor's own
   credentials and namespaces, with its working directory pinned to the private
   mode-0700 session-repository command view and a fixed minimal environment. It
   bounds wall-clock time and output, starts the command in its own process
   group, and proves the group empty before reporting the scope settled. It
   records `dev-fixture-bounded-non-claim-bearing:direct-bash`.

Neither runner is a production containment profile, and the receipt records the
exact profile that ran, so the route never hides which one was used. The
sandbox runner also reports `cgroup2=not-established` when no delegated cgroup
exists; it never claims resource containment it did not establish.

Fail-closed behavior is preserved: a missing runner, a malformed `bash` argument
payload, an unregistered command shape, a command whose process scope does not
settle, and an over-bound or timed-out command all withhold the result instead
of running or claiming anything.

## What this route does NOT claim

- **No production containment.** `containment="not-established"` and the
  lifecycle settlement is `UNKNOWN`. The Bash step may run in the development
  sandbox cell (`dev-wsl-non-claim-bearing`, recording `cgroup2=not-established`
  when no delegated cgroup exists). That is development evidence only — not the
  frozen production containment, resource-cgroup, or settlement profile.
- **No provider exchange or authenticity.** The provider transport is a scripted
  offline doer; `provider_exchange=false`; no credentials are loaded and no
  network request is made.
- **No D06 / private-Git provenance.** The dev store uses non-claiming
  development callbacks; it does not bind a sealed read-only source view or
  private-Git provenance.
- **No publication, G1, or H1.** `publication="not-attempted"`; the route is
  component development evidence only.

## Production routes still refuse

```text
tbound serve --pi                         -> exit 2 (native Pi host profile is missing)
tbound serve --pi --native-host-profile … -> exit 2 (not an installed, attested production runtime)
```

`--dev-fixture` is mutually exclusive with `--native-fixture` and
`--native-host-profile`. Production `serve --pi` remains refusal-only until a
reviewed host profile, real containment, provider continuity, and D06
integration exist.
