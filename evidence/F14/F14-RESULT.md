Fresh F14 verified: `02f9fb71a26aadf4eebeb0ff217fe97c374ffff8`, 2026-10-30.

This validates the F14 thesis source on the guest. It is not a qualification of any later integrated version, and it does not establish H1 readiness or product release readiness.

Citation roots: `L=F:/TBoundAssets/recovery-20261015/F14/logs`; `H=/home/jeli2k/tbound-h.zfosIW/f14`; `W=/home/jeli2k/tbound-w.RciFcO/src`. Gate and repin receipts: `F:/TBoundAssets/t5t7-20261029/RESULTS.md`.

- **Gate:** `t57i` on the frozen commit: TEST_RC=0, 5367 pass, 40 skip (same named set as `pc29h`), 0 fail. An earlier run `t57h` failed on two loopback timeouts that passed alone (`gate-02f9fb7-r1/`).
- **Local freeze:** both builds identical for P1, P2 and P3. New digests against F13 (`L/console-20-p1.sh.txt`, `L/console-25-p2-rebuild.txt`, `L/console-30-p3.sh.txt`). `BUNDLE_DIGEST=sha256:af97c541f696a3d3da8d0d0799812a1f46e57f3367f0e5dbcc18292b66ac0b25`, because `adapter/src/ipc-transport.ts` changed for T7.
- **EVQ capture:** the first attempt failed: `cmd/tbound` hit Go's default 600 s test timeout while another job loaded WSL (`H/../cf14.failed-20261029T221236Z`, `L/*.r1-20261029T221236Z`). The rerun on idle WSL passed with `exit_status` 0; `cmd/tbound` took 470 s.
- **Signing:** `signed_manifest_digest == IMGDIGEST = sha256:8352c9bdd77e1719f727326884a40299b381b5eb772fe899e907dfefcdf434de`; a throwaway key fails to verify; `LOAD_TAR_DIGEST=sha256:cdfdf90c656f7a52c979b176c15ff790feb3bb16ffa7e3d664d48f1a35c701a6` (`L/console-40-sign.txt`).
- **Guest address:** measured `172.24.140.254/20` through the VM adapter MAC `00-15-5D-0C-40-02` and confirmed over the pinned host key. Passed as `GUEST_IP`; the pinned `F14.env` still carries the stale value.
- **Swap and upgrade:** real swap from `6a07a8a9…` (F13) with the old tree kept as `runtime-src.old-6a07a8a9-20261030T051935594392700Z` (`L/guest-05-runtime-swap.sh.txt`). Step-7 measures and F14 pins passed (`L/guest-10-upgrade.sh.txt:134,180`). New image loaded, `Id == CFG`, `Digest == IMGDIGEST` (`L/guest-50-i1-i2.sh.txt:74-75`). Sudo window closed, `sudo -n true` fails (`L/guest-00-session-sudo.sh-close.txt`).
- **G1 PASS:** completed, successful `read/edit/bash/read`, g0→g2, STOPPED, SUCCEEDED/PUBLISHED, `pub-86d21af989ddc4ac477cd9ff033c3ae2`, run `production-run-2520600968feccc19764de2d` (`H/g1/response.txt:1`).
- **rp PASS:** completed, NO_DIFF, four bash calls, four settled commands, CHECKS_OK (`L/guest-31-reg-rp.sh.txt:292,295,301,310`). **dr PASS:** expected unknown, one launch proof, CHECKS_OK (`L/guest-32-reg-dr.sh.txt:403,417`).

Artifacts: all under `H/evq`; 2026-10-30 CET local-export times, **not attested guest creation times**.

| Artifact | Envelope commit | Created |
|---|---|---|
| capability.json | 02f9fb71a26aadf4eebeb0ff217fe97c374ffff8 | 06:23:39 |
| procoracle.json | 02f9fb71a26aadf4eebeb0ff217fe97c374ffff8 | 06:23:48 |
| landlock-connect.json | 02f9fb71a26aadf4eebeb0ff217fe97c374ffff8 | 06:23:54 |
| provider.json | 02f9fb71a26aadf4eebeb0ff217fe97c374ffff8 | 06:24:00 |
| route-bash-view.json | 02f9fb71a26aadf4eebeb0ff217fe97c374ffff8 | 06:26:16 |
| route-direct-runtime.json | 02f9fb71a26aadf4eebeb0ff217fe97c374ffff8 | 06:27:54 |

**EVQ 21/22**, only `EVQ-NETWORK-SINK-GUEST` open: no corpus-mode netsink artifact supplied (`L/evq-report/evq.stdout`). **Routes Complete**, 4/4 covered (`L/evq-report/route-report.stdout`). All rc 0; exact commands in `L/evq-report/commands.log`. Fresh adapter capture at the frozen commit, node v24.15.0, exit 0.

**Findings, not F14 failures:**

- `tbound-op verify-retained production-run-2520600968feccc19764de2d` refused: `retained D09 command event 1: is not bound to exactly one pinned session` (`L/verify-retained-g1.txt`). The checklist classes a refusal of this optional control as a finding about the verb. The verb has now run once on a production-closed Store and refused; do not rely on it until that is understood.
- The chain's registrations make real model calls on the guest's configured route (`provider_mode: model`, 5 provider turns for G1), as at F13.
- `cmd/tbound` needs about 470 s idle against the capture's 600 s default limit, so the capture needs an idle WSL.
- The T5 draft is `W/corpus/pilot/t5-2026-10-29/manifest.draft.json`; `corpus/pilot/manifest.draft.json` is the retained pre-T5 snapshot. D1 must base `corpus/pilot/manifest.json` on the T5 draft.

**Housekeeping left as is:** `runtime-src.old-6a07a8a9-…` on the guest, the failed capture directory, and the `.pre-f14` kit backups.

Not done: D1 manifest pins, corpus-mode install, corpus witnesses, pilots, preregistration, H1.
