## By origin

- **scope**: #241 (9 files), #261 (5 files), #265 (7 files), #268 (2 files), #270 (1 files), #271 (1 files), #272 (21 files), #273 (7 files), #274 (11 files), #275 (2 files), #276 (5 files), #277 (18 files), #278 (8 files), #279 (2 files), #281 (1 files), #284 (18 files), #286 (34 files), #287 (14 files)
- **#238 comment sweep**: #238 (44 files)
- **#240 Go cleanup**: #240 (73 files)
- **other (evidence, docs, kit, sampling tooling)**: #252 (3 files), #254 (138 files), #256 (3 files), #257 (93 files), #258 (115 files), #259 (1 files), #260 (4 files), #263 (8 files), #264 (1 files), #266 (6 files), #267 (5 files), #269 (2 files), #280 (1 files), #282 (1 files), #285 (368 files)

## By package or path (PRs touching it)

- `.agents`: #238
- `.claude`: #238
- `.gitattributes`: #238
- `.github`: #265
- `.gitignore`: #238, #252
- `.opencode`: #238
- `AGENTS.md`: #252
- `CONTRIBUTING.md`: #265
- `Makefile`: #238
- `README.md`: #241
- `USAGE.md`: #241
- `cmd/tbound`: #238, #240, #277, #286
- `cmd/tbound (tests)`: #240, #252, #277, #286
- `cmd/tbound-panel`: #238, #268, #275
- `cmd/tbound-profile`: #276, #277, #278, #284
- `cmd/tbound-profile (tests)`: #272, #276, #284
- `cmd/tbound-run`: #273, #277, #278, #284
- `cmd/tbound-run (tests)`: #272, #273, #277, #278, #284
- `corpus/sampling`: #263, #267, #269, #280, #282, #285
- `docs`: #240, #241, #261
- `docs/design`: #260, #271, #281
- `docs/guides`: #240, #241, #261, #265, #268, #276
- `docs/runtime`: #260
- `e2e (tests)`: #240
- `evidence/F31`: #254
- `evidence/F32`: #257, #258, #260
- `evidence/T1`: #266
- `evidence/pilot-walk`: #285
- `extensions`: #238
- `infra/freeze`: #238, #256, #287
- `infra/guest`: #238, #259, #261, #264, #270, #274, #279, #286, #287
- `infra/repro`: #241
- `infra/workspace`: #238
- `install`: #241
- `internal/adaptivesource (tests)`: #240
- `internal/attacker`: #240
- `internal/attacker (tests)`: #240
- `internal/broker`: #240
- `internal/broker (tests)`: #240
- `internal/broker/openrouter`: #240, #286
- `internal/broker/openrouter (tests)`: #240, #286
- `internal/calibration`: #273
- `internal/calibration (tests)`: #273
- `internal/corpusadmit`: #240, #277
- `internal/corpusadmit (tests)`: #240
- `internal/evalcorpus`: #240, #277
- `internal/evalcorpus (tests)`: #277
- `internal/evalreport`: #272, #274, #276, #284
- `internal/evalreport (tests)`: #272, #274, #276, #284
- `internal/executor`: #240
- `internal/executor (tests)`: #240
- `internal/netoracle`: #240
- `internal/netoracle (tests)`: #240
- `internal/piruntime`: #238, #240, #277, #286
- `internal/piruntime (tests)`: #240, #286
- `internal/productdriver`: #238, #240
- `internal/productdriver (tests)`: #240
- `internal/productsession`: #240
- `internal/productsession (tests)`: #240
- `internal/productsetup (tests)`: #240
- `internal/projectbundle`: #238
- `internal/providerbridge (tests)`: #240
- `internal/researchprofile`: #274
- `internal/researchroute`: #274, #286
- `internal/researchroute (tests)`: #274, #286
- `internal/runner`: #238, #240, #272, #273, #277, #278, #284
- `internal/runner (tests)`: #240, #272, #273, #274, #277, #278, #284
- `internal/sessionlaunch`: #240
- `internal/sessionlaunch (tests)`: #240
- `internal/taskaccept`: #240
- `internal/taskaccept (tests)`: #240
- `internal/vrun`: #286
- `internal/vrun (tests)`: #286
- `internal/wsoracle`: #238
- `scripts`: #238, #240, #261, #265, #275, #287
- `supervisor`: #263
- `tests`: #265

## #240's product-only commit

```
df3596eb feat(product): journal the removal count of a review turn and state it in the review event

 docs/product-contracts.md                                    |  4 ++--
 supervisor/cmd/tbound/product_service_events_linux.go        |  6 +++++-
 supervisor/cmd/tbound/product_service_events_linux_test.go   | 11 +++++++++++
 supervisor/internal/productdriver/failed_publication_test.go |  5 +++--
 supervisor/internal/productdriver/removal_limit_test.go      |  3 +++
 supervisor/internal/productdriver/turn.go                    |  5 +++--
 supervisor/internal/productsession/catalog.go                |  2 +-
 supervisor/internal/productsession/catalog_test.go           |  3 ++-
 supervisor/internal/productsession/records.go                |  1 +
 supervisor/internal/productsession/state.go                  |  6 +++++-
```
