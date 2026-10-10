# `tbound-doctor`

`tbound-doctor` is a standalone readiness check for a tbound install. It never
changes system state, never needs `sudo`, and never installs anything unless you
pass `--install-pi`.

Run it right after installing:

```sh
tbound-doctor            # human-readable report
tbound-doctor --json     # machine-readable report
```

## What it checks

| Check | Meaning | Dev impact |
|---|---|---|
| `os` | Host OS/arch | informational |
| `self` | Resolves its own executable | informational |
| `prefix` | tbound prefix exists and is private (`0700`) | WARN if missing/loose |
| `node` | A Node >= 22.19 was found (explicit, prefix, or PATH) | **FAIL** blocks dev |
| `pi` | Pi packages detected (prefix or `--pi`), version compared to the pin | WARN if missing |
| `cgroup-v2` | cgroup v2 controllers are delegated (Linux) | WARN — governed only |
| `podman`/`crun`/`cosign` | Containment/image tooling present (Linux) | WARN — governed only |
| `signed-profile` | `/etc/tbound/pi-host-profile.*` present (Linux) | WARN — governed only |

## Exit codes

- `0` — `dev_ready` is true (a usable Node was found).
- `1` — a dev-use requirement is unmet (e.g. Node missing or too old).
- `2` — internal error (bad flags, install failure).

`--json` prints `{dev_ready, governed_ready, checks[], fail_count, warn_count, …}`.

## Flags

| Flag | Meaning |
|---|---|
| `--prefix DIR` | tbound prefix to inspect |
| `--node PATH` | explicit Node binary |
| `--pi DIR` | explicit `node_modules` directory that contains Pi |
| `--install-pi` | install pinned Pi packages into the prefix (side-by-side, needs network) |
| `--json` | JSON output |
| `--version V` | version label to report |

## dev-ready vs governed-ready

`dev_ready` means you can run the non-claim-bearing dev/runtime surface.
`governed_ready` is **always false**: this tool never returns an admission result.
The Linux containment/profile checks are advisory prerequisites only
(`governed_prereqs_advisory`). `tbound serve --pi` stays refused until the runtime
composition and the authoritative verifier admit a signed profile. A missing
containment stack is reported but does not block dev work.
