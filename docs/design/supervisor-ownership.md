# Supervisor ownership map

Which supervisor package belongs to which layer, and which imports are forbidden. Names omit `tbound/supervisor/`; a bare name means `internal/<name>`.

## Package map

| Layer | Packages |
|---|---|
| Shared runtime (26) | audit, broker, broker/correlation, broker/openrouter, broker/protocol, cellview, delta, drainproof, effectoracle, executor, faultfs, gate, ipc, piinstall, piruntime, privategit, providerbridge, publication, publicationevents, sandbox, sessionlaunch, sessionrepo, workflow, workflowrecovery, workspace, wsoracle |
| Product (7) | productdriver, productprotocol, productprovider, productsession, productsetup, projectbundle, releasetrust |
| Evaluation and validation (22) | adaptivesource, attacker, calibration, cgroupguard, corpusadmit, evalcorpus, evalreport, evalscore, guestevidence, netoracle, panelaudit, procoracle, researchprofile, researchroute, routematrix, runbooklint, runner, taskaccept, trialfix, usdledger, vrun, e2e (test-only, at the module root) |
| Commands (8) | cmd/tbound, cmd/tbound-cell-entrypoint, cmd/tbound-doctor, cmd/tbound-hint-eval, cmd/tbound-panel, cmd/tbound-profile, cmd/tbound-provider, cmd/tbound-run |

effectoracle and wsoracle count as shared, because production `publication` uses their fencing, manifest and ledger types.

## Entry points

- `cmd/tbound` composes the product and governed research routes; its product turn host implements `productdriver.Host`. It is a mixed composition root.
- `productdriver` owns turn order, review, receipts and restart disposition.
- `productsetup` owns deployment planning, fixed paths and command-host policy.
- `tbound-run`, `tbound-panel` and `tbound-hint-eval` are evaluation tools.
- `tbound-profile` combines host-profile tooling with qualification and evaluation.
- `tbound-doctor` checks installs.
- `tbound-cell-entrypoint` starts contained workers.
- `tbound-provider` probes one provider exchange.

## Import boundaries

The rules cover non-test `.go` files on all platforms and build tags. Test files are exempt: `providerbridge_test` deliberately imports `attacker` and `adaptivesource`.

1. Shared and product packages must not import evaluation packages.
2. Shared packages must not import product packages. The one exception is `piruntime`, which may import `productsetup` and `productsession` from files named `product_*_linux.go` only.
3. `productdriver` may directly import, within this module, only `audit`, `broker/protocol`, `productprotocol`, `productsession` and `publication`.
4. Every package belongs to exactly one layer; a new package fails the guard until it is assigned.

## Product verification inside piruntime

Only `piruntime/product_*_linux.go` may import `productsetup` or `productsession`. These files verify product host inputs, templates, launcher admission and command ownership using private launcher state, held cgroup descriptors and verified OCI state. Moving them out would need a separately reviewed security API redesign, so private capability construction and admission stay in `piruntime`.

## Guard

`supervisor/e2e/import_boundary_test.go` enforces this map. Run `cd supervisor && go test ./e2e -run '^TestImportBoundaries' -count=1`. To rebuild the package list, run `go list -deps ./...` in `supervisor/` with `GOOS=linux GOARCH=amd64 CGO_ENABLED=0`.
