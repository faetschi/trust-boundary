# Landlock requirements for the TBound cell

This file states the intended Landlock configuration. It is a design requirement
and scaffold, **not** enforced or verified here.

## Requirements

- Landlock ABI >= 3 (filesystem access rights) — higher preferred for network
  and truncation rights (ABI >= 4/5).
- A process-wide ruleset applied before the workload executes.
- A default-deny posture with explicit read-only grants for the pinned runtime
  and a single writable cell root per command.

## Intended ruleset (illustrative)

| Path | Access |
|---|---|
| `/usr`, `/bin`, `/lib`, `/lib64` | read + execute only |
| `/usr/local/share/tbound` | read only |
| cell root (per command) | read + write |
| everything else | denied |

## Status

- Not yet enforced or verified on the declared evaluation host.
- The launcher must apply this ruleset process-wide and fail closed if Landlock
  is unavailable or reports an ABI below the required minimum.
- Do not treat the presence of this file as evidence of containment.
