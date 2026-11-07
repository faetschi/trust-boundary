# TBound — current state

Updated 2026-11-07. Three different things are easy to mix up here.

| Layer | State | Record |
| --- | --- | --- |
| Frozen research baseline | G1 passes on the real guest at F13 = `6162e3c` (2026-10-24). F14 = `02f9fb7` is the guest-verified thesis source freeze (2026-10-30). Both stay as recorded. | [F14 result](evidence/F14/F14-RESULT.md), [status](docs/evaluation/status.md) |
| Interim engineering qualification | F15 = `2a6492d`, F16 = `97738b6`, F17 = `8931c69` and F18 = `b26e5ef` are guest-verified engineering freezes of `main`. Each passed G1, rp and dr, and routes 4/4. EVQ was 21/22 at F15 and F16, and 22/22 with the guest net-sink artifact at F17 and F18. | [F15 result](evidence/F15/F15-RESULT.md), [F16 result](evidence/F16/F16-RESULT.md), [F17 result](evidence/F17/F17-RESULT.md), [F18 result](evidence/F18/F18-RESULT.md), [freeze kit](infra/freeze/README.md) |
| H1 evaluation | Not run. The build for it is F-H1, which does not exist yet. There are no H1 results. | [status](docs/evaluation/status.md) |

F15 to F18 are not the H1 build. They establish neither H1 readiness nor
product release readiness. `EVQ-NETWORK-SINK-GUEST` is open at F14, F15 and
F16. F17 and F18 passed it as interim rehearsals; the claim-bearing 22/22
belongs to F-H1.

The product lane is in progress with no qualified product host.
