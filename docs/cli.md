# Command reference

This page lists the user-facing commands. `tbound --help` prints the command
names. Where a command has flags, its usage text lists them. Commands marked
**Linux only** exit with an error on other systems.

Commands are in the `tbound` binary unless stated otherwise. The installer puts
`tbound`, `tbound-doctor`, and `tbound-profile` in `<prefix>/bin`. It prints the
`export PATH=...` line for you to add, unless you pass `--no-path`.

## Exit status

- `0`: success.
- `2`: usage error, refusal, or any other error that does not carry its own exit
  code. The `tbound` process exits with `2` for these.
- Some commands define their own codes in their usage text. `tbound provider`
  uses `3` for a failed credential or provider check, for example.

## Commands

| Command | What it does | Platform | Who runs it |
|---|---|---|---|
| `tbound --help` | Lists the commands. | Any | Anyone |
| `tbound serve` | Runs one task (fixture or governed). | Linux only | Developer (fixtures); operator (governed) |
| `tbound chat` | Interactive governed chat through the product service (a client; the service runs on Linux). | Client on any platform; service on Linux | Product user |
| `tbound provider` | Manages model-provider credentials for the product service. | Linux only | Service owner |
| `tbound project` | Bundles, imports, and exports a managed project. | Linux only | Project owner, then service |
| `tbound product` | Product host administration: setup, update, rollback, uninstall, doctor, turns. | Linux only; setup and update as root | Operator |
| `tbound release` | Verifies release manifests and signatures. | Any | Anyone verifying a release |
| `tbound abandon-unknown` | Settles a run whose outcome is unknown. | Linux only; root | Operator |
| `tbound verify-retained` | Re-checks a completed run's retained evidence. | Linux only; root | Operator |
| `tbound-doctor` | Reports prerequisites. Advisory. | Linux checks more fully | Anyone |
| `tbound-profile` | Generates, signs, and verifies the Linux host profile. | Linux only | Operator |

`product-serve` and `workflow-root-init` are also dispatched but not listed in
`--help`, so this page does not document them.

## `tbound serve`

Development fixtures:

```sh
tbound serve --pi --native-fixture    # Go-only synthetic lifecycle; claim_bearing=false
tbound serve --pi --dev-fixture       # real Pi CLI 0.87.1 with a scripted provider; claim_bearing=false; needs env vars (setup.md)
```

Governed request (Linux, with an installed, admitted service):

```sh
tbound serve --pi --task-id TASK_ID --task-request-id REQUEST_ID \
  --bash-output out/result.txt --task 'EXACT_REGISTERED_PROMPT'
```

- `--bash-output PATH` (repeatable): exact relative paths `bash` may change.
  A directory is not a subtree.
- `--bash-no-outputs`: `bash` may run but change no paths. Do not combine it
  with `--bash-output`. With neither flag, `bash` is disabled.
- `--task-id`, `--task-request-id`, `--task`: must match the registered
  configuration exactly. A changed prompt is refused.
- `--service`: internal. It is used by the systemd socket, not by hand.

Read [setup](guides/setup.md#managed-linux-client) before a governed request.

## `tbound chat`

Flags: `--project`, `--provider`, `--mode`, `--read-only`, `--session`,
`--socket`, `--budget-host-calls`, `--budget-tokens`, `--budget-seconds`. A
write waits for your Publish or Discard. See the
[first governed product turn](guides/product-first-turn.md).

## `tbound provider`

```text
tbound provider list
tbound provider setup --profile ID --service-user NAME [--from-file PATH] [--replace]
tbound provider status --profile ID
tbound provider check --profile ID --offline
tbound provider check --profile ID --live --max-tokens N --yes
tbound provider rotate --profile ID --service-user NAME [--from-file PATH]
tbound provider remove --profile ID
```

Keys come only from a no-echo terminal prompt or a protected file. The live check
sends exactly one request. Exit `3` means the credential or provider check failed.

## `tbound project`

```text
tbound project bundle  --source DIR [--exclude PATH]... --out FILE
tbound project import  --bundle FILE --dest DIR [--manifest-out FILE]
tbound project export  --baseline-manifest FILE --current DIR --out FILE
tbound project apply   --bundle FILE --worktree DIR --state-dir DIR [--exclude PATH]... [--direct --writer-exclusion-held]
```

`bundle` runs as the project's owner and always excludes `.git`. `import` and
`export` run as the service user. `apply` verifies an export and writes a patch
to `--state-dir` without changing the worktree. Its `--direct` mode writes into
the worktree and needs a guarantee that no other writer is active.

## `tbound product`

Subcommands: `registration`, `calibration`, `image`, `setup`, `update`,
`rollback`, `uninstall`, `doctor`, `turns`, `self-test`, and `help`.
`setup`, `update`, `rollback`, and `uninstall` run as root on the product host.
Run `tbound product help` for the full usage text.

## `tbound release`

```text
tbound release verify      --manifest F --signature F --public-key F [--fingerprint HEX16] [--artifact F]...
tbound release manifest    --out F ARTIFACT...
tbound release sign        --manifest F --key PRIVATE --out F
tbound release keygen      --out DIR
tbound release fingerprint --public-key F
```

`verify` checks the signature first, then each artifact's size and SHA-256. Use a
verifier you trust independently of the download. `manifest`, `sign` and `keygen`
are for the release maintainer. See [`install/install.sh`](../install/install.sh) for
the trust notes.

## `tbound-doctor`

```sh
tbound-doctor --pi "$(command -v pi)"   # check Pi 0.87.1 and the other prerequisites
tbound-doctor --json                    # machine-readable output
```

Doctor is advisory. `governed_ready` is always false here, because runtime
admission decides that. See [setup](guides/setup.md#check-prerequisites-with-doctor).

## Research tools

These are built from `supervisor/cmd/` for the study and are not part of the
product workflow. Their flags change with the study, so use the study records:

- `tbound-run`: corpus-mode plan, run, export, report, and route reports.
- `tbound-panel`: optional supplementary AI analysis. See the
  [panel guide](guides/panel-run.md).
- `tbound-hint-eval`: evaluation of denial hints.
- `tbound-provider`: provider tooling. See [developer tools](guides/developer-tools.md).

The release archive also carries `tbound-cell-entrypoint` under `libexec/tbound/`.
It is an input to the signed command cell. Never run it directly.
