# Launch TBound on Linux

**Updated 2026-10-12.** This is the run guide for the current source tree. It covers a local development smoke run and the existing Linux guest. It does not claim an experiment-ready governed deployment.

## Current status and next work

- **Implemented:** reviewed governance components; Linux managed-service client and
  systemd socket-activated service endpoints; kernel-credential peer authentication,
  request journaling/idempotency, and service-mode source qualification with
  baseline revalidation and source-digest binding. Mismatches refuse before worker exposure.
- **Verified on the existing VM:** the current source passed the full Linux Go suite,
  full race suite, and a private-output `--native-fixture` launch.
- **Still unqualified:** real signed-profile
  registration, service provisioning and client/server/restart tests, and any positive
  governed Pi session. No real provider turn or G1 run has passed; containment is
  not-established and G1=false.
- **M1, the thesis path:** qualify signed
  source registration and provision/test the systemd service; then complete the Pi
  read → edit → bash → read provider workflow, D06/D09 isolation, publication
  receipts, independent effect checks, and registered G1 gates.
- **M2 (the parallel product/packaging shorthand in TASK_STATE.md):** parallel
  product work, not a thesis evidence gate. Next: (1) verify install and package
  behavior; (2) resolve canonical license text and a portable pinned Pi artifact;
  (3) validate Linux RPM, Homebrew, and container builds; (4) distribute/publish
  only after acceptance. Public npm/curl distribution is not available; D15 keeps
  the thesis claim path CLI-first.

Current Hyper-V access, fresh guest-probe results, and the unqualified HWE boot observations are in the [VM section](#existing-windows-hyper-v-vm) below. Treat the guest as a development reference, not a clean G1 baseline.

## Native Linux: run the Go-only fixture

Use a native Linux checkout. WSL is development-only and is not the evaluation profile. Go is required; this fixture does not need Pi, Node, provider credentials, or a network request.

~~~sh
cd /path/to/trust-boundary/supervisor
PRIVATE="$(mktemp -d)"
chmod 700 "$PRIVATE"
go build -o "$PRIVATE/tbound" ./cmd/tbound
"$PRIVATE/tbound" serve --pi --native-fixture
~~~

This runs the durable Go fixture and prints a non-claim-bearing receipt. It does **not** launch Pi or establish containment.

To run the real pinned Pi 0.87.1 SDK worker with a scripted offline provider (no network or credentials), use the non-claim-bearing `--dev-fixture` route. Copy the complete setup and invocation from the [opt-in environment example in dev-launch.md](dev-launch.md#opt-in-environment); it requires pinned Node v24.15.0, the adapter source, pre-existing pinned `node_modules`, and a private mode-0700 ext4 root. The final launch command is:

~~~sh
"$PRIVATE/tbound" serve --pi --dev-fixture
~~~

This route is Linux-only and non-claim-bearing. On the Hyper-V path, run it inside Ubuntu using the guest checkout and absolute Linux paths; do not use the Windows paths from the host.

## Existing Windows Hyper-V VM

Resume the existing `TBound-Ubuntu-2404` guest; do not create another VM for this run. From elevated PowerShell, check its state, start it only if Off, then open the console:

~~~powershell
Get-VM -Name 'TBound-Ubuntu-2404' | Select-Object Name, State
Start-VM -Name 'TBound-Ubuntu-2404'   # only when State is Off
vmconnect.exe localhost 'TBound-Ubuntu-2404'
~~~

At the Ubuntu console, sign in and discover the current guest address. DHCP addresses change; `172.19.193.190` is only a historical address from an earlier help sheet:

~~~sh
ip -brief address show eth0
~~~

If SSH is available, use the discovered current address. On first connection, compare the ED25519 fingerprint shown by SSH with the fingerprint pinned in the [Hyper-V installation record](../infra/hyperv/INSTALLATION-RECORD.md) or verified at the guest console before accepting it. Keep strict host-key checking enabled:

~~~powershell
ssh -o StrictHostKeyChecking=ask -o HostKeyAlgorithms=ssh-ed25519 `
  -i "$env:USERPROFILE\.ssh\id_ed25519" tboundadmin@<current-guest-ip>
~~~

On 2026-10-12, `CodexSandboxUsers` established strict SSH as `tboundadmin` to `192.168.251.23/20` after operator console login; the ED25519 host fingerprint matched the pin in the [Hyper-V installation record](../infra/hyperv/INSTALLATION-RECORD.md). The address is dynamic. The Hyper-V ACL still denies `Get-VM` to this execution context, and the host alias still does not resolve. A fresh guest probe reported 13 PASS, 0 FAIL, 2 UNVERIFIED; it reported kernel `6.8.0-146-generic` and Landlock ABI 4. HWE kernel `7.0.0-38` reached userspace on repeated boots and ran for minutes; inspected journal tails showed no panic/oops, but the reboot cause is unknown and HWE remains unqualified. The help-sheet address `172.19.193.190` was unreachable and remains historical. G1 is still false; this guest is not a clean G1 baseline.

Once signed in to Ubuntu, use the guest checkout's actual Linux path (Windows paths do not work there). Run the same non-claim-bearing Go fixture:

~~~sh
export TBOUND_TREE=/absolute/path/to/the/guest-checkout
cd "$TBOUND_TREE/supervisor"
PRIVATE="$(mktemp -d)"
chmod 700 "$PRIVATE"
go build -o "$PRIVATE/tbound" ./cmd/tbound
"$PRIVATE/tbound" serve --pi --native-fixture
~~~

A private 233-file snapshot of the current Windows source and its required test fixture was hash-verified in this guest on 2026-10-12. The full Go suite, full race suite, and `--native-fixture` launch passed there. Use your own guest checkout for repeatable runs and verify its revision before treating its results as current. For the Pi SDK development fixture, follow [dev-launch.md](dev-launch.md). For offline verification, follow [setup.md](setup.md): its powered-off host Verify gate requires exactly one adapter with no switch attached, and the guest boots with that adapter disconnected. That path is component/offline verification, not a governed TBound launch. Shut Ubuntu down normally with `sudo poweroff`; avoid host `Stop-VM` as a routine shutdown.

## Governed client status

On Linux, ordinary `tbound serve --pi` is a client for the local registered service. It does not accept profile, source, or workspace paths; those are service-owned. A client request has this shape; replace the placeholders with registered values and approved task text:

~~~sh
tbound serve --pi --task-id TASK_ID --task-request-id UNIQUE_REQUEST_ID --task "approved task text"
~~~

The client currently returns **service unavailable** when the registered service is absent. The `tbound serve --pi --service` command is the internal systemd socket-activated server entry point, not a standalone launch command. The server and source-capture code exist, but the service has not been provisioned and its real signed-profile path and positive launch remain unverified. Do not describe a fixture receipt, a successful build, or a capability probe as a governed Pi run.