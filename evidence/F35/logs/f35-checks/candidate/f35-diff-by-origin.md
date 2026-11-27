# F35 candidate: dev b727cbd3 (tree fcf1cee3), against F34 (8627adeb, tree 76a68d74) and F32 (08f3c3ee)

## F34 -> F35 by PR (first-parent merges on dev after F34's dev base 386cf312)

- #296 `4e23b0df`: feat(brand): new ink-and-red banner, mark and webchat favicon
    - README.md
    - assets/tbound-mark.svg
    - assets/trust-boundary-banner.png
    - assets/trust-boundary-banner.svg
    - extensions/webchat/docs/web-chat.md
    - extensions/webchat/internal/webchat/favicon.ico
- #298 `f19baf72`: evidence: F34 signed tags record (D39, D62)
    - evidence/: 2 file(s)
- #299 `8be667e6`: kit: F34 values file and the F23 measurer pair for F34
    - infra/freeze/README.md
    - infra/freeze/freezes/F34.env
    - infra/freeze/guest/05-runtime-swap.sh
- #301 `b5fe5acb`: evidence: F34 qualified (final freeze, D62) and the F33 record
    - evidence/: 449 file(s)
- #291 `1e9872f2`: docs: sync thesis overview copy from MSE_MA_Thesis ff7607c
    - docs/README.md
    - docs/thesis/tbound-overview.html
- #302 `7c7215c8`: docs: sync thesis overview (governator objection, evidence gap)
    - docs/thesis/tbound-overview.html
- #303 `37821735`: docs: sync thesis overview (scope cards, practice run)
    - docs/thesis/tbound-overview.html
- #304 `9b2f9c98`: fix: re-check an abandoned prior on the route its record binds (F35)
    - supervisor/cmd/tbound/pi_service_reconcile_linux_test.go
    - supervisor/cmd/tbound/pi_service_reconcile_production_linux.go
- #305 `5321941b`: fix: the v-stub route admits governed units (F35, D63 item 4)
    - supervisor/cmd/tbound/pi_service_v_linux_test.go
    - supervisor/cmd/tbound/production_corpus_admission_linux.go
    - supervisor/cmd/tbound/research_route_linux_test.go
    - supervisor/internal/piruntime/managed_live_root_linux.go
- #306 `8eb8ae86`: kit: research-route stub check in the V probe (RESEARCH_E2E=1, D63 item 4)
    - infra/freeze/guest/34-v-stub-probe.sh
    - infra/freeze/guest/vs-check.py
    - infra/freeze/guest/vs-recipe.py
    - infra/freeze/tools/test-vstub.sh
    - infra/guest/PROVISIONING-SIGNING.md
    - infra/guest/v-stub/stub-upstream.py
- #307 `488dc91b`: evidence: drop the V-probe signature binaries that check-self-contained refuses
    - evidence/: 6 file(s)
- #308 `b727cbd3`: chore: fresh F35 admission ids for the G1 and route-probe templates
    - infra/guest/serve-pi-service.g1.json
    - infra/guest/serve-pi-service.route-probe.json

## F34 -> F35 diffstat (excluding evidence/; corpus/third is not on dev)

```
 README.md                                                      |   2 +-
 assets/tbound-mark.svg                                         |   9 +++
 assets/trust-boundary-banner.png                               | Bin 206276 -> 40991 bytes
 assets/trust-boundary-banner.svg                               | 101 +++++++++++++++++++++++++++
 docs/README.md                                                 |   2 +-
 docs/thesis/tbound-overview.html                               | 151 +++++++++++++++++++++++++++++++++++------
 extensions/webchat/docs/web-chat.md                            |   2 +-
 extensions/webchat/internal/webchat/favicon.ico                | Bin 16580 -> 45596 bytes
 infra/freeze/README.md                                         |   1 +
 infra/freeze/freezes/F34.env                                   | 180 +++++++++++++++++++++++++++++++++++++++++++++++++
 infra/freeze/guest/05-runtime-swap.sh                          |   2 +
 infra/freeze/guest/34-v-stub-probe.sh                          | 168 +++++++++++++++++++++++++++++++++++++++------
 infra/freeze/guest/vs-check.py                                 |  46 ++++++++++++-
 infra/freeze/guest/vs-recipe.py                                | 179 ++++++++++++++++++++++++++++++++++++++++++++++++
 infra/freeze/tools/test-vstub.sh                               |   2 +-
 infra/guest/PROVISIONING-SIGNING.md                            |  14 +++-
 infra/guest/serve-pi-service.g1.json                           |   2 +-
 infra/guest/serve-pi-service.route-probe.json                  |   2 +-
 infra/guest/v-stub/stub-upstream.py                            |  47 ++++++++++---
 supervisor/cmd/tbound/pi_service_reconcile_linux_test.go       |  18 +++++
 supervisor/cmd/tbound/pi_service_reconcile_production_linux.go |   5 ++
 supervisor/cmd/tbound/pi_service_v_linux_test.go               |  18 ++++-
 supervisor/cmd/tbound/production_corpus_admission_linux.go     |   2 -
 supervisor/cmd/tbound/research_route_linux_test.go             |  32 +++++++++
 supervisor/internal/piruntime/managed_live_root_linux.go       |   2 +-
 25 files changed, 925 insertions(+), 62 deletions(-)
```

evidence/: 447 file(s) added or changed (F33 and F34 records, #301, #298, #307).

## F32 -> F35

F32 -> F34 is in evidence/F34/logs/f34-checks/candidate/f34-diff-by-origin.md (and .local/f33-candidate/); F34 -> F35 is the list above.

```
 1443 files changed, 151465 insertions(+), 1901 deletions(-)
```
