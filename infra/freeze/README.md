# TBound freeze kit (one reusable kit, one values file per freeze)

This is the F12 kit generalised: the scripts are the same steps, but every freeze-specific name and value
is read from `freezes/$KIT_FREEZE.env`. `KIT_FREEZE` selects the file; without it, or with a missing file or
the `FROZEN_COMMIT_TBD` placeholder, every entry script refuses to run. Before creating directories or
opening logs, shell entry points use `freeze-guard.sh` (directly or through `lib.sh`/`guest/glib.sh`):
validate `KIT_FREEZE` against `^F[0-9]+$`, require its values file, require `FREEZE=KIT_FREEZE`, require
a 40-character lowercase hex `FREEZE_COMMIT`, then refuse freeze numbers <= 12 (including leading zeros).
Python entry points apply the same checks. F12 and older freezes are historical and read-only.

## Start a new chain

A new chain needs no earlier freeze. Its owner keys are its own, so its signatures and digests don't verify against the shared chain's.

1. Run `infra/workspace/fetch-inputs.sh "$TBOUND_WORK/inputs"`, `install-toolchains.sh` and `fill-caches.sh`.
2. Copy `freezes/first-freeze.env.example` to `freezes/F100.env`. Set `FREEZE_COMMIT` to HEAD. Keep the file uncommitted.
3. Run `KIT_FREEZE=F100 bash 05-init-chain.sh`, once. It creates `$OLDH` with the keys and the passphrase file. Back both up.
4. Run `KIT_FREEZE=F100 bash tools/freeze.sh`.

The guest scripts stop on `PREV_FREEZE=none`.

## Start a new freeze

1. Copy the last values file, for example after F18: `cp freezes/F18.env freezes/F19.env`.
2. Set `FREEZE=F19`, `FREEZE_COMMIT=FROZEN_COMMIT_TBD`, `PREV_FREEZE=F18` and `PREV_COMMIT=<the F18 commit>`.
3. Fill every `PREV_*` value from `evidence/F18/logs`, plus a new `DR_ADMISSION_ID` and the current `GUEST_IP`.
   Commit fresh admission IDs in `infra/guest/serve-pi-service.g1.json` and `serve-pi-service.route-probe.json`; `10-freeze.sh` refuses reused ones.
4. Set `FREEZE_COMMIT` to the owner-selected 40-hex commit.

`$FREEZE`/`$PREV_FREEZE` naming is derived from the values file: the WSL freeze directory is
`$H/<freeze-lowercase>` (e.g. `$H/f13`), the EVQ capture directory is `$H/c<freeze-lowercase>` (e.g. `cf13`),
and the previous-freeze comparison base is `$H/<prev-lowercase>` (e.g. `$H/f12`, read-only). The archive step
names its directory `archive-<prev-lowercase>-<UTC>`.

Logs and outputs go to `$TBOUND_WORK/receipts/<FREEZE>/logs` (created on the first run), and `tools/pull-receipts.sh`
copies them into `evidence/` for committing. Machine values live in `.local/freeze-host.env` (see `host.env.example`). The kit
directory contains only code and values. Nothing here prints a secret. Only `05-init-chain.sh` writes a passphrase file and reads a key file, when it creates a chain's keys.

