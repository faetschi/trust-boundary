# F34 candidate: dev 386cf312 (tree 76a68d74), against F33 (4e8ac37d) and F32 (08f3c3ee)

## F33 -> F34 by PR (first-parent merges on dev after F33's dev base 9206c2e1)

- #289 `84d0ce19`: evidence: F33 signed tags record and their public key (D39)
    - evidence/F33/tags/README.md
    - evidence/F33/tags/allowed_signers
- #290 `4330fb78`: kit: F33 values file and the F23 measurer pair for F33
    - infra/freeze/README.md
    - infra/freeze/freezes/F33.env
    - infra/freeze/guest/05-runtime-swap.sh
- #283 `524d97df`: docs(design): cite fixes and drop the unverified resample claim (re-check 4)
    - docs/design/v-research-route.md
- #292 `8ac7da75`: fix: the V forwarder's client passes --bash-no-outputs
    - supervisor/cmd/tbound/pi_service_v_linux_test.go
    - supervisor/cmd/tbound/serve_linux.go
    - supervisor/cmd/tbound/trial_vforward_linux.go
    - supervisor/cmd/tbound/trial_vforward_linux_test.go
- #293 `bb9a226e`: kit: 10-freeze socket guard for the vrun relay test (+83 bytes, limit 24)
    - infra/freeze/10-freeze.sh
- #294 `e786b57e`: chore: fresh F34 admission ids for the G1 and route-probe templates
    - infra/guest/serve-pi-service.g1.json
    - infra/guest/serve-pi-service.route-probe.json
- #295 `386cf312`: test: the Finish test releases the upstream on every path (review note)
    - supervisor/cmd/tbound/pi_service_v_linux.go
    - supervisor/cmd/tbound/pi_service_v_linux_test.go
    - supervisor/internal/broker/openrouter/forward_test.go
    - supervisor/internal/researchroute/forwarder.go
    - supervisor/internal/researchroute/forwarder_linux_test.go

## F33 -> F34 diffstat (excluding corpus/third)

```
 docs/design/v-research-route.md                    |  13 +-
 evidence/F33/tags/README.md                        |  23 +++
 evidence/F33/tags/allowed_signers                  |   1 +
 infra/freeze/10-freeze.sh                          |   4 +-
 infra/freeze/README.md                             |   1 +
 infra/freeze/freezes/F33.env                       | 173 +++++++++++++++++++++
 infra/freeze/guest/05-runtime-swap.sh              |   2 +
 infra/guest/serve-pi-service.g1.json               |   2 +-
 infra/guest/serve-pi-service.route-probe.json      |   2 +-
 supervisor/cmd/tbound/pi_service_v_linux.go        |   6 +-
 supervisor/cmd/tbound/pi_service_v_linux_test.go   |  84 +++++++++-
 supervisor/cmd/tbound/serve_linux.go               | 117 ++++++++------
 supervisor/cmd/tbound/trial_vforward_linux.go      |   2 +-
 supervisor/cmd/tbound/trial_vforward_linux_test.go |   2 +-
 .../internal/broker/openrouter/forward_test.go     |  15 ++
 supervisor/internal/researchroute/forwarder.go     |  20 +--
 .../internal/researchroute/forwarder_linux_test.go |  58 ++++++-
 17 files changed, 450 insertions(+), 75 deletions(-)
```

## F32 -> F34

F32 -> F33 is in .local/f33-candidate/ (diff by origin v2, reviews, gates); F33 -> F34 is the list above.

```
 985 files changed, 102934 insertions(+), 1879 deletions(-)
```
