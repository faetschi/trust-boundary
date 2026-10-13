# `tbound-doctor`

`tbound-doctor` checks local prerequisites. It does not install Pi, change
system state, or make a runtime admission decision.

```sh
tbound-doctor
tbound-doctor --pi /absolute/path/to/pi
tbound-doctor --json
```

Without `--pi`, it resolves `pi` through `PATH`. It checks that the
executable reports the pinned version 0.87.1 and prints the path and SHA-256
fingerprint. Doctor does not verify that the executable or future TBound
extension is signed or admitted by a production profile.

On Linux it also reports cgroup-v2, Podman, crun, Cosign, and signed-profile
prerequisites. Those checks are advisory. A present binary or profile file does
not prove correct configuration or containment.

The JSON report includes `dev_ready`, `governed_ready`,
`governed_prereqs_advisory`, and individual checks. `governed_ready` is
always false: only the runtime admission path can decide whether a governed
session may start. See [current status](status.md).
