Fresh F15 verified: `2a6492dda21e5af9eab8e92e81f56725b3f8e9c5` (`main` after PR #44), 2026-11-04/05.

F15 is an interim qualification of `main` on the guest (plan M2). It is not the H1 build (F-H1), and it does not establish H1 readiness or product release readiness.

Citation roots: `L=evidence/F15/logs`; `H=/home/tbound/tbound-h/f15` and `W=/home/tbound/tbound-w.gaeUWH` in the `tbound-dev` distro. Values: `infra/freeze/freezes/F15.env`. The owner-side steps ran natively in `tbound-dev` from a clean Linux checkout at the frozen commit; the guest steps ran from Git Bash from kit commit `ffdae7f1` (the F14 measurer pair `69feed6f` plus WSL helper calls kept off `/mnt`). Later kit commits on the same branch enforce that rule (no `linux_path`; owner wrappers refuse Git Bash; `TBOUND_WGA` required and fingerprinted against the kit); the guest swap dry run passes with them.

- **Gate:** PR #44's CI on the merged tree (linux-ci quick-checks and runtime, including the Go unit and race suites, extension-boundary, webchat). No separate local `gate.sh` run.
- **Measurer audit:** `launcher_linux.go`, `host.go`, `profile_tooling_linux.go` unchanged in 02f9fb7..2a6492d; `internal/sessionlaunch` changed (#39) but is outside `tbound-profile`'s import closure (`GOOS=linux go list -deps ./cmd/tbound-profile`). Pair added in `69feed6f` (PR #45).
- **Local freeze:** both P2 builds byte-identical. New digests against F14, as predicted (`.local/freeze-f15/F15-prep.md`): `TBOUND_DIGEST`, `TBP_DIGEST=sha256:94499a2e…`, `SRC_DIGEST`, `BUNDLE_DIGEST=sha256:5104ec0e…`, `IMGDIGEST=sha256:bcca636b…`, `CFG`; `BASH_DIGEST` and `NODE_DIGEST` unchanged (`L/console-20-p1.sh.txt`, `L/console-25-p2-rebuild.sh.txt`, `L/console-30-p3.sh.txt`).
- **EVQ capture:** passed first time on idle WSL, `exit_status` 0, capture commit and `archive_tree_digest == SRC_DIGEST` (`L/console-35-evq-capture.sh.txt`).
- **Signing:** `signed_manifest_digest == IMGDIGEST`; `LOAD_TAR_DIGEST=sha256:0dcc9284…` (`L/console-40-sign.sh.txt`).
- **Hyper-V checkpoint:** `pre-F15-2026-11-04`, taken with the VM off before any guest step.
- **Guest address:** `172.26.25.5` on 2026-11-05 after a host restart (it was `192.168.220.110` on 2026-11-04), found by MAC `00-15-5D-0C-40-02` and confirmed over the pinned host key (`tboundadmin@tbound-abi8-seed`).
- **Swap and upgrade:** the guest accepted the pinned F14 measurer; old tree kept as `runtime-src.old-af97c541-20261105T092912584772900Z` (`L/guest-05-runtime-swap.sh.txt`). New image loaded, `Id == CFG`, `Digest == IMGDIGEST` (`L/guest-50-i1-i2.sh.txt`). Sudo window closed, `sudo -n true` fails (`L/guest-00-session-sudo.sh-close.txt`).
- **G1 PASS:** completed, success, `read/edit/bash/read`, PUBLISHED/SUCCEEDED, `pub-9d01b694d80031f5e95c84c1b966f617`, settlement STOPPED, run `production-run-bce8f37b6fe7ae7712c72737` (`L/g1-response-extract.json`, a checked extract of `H/g1/response.txt` with its SHA-256).
- **rp PASS:** NO_DIFF, CHECKS_OK (`L/guest-31-reg-rp.sh.txt`). **dr PASS:** expected unknown, CHECKS_OK (`L/guest-32-reg-dr.sh.txt`).

**EVQ 21/22**, only `EVQ-NETWORK-SINK-GUEST` open, as at F14 (no corpus-mode netsink artifact) (`L/evq-report/evq.stdout`). **Routes Complete**, 4/4 covered (`L/evq-report/route-report.stdout`). Commands in `L/evq-report/commands.log`; the repo argument is the Linux checkout, not a `/mnt` path. The first adapter capture failed because the npm cache directory did not exist; it was filled with a copy of P2's `cache-master` and rerun (first-attempt files kept as `*.r1`). Empty stderr files were later removed from `H/f15/evq-report` because the kit's 0-byte check refuses them; the copies under `L/evq-report` keep them.

**Findings, not F15 failures:**

- The kit's guest helpers reached the kit through `/mnt/<drive>` (`glib.sh` `WGA`, and `wsl.exe` starting in the Windows cwd). F15 ran with `TBOUND_WGA` pointing at the Linux checkout and `--cd /` (`ffdae7f1`).
- `wsl.exe` without `-e` runs the command through a second shell that expands `$VAR` first; the kit already uses `-e`, ad-hoc commands must too.
- The chain's registrations make real model calls on the guest's engineering route, as at F14.

**Housekeeping left as is:** `runtime-src.old-af97c541-…` on the guest, the Hyper-V checkpoint, the `*.r1` capture files.

Not done: corpus-mode install, corpus witnesses, pilots, preregistration, H1.
