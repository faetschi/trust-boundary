# F35 candidate rev 2: dev 860c2379 (tree 3979713e), against F34 (8627adeb, tree 76a68d74)

Rev 2 = rev 1 (dev b727cbd3, tree fcf1cee3; full list in .local/f35-candidate/f35-diff-by-origin.md) + #310 only.

## Added in rev 2

- #310 `860c2379`: fix(researchroute): forward a replayed reasoning_details as reasoning (F35 F1)
    - supervisor/internal/broker/openrouter/forward.go
    - supervisor/internal/broker/openrouter/forward_test.go
    - supervisor/internal/broker/openrouter/testdata/f35-probe-v-request-3.json
    - supervisor/internal/researchroute/forwarder_linux_test.go

## F34 -> F35 rev 2, runtime (supervisor/) only: #304, #305, #310

```
 .../cmd/tbound/pi_service_reconcile_linux_test.go  | 18 ++++
 .../pi_service_reconcile_production_linux.go       |  5 ++
 supervisor/cmd/tbound/pi_service_v_linux_test.go   | 18 +++-
 .../tbound/production_corpus_admission_linux.go    |  2 -
 supervisor/cmd/tbound/research_route_linux_test.go | 32 +++++++
 supervisor/internal/broker/openrouter/forward.go   | 68 ++++++++++++++-
 .../internal/broker/openrouter/forward_test.go     | 98 ++++++++++++++++++++++
 .../openrouter/testdata/f35-probe-v-request-3.json |  1 +
 .../internal/piruntime/managed_live_root_linux.go  |  2 +-
 .../internal/researchroute/forwarder_linux_test.go | 39 +++++++++
 10 files changed, 278 insertions(+), 5 deletions(-)
```

## F34 -> F35 rev 2, everything outside evidence/ and docs/thesis/

```
 README.md                                          |   2 +-
 assets/tbound-mark.svg                             |   9 ++
 assets/trust-boundary-banner.png                   | Bin 206276 -> 40991 bytes
 assets/trust-boundary-banner.svg                   | 101 ++++++++++++
 docs/README.md                                     |   2 +-
 extensions/webchat/docs/web-chat.md                |   2 +-
 extensions/webchat/internal/webchat/favicon.ico    | Bin 16580 -> 45596 bytes
 infra/freeze/README.md                             |   1 +
 infra/freeze/freezes/F34.env                       | 180 +++++++++++++++++++++
 infra/freeze/guest/05-runtime-swap.sh              |   2 +
 infra/freeze/guest/34-v-stub-probe.sh              | 168 ++++++++++++++++---
 infra/freeze/guest/vs-check.py                     |  46 +++++-
 infra/freeze/guest/vs-recipe.py                    | 179 ++++++++++++++++++++
 infra/freeze/tools/test-vstub.sh                   |   2 +-
 infra/guest/PROVISIONING-SIGNING.md                |  14 +-
 infra/guest/serve-pi-service.g1.json               |   2 +-
 infra/guest/serve-pi-service.route-probe.json      |   2 +-
 infra/guest/v-stub/stub-upstream.py                |  47 ++++--
 .../cmd/tbound/pi_service_reconcile_linux_test.go  |  18 +++
 .../pi_service_reconcile_production_linux.go       |   5 +
 supervisor/cmd/tbound/pi_service_v_linux_test.go   |  18 ++-
 .../tbound/production_corpus_admission_linux.go    |   2 -
 supervisor/cmd/tbound/research_route_linux_test.go |  32 ++++
 supervisor/internal/broker/openrouter/forward.go   |  68 +++++++-
 .../internal/broker/openrouter/forward_test.go     |  98 +++++++++++
 .../openrouter/testdata/f35-probe-v-request-3.json |   1 +
 .../internal/piruntime/managed_live_root_linux.go  |   2 +-
 .../internal/researchroute/forwarder_linux_test.go |  39 +++++
 28 files changed, 998 insertions(+), 44 deletions(-)
```

evidence/: 447 file(s) added or changed.

## Merge commit for main (local, not pushed)

47ef959d: tree 3979713e (= g35b's gated tree = dev 860c2379's tree), parents main 5e2f6cc1 and dev 860c2379, message as bf6ab60f. It drops corpus/third (334 files), which returns in the corpus-only commit with tree corpus/third = 213b85f5 (checked: 5e2f6cc1:corpus/third = 213b85f5).