P2: `25-p2-rebuild.sh` refuses a second attempt, verifies the frozen LF sources,
Node/npm/Pi pins, builds twice with `npm --offline` in private network namespaces and cache copies under `$W/p2-rebuild`,
compares listings/content/tars, measures read-only binds with the frozen P1 measurer, and verifies extraction.
It writes the same five P2 records as reuse plus `BUNDLE_TAR_SIZE` and `P2-MODE=rebuild`; logs use `25-p2-rebuild`.
`guest/05-runtime-swap.sh` installs a changed rebuilt bundle using P11, after 27 archive and 00 open, before 10-upgrade.
It verifies the owner tar sha256/size, space, mount/unit pins and old/new service-context digests; failures roll back automatically.
Keep the printed `.old-<previous-digest-prefix>-<UTC>` tree until the signed profile is installed and verified. Reuse/already-installed bundles only verify.
F13 accepts the pinned F12 measurer: `VerifyLinuxPiRuntimeBundle` and its mount/device helpers are unchanged in eed7722..6162e3c; 10 rechecks with F13.
F14 accepts the pinned F13 measurer (05 also runs when the bundle is unchanged): the same functions, `internal/sessionlaunch` and `profile_tooling_linux.go` are unchanged in 6162e3c..<F14>; 10 rechecks with F14.
F15 accepts the pinned F14 measurer: `launcher_linux.go`, `host.go` and `profile_tooling_linux.go` are unchanged in 02f9fb7..2a6492d, and `internal/sessionlaunch`, which changed, is not in `tbound-profile`'s import closure (`GOOS=linux go list -deps ./cmd/tbound-profile`); the pair lives in a kit commit on top of F15, since a tree cannot name its own commit; 10 rechecks with F15.
F16 accepts the pinned F15 measurer: `launcher_linux.go`, `host.go` and `profile_tooling_linux.go` are unchanged in 2a6492d..97738b6 (`git diff --quiet`); the pair lives in a kit commit on top of F16; 10 rechecks with F16.
F17 accepts the pinned F16 measurer: `launcher_linux.go`, `host.go` and `profile_tooling_linux.go` are unchanged in 97738b6..8931c69 (`git diff --quiet`); the pair lives in a kit commit on top of F17; 10 rechecks with F17.
F18 accepts the pinned F17 measurer: 8931c69..b26e5ef has deletion-only non-test changes in `launcher_linux.go`, `profile_tooling_linux.go` and `construct_evidence_linux.go`, unreachable from `runtime-bundle-digest` (F17 caller grep and import-closure audit); the command, `VerifyLinuxPiRuntimeBundle`, the mount/device helpers and the `host.go` limits are byte-identical; the pair lives in a kit commit on top of F18; 10 rechecks with F18.
F20 accepts the pinned F18 measurer (F19 was cut but not qualified; the guest was restored to F18): `launcher_linux.go`, `host.go` and `profile_tooling_linux.go` are unchanged in b26e5ef..2641c6b (`git diff --quiet`); the pair lives in a kit commit on top of F20; 10 rechecks with F20.
F21 accepts the pinned F20 measurer: `launcher_linux.go`, `host.go` and `profile_tooling_linux.go` are unchanged in 2641c6b..bbc6ec8 (`git diff --quiet`); the pair lives in a kit commit on top of F21; 10 rechecks with F21.
F23 accepts the pinned F20 measurer (F22's route probe failed on a provider stream deviation; the guest was restored to F20): `launcher_linux.go`, `host.go` and `profile_tooling_linux.go` are unchanged in 2641c6b..13ed119 (`git diff --quiet`); the pair lives in a kit commit on top of F23; 10 rechecks with F23.
F24 accepts the pinned F23 measurer: `launcher_linux.go`, `host.go`, `profile_tooling_linux.go` and `cmd/tbound-profile/main_linux.go` are unchanged in 13ed119..de2a3cf (`git diff --quiet`), and every helper `VerifyLinuxPiRuntimeBundle` calls is in `launcher_linux.go`; the changed `containment_launch_linux.go` and `d09_command_runner_linux.go` are not on the digest path; the pair lives in a kit commit on top of dev `c1d226e1` (tree identical to F24); 10 rechecks with F24.
F25 accepts the pinned F23 measurer (F24 was not qualified: G1 UNKNOWN on an inactive provider subscription; the guest was restored to F23): the same four files are unchanged in 13ed119..2fa3e00 (`git diff --quiet`); F25 is F24's code plus the F24 kit and fresh admission ids; the pair lives in a kit commit on top of dev `e5f8b7c9` (tree identical to F25); 10 rechecks with F25.
F26 accepts the pinned F23 measurer (F25 was not qualified: corpus mode refused the pre-D50 pilot budgets; the guest was restored to F23): the same four files are unchanged in 13ed119..2780072 (`git diff --quiet`); F26 adds the F25 kit, the D50 pilot budgets (#138, corpus drafts and tests only) and fresh admission ids; the pair lives in a kit commit on top of dev `c90f25e0` (tree identical to F26); 10 rechecks with F26.
F27 accepts the pinned F23 measurer (F26 was verified but the guest was restored to F23 to clear its wedged rehearsal): the same four files are unchanged in 13ed119..db07853 (`git diff --quiet`); no `internal/piruntime` file changed since F26, and the changed `cmd/tbound-profile` files (corpus-digest, construct, preregistration) are off the runtime-bundle-digest path; the pair lives in a kit commit on top of dev `8f375ba9` (tree identical to F27); 10 rechecks with F27.
F28 accepts the pinned F23 measurer (F27 was not qualified: the rp model declined commands 2-4; the guest was restored to F23): the same four files are unchanged in 13ed119..40e651b (`git diff --quiet`); F28 adds the F27 kit, fresh admission ids and the route-probe prompt (no Go change); the pair lives in a kit commit on top of dev `86a45bf8` (tree identical to F28); 10 rechecks with F28.
F29 accepts the pinned F23 measurer (F28 was verified; the guest was restored to F23 for the F29 screening build): the same four files are unchanged in 13ed119..28f407b (`git diff --quiet`); F29 adds the F28 kit and evidence, #165, #168, #169, B6 (#177), fresh admission ids (#172), the piruntime memo clock (#181) and the cell entrypoint fix (#183); the changed `internal/piruntime` memo files and `construct_evidence_linux.go` are off the runtime-bundle-digest path; the pair lives in a kit commit on top of dev `04b9bea5` (tree identical to F29); 10 rechecks with F29.
F30 accepts the pinned F23 measurer (F29 was verified; the guest was restored to F23 for the F30 screening build): the same four files are unchanged in 13ed119..5626e34 (`git diff --quiet`); F30 adds the F29 evidence, #187 (effectoracle/v4), #191 (kit), #192 (typed end at the host-call budget), the install stack #179, #178 and #174, and fresh admission ids (#193); no `internal/piruntime` or `cmd/tbound-profile` file changed since F29; the pair lives in a kit commit on top of dev `2a204b7f` (tree identical to F30); 10 rechecks with F30.
`KIT_FREEZE=F14 DRY_RUN=1 bash guest/05-runtime-swap.sh` in Git Bash checks owner inputs and prints every guest command without guest contact.
`10-freeze.sh` step 3b refuses a frozen g1/route-probe template that carries the previous freeze's admission id, and a dr id that is spent.
05 requires `HF/BUNDLE_TAR_SIZE`, written by P2 rebuild after tar verification (F13's missing record was added after the same sha256 check).
A missing 27 completion record is reported as pending in dry-run mode and blocks a live run. P3/signing and guest pins read current records.

## Screening round with a carrier

A screening round reruns `guest/33-corpus-pilot.sh` on a qualified freeze with a lane C carrier (`corpus/sampling/carriers/<round>/`) in place of the T5 draft.
Set all three of `CARRIER_DIR` (the carrier directory in a Linux checkout at the delivering commit), `CARRIER_COMMIT` and `CARRIER_MANIFEST_DIGEST`
(`sha256:` plus lane C's `sha256(manifest.draft.json)`), or none of them. With none, 33 behaves as before. Phase 0 (`guest/carrier-check.sh`) verifies the
checkout and an owner-side copy in `$HF/pilot/carrier` before any guest write. `$HF/pilot` holds one round, and 33 refuses a `$HF/pilot` of another round
or of the T5 run until it is renamed aside. `guest/carrier-check.sh` is one of the `TBOUND_WGA` helpers, so refresh that Linux checkout to this kit commit. `tools/test-carrier.sh [<W> <HF>]` (WSL) tests phase 0 and, with the freeze's tools, the phase-2 dry run.
The operator steps and the run-config pins are in `infra/guest/PROVISIONING-SIGNING.md`, "Screening round with a carrier".

## Per-freeze checklist

- [ ] Values file copied and `FREEZE`/`PREV_FREEZE`/`PREV_COMMIT` set; every `PREV_*` value filled from `evidence/F14/logs`.
- [ ] `FREEZE_COMMIT` is the owner-selected 40-hex commit, not the placeholder.
- [ ] `GUEST_IP` verified with `Get-NetNeighbor -LinkLayerAddress 00-15-5D-0C-40-02`; `DR_ADMISSION_ID` is new.
- [ ] Owner-side order with `KIT_FREEZE=<FREEZE>`: `10-freeze.sh <commit>`, `20-p1.sh`, `25-p2-rebuild.sh`,
      `30-p3.sh`, `session-env.sh`, `35-evq-capture.sh`, `40-sign.sh`.
- [ ] No `/mnt`: run the owner-side order natively in WSL from a Linux checkout at the freeze commit (`tools/freeze.sh` and `tools/all.sh` refuse Git Bash). Run the guest order from Git Bash with `TBOUND_WGA` set to `infra/freeze/guest` of a second Linux checkout at the kit commit; `guest/glib.sh` refuses a missing, relative or `/mnt` value and any helper, lib or values-file difference from the kit.
- [ ] Guest order: `27-archive-state.sh archive` (archives the previous freeze), `00-session-sudo.sh open`,
      `05-runtime-swap.sh`, `10-upgrade.sh`, `20-r1.sh`, `50-i1-i2.sh` (`RECLAIM_PODMAN_PAUSE=1`), `00-session-sudo.sh close`,
      `29-g1-recover.sh status`, `30-reg-g1.sh`, `40-guest-evidence.sh capability procoracle landlock-connect
      provider-exchange`, `31-reg-rp.sh`, `40-guest-evidence.sh route-bash-view`, `32-reg-dr.sh`,
      `40-guest-evidence.sh route-direct-runtime`. Corpus mode, after that (owner approval; `TBA_PW` for the artifact install;
      `TBOUND_TRIAL_KEY=<trial key path>` adds the netsink step): `33-corpus-pilot.sh` (abandon the dr request, D1 manifest, 76 T5 artifacts,
      the RG cycle for `pilot`, optional `trial reset` and `40-guest-evidence.sh netsink`); `tools/guest-chain.sh` runs it with `CORPUS_PILOT=1`.
- [ ] `33-corpus-pilot.sh` ran to "DONE" (`$HF/pilot/CYCLE_OK`; with the trial key also `$HF/evq/netsink.json`), the session sudo is closed again.
- [ ] `27` archived the previous freeze's G1/rp/dr state and exports; it archives the pilot items too, and `33` resets a pilot install left by an earlier freeze (`pilot-reset` phase); its dr request ended UNKNOWN BY DESIGN and
      the stale-evidence commit check in `40-guest-evidence.sh` passed for this freeze.
- [ ] Before every `tbound-run run` block (and after a host restart or sleep): `w32tm /resync /force` on the Windows host and `chronyc makestep` on the guest
      (`infra/guest/PROVISIONING-SIGNING.md`, "Clocks before every `run` block"). The runner refuses an adversarial step when the guest clock is more than 250 ms off the host clock.
- [ ] Every log is under `<FREEZE>\logs`; no 0-byte output except `*.err`, `evq-report/*.stderr` and the dr command/provider audits.
- [ ] The kit scripts pass `bash -n`; the Python files pass `python -I -m py_compile`; all files are UTF-8 without
      BOM and LF-only.

