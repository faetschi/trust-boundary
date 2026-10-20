# Launch TBound

The Go-only native fixture and an offline Pi CLI development fixture are
available on Linux. Start the native fixture with:

```sh
mkdir -m 700 -p "$HOME/.cache/tbound-tmp"
export TMPDIR="$HOME/.cache/tbound-tmp"
cd supervisor
go build -o /tmp/tbound ./cmd/tbound
/tmp/tbound serve --pi --native-fixture
```

The audit journal rejects any ancestor that is writable by group or other. The
default `/tmp` is world-writable, so the private `TMPDIR` above is required on
a stock host; without it the fixture fails closed with
`writable ancestor mode 0777`.

The JSON receipt marks `claim_bearing=false` and
`containment=not-established`. Use a private output directory in place of
`/tmp` if required by your host policy.

## Offline Pi CLI fixture

This development route launches the official Pi 0.87.1 CLI over RPC, the
isolated TBound extension, and a scripted offline provider, replacing the
former embedded-SDK development fixture. It requires absolute paths and an
existing owned mode-0700 directory on ext4. From the repository root, set:

```sh
export TBOUND_DEV_FIXTURE_PI="$(realpath "$(command -v pi)")"
export TBOUND_DEV_FIXTURE_NODE="$(realpath "$(command -v node)")"
export TBOUND_DEV_FIXTURE_ADAPTER="$(pwd)/adapter"
export TBOUND_DEV_FIXTURE_ROOT="$HOME/.local/state/tbound-dev-fixture"
export TMPDIR="$HOME/.cache/tbound-tmp"
mkdir -m 700 -p "$TBOUND_DEV_FIXTURE_ROOT" "$TMPDIR"
test "$(stat -f -c %T "$TBOUND_DEV_FIXTURE_ROOT")" = ext2/ext3
test "$(stat -c %a "$TBOUND_DEV_FIXTURE_ROOT")" = 700
mkdir -m 700 -p "$HOME/.cache/tbound-build"
cd supervisor
go build -o "$HOME/.cache/tbound-build/tbound" ./cmd/tbound
"$HOME/.cache/tbound-build/tbound" serve --pi --dev-fixture
```

The fixture needs no `NODE_MODULES`, credentials, or network provider. It
processes the scripted read/edit/bash/read tool sequence against fixture data.
Its receipt should report four proposals/results, five scripted provider turns,
`worker_ready=true`, `prompt_admitted=true`, `turn_completed=true`,
`provider_exchange=false`, `settlement=UNKNOWN`,
`containment=not-established`, `publication=not-attempted`, and
`claim_bearing=false`. It does not establish production admission, the
production CLI profile, containment, or qualification. See
[architecture](architecture.md) and [status](status.md).

## Managed Linux client

On Linux, ordinary `serve --pi` sends a task request to a registered local
managed service. The task must be host-admitted, and the connecting client UID
must be allowed by its signed registration. Only operator-trusted clients should
have that permission. The provider/model cannot set the output scope.

Bash is disabled when neither scope option is supplied. To permit exact paths
to change, repeat `--bash-output`:

```sh
tbound serve --pi --task-id TASK_ID \
  --task-request-id UNIQUE_REQUEST_ID \
  --bash-output task.txt \
  --bash-output build/result.txt \
  --task "approved task text"
```

Paths are exact relative paths, with at most 128 entries. The CLI sorts them and
rejects invalid or duplicate entries. Include every changed object, including a
directory if Bash creates it; paths do not authorize a whole directory subtree.
Use `--bash-no-outputs` to permit Bash only when no paths change. The two scope
options cannot be combined. They affect Bash only; read/write/edit keep their
typed paths and existing gate checks.

After settlement, Store checks the actual delta against this scope. A change
outside it is denied, its candidate is not promoted, and its result is withheld.
Scope does not define shell-content or external-effect policy; supplying it
does not qualify the host or establish G1/H1. See [delta admission](delta-admission.md).

The client currently
returns `service unavailable` if the service is absent. The internal
`--service` mode is for the systemd socket-activated server; it is not a
standalone user launch. Provisioning, real signed-profile source qualification,
client/server operation, and a positive governed Pi launch remain unverified.

## Existing Windows Hyper-V VM

TBound runs inside Ubuntu, not as a Windows security boundary. To resume the
existing ABI 8 guest, open elevated PowerShell:

```powershell
Get-VM -Name 'TBound-Ubuntu-2404-ABI8' | Select-Object Name, State
Start-VM -Name 'TBound-Ubuntu-2404-ABI8' # only when State is Off
vmconnect.exe localhost 'TBound-Ubuntu-2404-ABI8'
```

At the guest console, log in and check the current DHCP address:

```sh
ip -brief address show eth0
```

Use the pinned SSH host key and the current address if connecting remotely. The
DHCP address can change; keep strict host-key checking and pass
`-o HostKeyAlias=172.18.155.19` so a new address is verified against the
pinned key. Shut Ubuntu down cleanly with `sudo poweroff`; do not use host
power-off as a routine shutdown.

The ABI 8 guest (`tbound-abi8-seed`, kernel 7.0.0-38-generic) is measured but
not qualified, and it is not a clean G1 baseline. The retired
`TBound-Ubuntu-2404` guest (kernel 6.8, Landlock ABI 4) is out of scope. For new-host and
offline procedure details, see [reproducible setup](reproducible-setup.md)
and the guest [offline runbook](../infra/guest/README.md).
