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

1. Copy the last values file: `cp freezes/F14.env freezes/F15.env`.
2. Set `FREEZE=F15`, `FREEZE_COMMIT=FROZEN_COMMIT_TBD`, `PREV_FREEZE=F14` and `PREV_COMMIT=<the F14 commit>`.
3. Fill every `PREV_*` value from `evidence/F14/logs`, plus a new `DR_ADMISSION_ID` and the current `GUEST_IP`.
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
`KIT_FREEZE=F14 DRY_RUN=1 bash guest/05-runtime-swap.sh` in Git Bash checks owner inputs and prints every guest command without guest contact.
`10-freeze.sh` step 3b refuses a frozen g1/route-probe template that carries the previous freeze's admission id, and a dr id that is spent.
05 requires `HF/BUNDLE_TAR_SIZE`, written by P2 rebuild after tar verification (F13's missing record was added after the same sha256 check).
A missing 27 completion record is reported as pending in dry-run mode and blocks a live run. P3/signing and guest pins read current records.

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
      `40-guest-evidence.sh route-direct-runtime`.
- [ ] `27` archived the previous freeze's G1/rp/dr state and exports; its dr request ended UNKNOWN BY DESIGN and
      the stale-evidence commit check in `40-guest-evidence.sh` passed for this freeze.
- [ ] Every log is under `<FREEZE>\logs`; no 0-byte output except `*.err` and the dr command/provider audits.
- [ ] The kit scripts pass `bash -n`; the Python files pass `python -I -m py_compile`; all files are UTF-8 without
      BOM and LF-only.

