# Try the product demo from Windows

Use this guide to chat with a running TBound product service from a Windows
workstation, in the terminal and in your browser. The service runs in a Linux
VM; Windows only needs an SSH client. To set up the VM and the service first,
follow [Run a first governed product turn](product-first-turn.md).

To create the VM, start with [Create a product VM on Windows](hyperv-from-zero.md).
The connection values below are one owner's example. Replace them with yours.

The prompts below assume the Bash-deny install from that guide. For Bash through
the signed D09 cell, follow [Enable Bash](product-bash.md) first.

Nothing here is thesis evidence.

## Before you start

You need:

- The product VM running, with `tbound product doctor` passing and the
  provider key installed.
- Git for Windows. Use its **Git Bash**; the Windows OpenSSH client can fail
  the key exchange with the Ubuntu 24.04 guest.
- An SSH key the VM accepts, and the VM's host key pinned in a
  `known_hosts` file under a stable alias.
- A user on the VM in the `tbound-product` group, registered as a principal in
  the signed registration.

In VS Code, open a Git Bash terminal: **Terminal > New Terminal**, then pick
**Git Bash** from the dropdown next to `+`.

## 1. Connect to the VM

Keep the connection options in one place. This repository's demo kit keeps them
in `.local/product-demo/guest-ssh.env` (not tracked), for example:

```sh
GUEST_IP=172.21.171.248
GUEST_OPTS="-F /dev/null -i id_ed25519 -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=known_hosts -o GlobalKnownHostsFile=/dev/null -o HostKeyAlias=tbound-product-demo -o ConnectTimeout=10"
```

The key and `known_hosts` paths are relative, so run SSH from that directory. Run the `cd` from the repository root, or set `TBOUND_LOCAL` to the absolute `.local` path:

```sh
cd "${TBOUND_LOCAL:-.local}/product-demo"
. ./guest-ssh.env
ssh $GUEST_OPTS tboundadmin@$GUEST_IP
```

If SSH times out after the VM or Windows restarted, the Hyper-V Default Switch
probably gave the VM a new address. Look it up by the VM's MAC address in
PowerShell, then update `GUEST_IP`:

```powershell
Get-NetNeighbor -LinkLayerAddress 00-15-5D-0C-40-03
```

The host-key alias stays the same, so the pinned key still applies.

## 2. Chat in the terminal

On the VM:

```sh
tbound chat --project demo --mode review
```

Try three prompts:

| Prompt | Expected |
| --- | --- |
| `Read README.md and summarize it` | Allowed; the model answers |
| `Create a file TEST.md with one line: hi` | Held for review with a diff; type `/publish` or `/discard` |
| `Use bash to run ls` | Denied with `policy_default_deny` |

`/status` shows the session and its budget, and `/quit` detaches. The session
keeps running; reconnect with `tbound chat --session ID`.

## 3. Chat in the browser

From Git Bash on Windows, one command starts the gateway on the VM and
forwards its port to your workstation:

```sh
cd "${TBOUND_LOCAL:-.local}/product-demo" && . ./guest-ssh.env
ssh -t $GUEST_OPTS -L 127.0.0.1:8791:127.0.0.1:8791 tboundadmin@$GUEST_IP \
  '~/bin/tbound-webchat --live --socket /run/tbound-product/product.sock'
```

1. Ctrl+click the printed `http://127.0.0.1:8791/pair#…` link. It works once,
   within 10 minutes.
2. Under **New session**, enter project `demo` and provider profile
   `opencode-go-engineering`, then select **Create session**. Or click an
   existing session in the left panel. The chat stays empty until a session
   is selected.
3. Send the same three prompts. **Publish** and **Discard** appear under the
   diff.
4. Press Ctrl+C in the terminal to stop the gateway and the tunnel. Run the
   command again to pair another browser.

Each turn spends provider tokens from the installed key.

A session marked `blocked` has an earlier turn whose outcome is `unknown` and
refuses new prompts. The project is also blocked in new sessions. Use
[operator recovery](product-bash.md#8-recover-a-turn-killed-during-bash).

## 4. Run the product tests (optional)

The tests need Linux. A WSL distro with Go 1.27.1 can run them. Feed it the
committed tree instead of a Windows path, so the build runs on a Linux
filesystem. From the repository checkout, in Git Bash:

```sh
git archive HEAD | MSYS_NO_PATHCONV=1 wsl.exe -d tbound-dev -- bash -c '
  set -euo pipefail
  mkdir ~/tbound-product-test && tar -x -C ~/tbound-product-test
  cd ~/tbound-product-test/supervisor
  CGO_ENABLED=0 go test -count=1 ./cmd/tbound ./internal/productsetup
  cd && rm -r ~/tbound-product-test'
```

Replace `tbound-dev` with your distro. It takes about 10 minutes; expect two
`ok` lines. `MSYS_NO_PATHCONV=1` stops Git Bash from rewriting Linux paths.
WSL can run the tests but is not a governed product host.
