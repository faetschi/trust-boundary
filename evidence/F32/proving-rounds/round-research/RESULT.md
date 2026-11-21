# Proving round 2 on F32 (research route, install only): result

Freeze F32 = `main` `08f3c3ee` (`logs/RA-0-status.txt:1`). This round is the install of the research route on the guest, by the runbook section "Research route after Z1" (`infra/guest/PROVISIONING-SIGNING.md:1188`, steps RA-0 to RA-11), approved by the owner (relayed by the coordinator; no repo file). It is not a measured round: no unit ran, no provider request was made, nothing was spent. Times are UTC, 2026-11-21. Paths below are relative to this folder.

**Result: RA-0 to RA-10 completed. At RA-11 the service refused to start, by design: "a pilot corpus on the research route is the research pilot and must pin its D48 pilot draw (pins.draw_digest)" (`logs/RA-11-start.txt:4`; `supervisor/cmd/tbound/production_corpus_admission_linux.go:216-219` at `08f3c3ee`, message at `:218`). The corpus that was installed is the engineering pilot corpus (`logs/RA-0-status.txt:6`); that it pins no draw is **inferred** from the refusal. So the first live OpenRouter request is the research pilot itself, an owner step. No request was reserved and nothing was spent (relayed by the coordinator; no repo file; the log shows the service failing at start, `logs/RA-11-start.txt:1-4`). The session sudo was closed and the next step 33 rolled the config back to engineering.**

## Steps

| step | what | source |
|---|---|---|
| RA-0 | status before: service and socket active, installed config matches the engineering pilot state; service stopped (`inactive inactive`) | `logs/RA-0-status.txt:4-6` (12:25:54), `logs/RA-0-stop.txt:1-2` |
| RA-1 | preconditions: only the OpenCode Go key present; no ledger; session sudo opened 12:26:34 to 12:26:40 (`sudo -n true` works) | `logs/RA-1-check.txt:1-3`, `logs/RA-1-open.txt:1,11,19` |
| RA-2 | frozen research profile installed, `frozen_at` `2026-11-21T13:35:13Z` (was `null`), sha256 `3d0eaf64…`; ledger configuration `db706cd7…` unchanged; same digests on the guest | `logs/RA-2-owner.txt:3-9`, `logs/RA-2-guest.txt:1-2` |
| RA-3 | OpenRouter key installed at `/etc/tbound/provider/openrouter.key`: owner `tbound-pi:tbound-pi`, mode `400`, a regular file of 73 bytes. Only these facts are recorded; the value is not in any file here | `logs/RA-3-key.txt:1-2` |
| RA-4 | ledger genesis: `tbound-h1-research`, cap 5 000 000 micro-USD, records 1, head `6bed50e0…`, `profile_digest` `5f95f9ed…`; `checkpoint-1.json` records the same count and head; stderr empty | `logs/genesis.json:1`, `logs/checkpoint-1.json:1`, `logs/RA-4-genesis.err` (0 bytes) |
| RA-5 | research service config `c7e0211f…` | `logs/RA-5.txt:1` |
| RA-6 | managed live-root registration measured: `REG-OK`, digest `be141d6c…` | `logs/RA-6.txt:85,95` |
| RA-7 | S3 signed and verified (policy and signature only, "not a live-root quiescence permit"); `s3 rc=0` | `logs/RA-7.txt:1-4,10` |
| RA-8 | registration installed and verified | `logs/RA-8.txt:1-2` |
| RA-9 | S4 host profile `pi-host-profile-rr-08f3c3ee4043` signed, digest `d9dc8dce…`, template digest `82ae9316…`; `s4 rc=0` | `logs/RA-9.txt:10,13,14,19-21` |
| RA-10 | profile verified, template digests printed; installed (**inferred**: the log shows the verify and the template digests, not the install commands) | `logs/RA-10.txt:1-5` |
| RA-11 | start: the second unit reports `failed`; the journal shows the refusal quoted above at 15:24:42; maintenance lock free; `podman ps` prints an error about a missing `…/rundir/libpod` (the service is not running) | `logs/RA-11-start.txt:1-2,4,10-11,13` |
| close | session sudo removed at 15:25:42; `sudo -n true` fails again; only the `tbound-op` NOPASSWD rule plus the password rule remain | `logs/RA-11-close.txt:1,22,24` |
| after | the next step 33 (15:26:04) installed the engineering pilot service config again (`provider_route = engineering`, cfg `4e8ff190…`) | `logs/next-33-pass1.console.txt:1,205,211,218` |

The ledger stays at genesis: `records 1`, head `6bed50e0…`. The owner keeps `genesis.json` and `checkpoint-1.json` off the guest (runbook RA-4, `infra/guest/PROVISIONING-SIGNING.md:1274`).

## Why the service refused

The research route admits a pilot corpus only if the manifest pins its D48 pilot draw (`pins.draw_digest`); the shipped rehearsal counts are for the engineering route only (`supervisor/cmd/tbound/production_corpus_admission_linux.go:214-219`). The manifest of this round pins no draw (inferred from the refusal). So the install is complete and verified, and the first request on this route is the research pilot with its own drawn corpus.

## What this round shows and does not show

- **Shows:** the F32 guest takes the research route installation end to end up to the start (frozen profile, ledger configuration, key placement, genesis, registration, S3, S4, verify), and the start refuses a pilot corpus without a draw.
- **Does not show:** a provider exchange on OpenRouter, a reserved request, a ledger append, or the reconciliation of any request. None happened.
- **Not kept:** nothing from `diag/` (empty) or `runs/` (empty). The step 33 pass log of this round (`33-pass1.log`, installs the engineering config before the install steps) is not copied; the later one is `logs/next-33-pass1.console.txt`.

## Secrets

No key material is in these files: `RA-3-key.txt` has the path, owner, mode and length only. `RA-9.txt:11` prints the public key of the signed host profile (public). The signing keys and the OpenRouter key stay off these files (a search for `sk-`, `BEGIN` and `PRIVATE` finds only `tbound-op` log lines `BEGIN argv=` and the option name `--task-…`, in `RA-0-status.txt` and `next-33-pass1.console.txt`).
