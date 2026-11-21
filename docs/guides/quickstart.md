# Quickstart: build, check, and try TBound

This guide takes you from a fresh Linux machine to a passing build, passing
checks, and a first run of the Go-only fixture. It takes about 30 minutes,
most of it downloads. It does not set up a governed host. For that, read
[setup](setup.md) and [reproduction](reproduce.md) after this guide.

Two terms are used below. A **fixture** is a scripted, synthetic run that
exercises the code without a real model provider. A **governed host** is a
dedicated Linux machine that TBound uses to enforce its security boundary. See the
[glossary](../glossary.md) for the rest.

## 1. Pick a machine

| You have | Do this |
|---|---|
| Ubuntu 24.04 on amd64 (native or a VM) | Follow this guide. |
| Windows 10 or 11 | Install WSL 2 with Ubuntu 24.04, then follow this guide inside WSL. WSL is for building and testing only, not a governed host. Keep the checkout in the Linux home directory, not under `/mnt/c`. |
| macOS, or native Windows | Not supported. TBound is a Linux system. |

## 2. Install the tools

On Ubuntu 24.04:

```sh
sudo apt update
sudo apt install -y git make python3 build-essential coreutils tar
```

Install the two pinned toolchains:

- **Go 1.27.1**: download `go1.27.1.linux-amd64.tar.gz` from
  <https://go.dev/dl/>, then run
  `sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.27.1.linux-amd64.tar.gz`
  and add `/usr/local/go/bin` to your `PATH`.
- **Node.js 24.21.0** (minimum 22.19): install it with `nvm` or from
  <https://nodejs.org/dist/v24.21.0/>.

Pi, the coding agent TBound supervises, is needed only for the Pi fixture.
Install it as described in [setup](setup.md#install-pi).

## 3. Get the code

```sh
git clone https://github.com/faetschi/trust-boundary.git
cd trust-boundary
```

`main` is the public branch and the default checkout. Contributors branch from
`codex/tbound-prototype`; see [CONTRIBUTING](../../CONTRIBUTING.md). Full CI runs
only for pull requests into `main` and merges to `main`.

## 4. Check the machine

```sh
bash scripts/dev-preflight.sh
```

It reads your system and changes nothing. Fix every `missing` line it prints.
`warn` lines are optional for development. They matter only for a governed host.
On WSL, `warn` lines about cgroup v2, Landlock, Podman, crun and cosign are
expected.

Before steps 5 to 7, prepare the shell. The tests and the fixture refuse a
world-writable parent directory such as `/tmp`, and many tests fail under the
default umask `022`. Use a private umask and temporary directory, and keep this
shell for the rest of the guide:

```sh
umask 077
export TMPDIR="$(mktemp -d "$HOME/tbound-tmp.XXXXXX")"
```

## 5. Run the checks

```sh
make boundary                      # checks that core code does not import the optional web chat; no Node needed
make test                          # the Go unit suite (go test ./...); needs Node
python3 scripts/check-doc-links.py # checks links in the documentation
```

`make test` is a subset of what CI runs. CI also runs a race-detector suite and
Python regression tests. Some tests need root, cgroup v2, or a container runtime.
A developer machine may lack these, so a failure in those tests is not proof of
a code problem. Compare with CI before you report it.

## 6. Build the CLI and run the Go-only fixture

```sh
mkdir -m 700 -p "$HOME/.cache/tbound-build"
cd supervisor
go build -o "$HOME/.cache/tbound-build/tbound" ./cmd/tbound
"$HOME/.cache/tbound-build/tbound" --help
"$HOME/.cache/tbound-build/tbound" serve --pi --native-fixture
```

The fixture prints a JSON lifecycle receipt. It shows `claim_bearing=false`
and `containment=not-established`. That is intended: the fixture shows the flow
on synthetic inputs and does not test a security boundary.

The command reference is in [cli](../cli.md).

## 7. Optional: install a local build

```sh
cd ..
make build VERSION=dev
bash install/install.sh --version dev --local-dist dist \
  --prefix "$HOME/.local/share/tbound" --no-path
export PATH="$HOME/.local/share/tbound/bin:$PATH"
tbound-doctor
```

`tbound-doctor` reports prerequisites. It is advisory.

## Where to go next

| You want to | Read |
|---|---|
| Run a task, or the Pi development fixture | [Use TBound](../../USAGE.md), [setup](setup.md) |
| Understand a term such as PVR, freeze, or cell | [Glossary](../glossary.md) |
| Look up a command or flag | [CLI reference](../cli.md) |
| Contribute code or docs | [CONTRIBUTING.md](../../CONTRIBUTING.md) |
| Set up a governed host | [governed setup](governed-setup.md), [reproduction](reproduce.md) |
| Read the thesis evaluation records | [research index](../research/README.md) |
