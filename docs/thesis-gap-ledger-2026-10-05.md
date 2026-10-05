# Five-slice evidence and thesis-gap ledger — 2026-10-05

This supplements, rather than overwrites, the pre-existing
`docs/implementation-status.md`. That document preserves historical guest
reports and user infrastructure work; its older component-status paragraphs do
not describe newer committed evidence. This ledger closes the requested execution
record, not acceptance of preserved dirty user source or a completed thesis
demonstration. Three slices are implemented/verified; two have evidenced blockers.

## Slice evidence

| Slice | Accepted identity / evidence | Current outcome |
| --- | --- | --- |
| 1 — genuine provider → durable executor | `1bedc0ec350edcbb2b7e3df4d877de50f5308c66`; `docs/provider-durable-evidence.md`; allowlisted genuine receipt in `docs/evidence/real-provider-durable-2026-10-05.json` | Independently verified WSL full race suite, Windows build/internal/e2e, genuine credential-gated synthetic-content exchange and committed-byte comparison. |
| 3 — read-only explorer | `b827199c9550fd89b3e713d19ae067b90d8c4250`; `docs/test-run-explorer.md` | Full isolated race suite, Windows checks, actual live Go JSON runs, private Chrome LIVE/REPLAY/mobile checks and committed-byte comparison passed. |
| 2 — recoverable publication | `4cbfbf6b7beacf81de70841bf12703809b9b6bf3`; `docs/publication-contract.md` | Full combined-source Linux race suite, Windows build/internal/e2e, formatting/diff/module and committed-byte checks passed. Package composition with real sessionrepo-generated evidence, not production CLI/gate wiring. |
| 4 — current-source offline guest verifier | `022b74a7c21633ed4d00afd748dd82357144df7e`; no fresh guest report | Blocked; see `docs/verifier-blockers-2026-10-05.md`. Historical guest PASS reports do not verify current source. |
| 5 — frozen containment | `72a682700eb247232a68f9be11ed76621f0fab53`; no admitted profile | Blocked; see `docs/containment-blockers-2026-10-05.md`. Containment `not-established`, G1=false. |

Final tested implementation source: `4cbfbf6b7beacf81de70841bf12703809b9b6bf3`.
LF source archive SHA-256:
`816abd10797fbc34fd4107b3f99d85725a59d9c9a4943da7b1a2ab6b07654f71`;
139-file host manifest SHA-256:
`6418fe1b5092b37d0b946076bb8107fc6d5a7595b3b2b6b6a84f671d8668463e`.
All exported file hashes passed readback; neither artifact is a guest report.
Later documentation-only commits do not change this tested implementation tree.

## Claims that remain missing

- A complete pinned Pi SDK/adapter conversation exercising all four proxy tools,
  authenticated cell IPC, governed result continuation and externally authentic
  policy authority. Slice 1 proves a genuine captured write through durable IPC
  with a synthetic receipt authority, not this complete profile.
- Current-source guest offline evidence with exact source identity, complete
  report/artifact hashes and authorized helper/operator admission. WSL tests and
  historical guest reports are not substitutes.
- Production integration of publication authority and the approved-generation
  workflow. Per-object recovery must not be described as filesystem-wide
  atomicity, idempotent effect replay, or permission to restore old live state.
- The whole frozen containment profile: authorized offline image signature,
  signed in-image entrypoint, exact static seccomp/Landlock policy, fixed delegated
  cgroup-v2 lifecycle, recursive descendant settlement and a surviving observer.
  Digest-pinned rootless containers and mechanism probes alone do not establish it.
- Private-Git initialization under process §6/D06 is explicitly waived/divergent
  for this prototype scope. That is not normative compliance.
- The broader user-authored interactive Pi frontend plan is not completed by a
  read-only test explorer. The CLI remains claim-bearing; viewer output remains
  untrusted and nonauthoritative, even when its local audit reconstruction passes.
- Full narrow-demonstration and thesis evaluation acceptance, including the
  declared evaluation host/profile and required falsification/fault evidence.

No headline containment/security result, completed demonstration, completed
frontend plan or thesis completion follows from closing these code slices.
