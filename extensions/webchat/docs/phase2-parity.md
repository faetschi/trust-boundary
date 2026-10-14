# Phase 2a: parity port of the DOM-free logic (`frontend/src/core/`)

Status: **Phase 2a implemented and committed in `4700ca5`.** The DOM-free
modules of the legacy client are ported to strict TypeScript under
`extensions/webchat/frontend/src/core/`. The legacy modules are unchanged and still
the shipped client. No product UI changed, and `dist/` is byte-identical
(`npm run repro`).

Claim boundary (unchanged by this port): no `G1`, containment or `H1` result is
established. Within the client, `UNKNOWN` stays `UNKNOWN`, a policy `ALLOW` is a
permission and never a success, and an unverified success is shown as `reported`,
never as verified. Green tests here verify code only.

## 1. What was ported

| Legacy module (`internal/webchat/assets/`) | Ported to (`frontend/src/core/`) | Notes |
|---|---|---|
| `format.mjs` | `format.ts` | `isVerified` takes any string and fails closed. `verifyItem` is the only way to make the `VerifiedEvidence` brand. |
| `filters.mjs` | `filters.ts` | Read model: takes partial items (`ItemView`). |
| `store.mjs` | `store.ts` | Takes validated contract values. Legacy defensive checks kept. |
| `trajectory.mjs` | `trajectory.ts` | Read model: takes partial items (`ItemView`). |
| `anchor.mjs` | `anchor.ts` | Pure arithmetic. |
| `backoff.mjs` | `backoff.ts` | Timer host injected. `random` defaults to `Math.random`, as before. |
| `stream.mjs` | `stream.ts` | `isOnline`, timers and `AbortController` are injected, with no global defaults. |
| `stop-sequence.mjs` | `stop-sequence.ts` | Pure. `isModalActive` injected, as before. |
| `api.mjs` | `api.ts` | `fetch` and `EventSource` injected. Raw transport kept for parity. Validated wrappers added. |
| `jstest/fake-timers.mjs` | `testing/fake-timers.ts` | Test helper. |
| (new) | `contract.ts` | Typed contract: closed vocabularies, discriminated `Item` union, `ItemView`, `Snapshot`, events. Derived from `contract.go` and `validate.go`. |
| (new) | `validate.ts` | Strict runtime validator, a TypeScript mirror of `validate.go`. The only place untrusted JSON becomes a typed value. |

Purity: `check-purity.test.ts` fails if any file under `src/core` imports
`react` or `react-dom`, references `document`, `window`, `navigator` or storage as
a value, or (in a module) calls `setTimeout`, `fetch`, `EventSource`,
`AbortController` and similar globals directly.

## 2. Parity table: legacy tests to ported tests

Counts are the number of tests (TAP `ok` lines for the legacy files, verbose
reporter entries for the ported files). All ported tests pass, and none are skipped.

| Legacy test file | Ported test file | Legacy count | Ported count | Status |
|---|---|---:|---:|---|
| `anchor.test.mjs` | `anchor.test.ts` | 4 | 4 | ported |
| `api.test.mjs` | `api.test.ts` | 10 | 10 | ported (globals stubbed → injected transports) |
| `backoff.test.mjs` | `backoff.test.ts` | 9 | 9 | ported |
| `filters.test.mjs` | `filters.test.ts` | 12 | 12 | ported |
| `format.test.mjs` | `format.test.ts` | 7 | 7 | ported |
| `stop-sequence.test.mjs` | `stop-sequence.test.ts` | 39 | 39 | ported (+1 `assert.ok`, see §6) |
| `store.test.mjs` | `store.test.ts` | 23 | 23 | ported |
| `stream.test.mjs` | `stream.test.ts` | 32 | 32 | ported |
| `trajectory.test.mjs` | `trajectory.test.ts` | 23 | 23 | ported |
| **Ported total** | | **159** | **159** | |

Not ported in this phase, with reasons. These 99 tests stay in the legacy suite,
which therefore still runs 258 tests:

| Legacy test file | Count | Reason |
|---|---:|---|
| `markdown.test.mjs` | 56 | Targets `markdown.mjs`. Ported in `src/markdown/` (Phase 2b, `phase2-markdown.md`). Not in `src/core`. |
| `render.test.mjs` | 37 | Targets `render.mjs`, which is DOM code. Replaced by React views in Phase 3. |
| `shell.test.mjs` | 4 | Reads CSS and HTML. Not ported logic. |
| `tokens.test.mjs` | 2 | Reads CSS tokens. Not ported logic. |

New tests with no legacy counterpart (the contract net, the boundary, and the differential):

| Test file | Count | What it checks |
|---|---:|---|
| `fixtures.test.ts` | 36 | The Go `--demo` and `--replay` fixtures parse through `validate.ts`; 27 refusal checks (18 table-driven, 9 in dedicated tests) for inputs `validate.go` refuses; downgrade; events; claim boundary on presentation. |
| `boundary.test.ts` | 8 | The validated transport: snapshot and event payloads accepted or refused, typed handlers, errors routed to `onError`. |
| `differential.test.ts` | 9 | Legacy modules vs TypeScript ports on seeded corpora (§3). |
| `check-purity.test.ts` | 2 | The purity gate and its self-test. |
| **New total** | **55** | |

Core total: 159 + 55 = **214 tests in 13 files**. Full `npm run test`: 313 tests in
24 files (core 214, `App.test.tsx` 2, `src/markdown` 97, owned by agent L).

### Measurement commands (exact)

Run in WSL (`Ubuntu`) with Node 24.15.0 and Go 1.27.1 as in the frontend notes.

```sh
# legacy: count TAP "ok" lines per file (node --test, reporter tap)
cd extensions/webchat/internal/webchat/jstest
node --test --test-reporter=tap anchor.test.mjs | grep -cE '^\s*ok [0-9]+'   # and the other files
# legacy total, from extensions/webchat
node --test internal/webchat/jstest/*.test.mjs       # -> tests 258, pass 258, fail 0, skipped 0

# ported: verbose reporter, counted per file
cd extensions/webchat/frontend
npx vitest run src/core --reporter=verbose | grep -E '^ +✓ src/core/[a-z.-]+\.test\.ts >'
npx vitest run src/core                              # -> Tests 214 passed (214), 0 skipped
```

## 3. Differential check (`differential.test.ts`)

The legacy `.mjs` modules are loaded by path (`VITE_WEBCHAT_LEGACY_ASSETS`, default
`internal/webchat/assets`), and each named export is checked at run time. Each
legacy function and its port receive the same seeded inputs (`mulberry32`, one
seed per corpus). Every output is deep-compared, and the whole store state (Map
contents included) is compared after every step.

| Corpus | Seed | Comparisons (each a deep-equal of two outputs) |
|---|---|---:|
| format helpers (`slug`, `humanize`, `isVerified`, `isVerifiedProjection`, `stateChipClass`, `verdictChipClass`, `toolGlyph`, `shortDigest`, `formatClock`, `formatStamp`, constants) | `0x5eed0001` | 23,364 |
| filters (`matchesFilter`, `filterItems`, `countFilter`, `governanceCounts`, `FILTERS`) | `0x5eed0002` | 50,634 |
| store reducers: 120 runs × 250 steps, plus 3 long runs × 1,400 steps (run 0 has no snapshots, so the client cap trims with no earlier server loss); `groupTurn`/`turnsOf` every 10 steps | `0x5eed0003`, `0x5eed0004` | 40,200 |
| trajectory (every projection, `visibleRecords`, `sequenceLanes`, `searchRecords`, `groupRecords`) | `0x5eed0005` | 48,359 |
| anchor arithmetic: 20,000 seeded metric sets | `0x5eed0006` | 140,001 |
| backoff delays (600 seeded scripts) and scheduler fire order (300 seeded schedules) | `0x5eed0007` | 112,801 |
| stop-sequence `step()`: 300 streams × 150 events | `0x5eed0008` | 90,007 |
| **Total** | | **505,366** |

Mismatches: **0**.

Store coverage, measured on the TypeScript side during the run (so the corpus is
shown to reach each branch): applied 15,020; duplicate 2,030; gap 2,589; invalid
831; server window gap on 26,189 steps; client window gap on 242 steps; evictions
on 26,775 steps; window at the 512-item cap on 683 steps. The corpus test fails if
any of these branches is not reached.

Corpus generation is driven by the store's own model, so it produces valid
sequences and deliberate duplicates, gaps and invalid sequences in realistic
proportions. An earlier version of the generator drifted from the store; that was
caught by the coverage numbers, and the generator was corrected.

### Mutation proof

The differential suite must fail when a legacy function changes. Two mutants were
made in a copy under `/tmp/webchat-mutant/assets`, outside the repository. The
repository's legacy files were not edited (`git status` shows
`internal/webchat/assets` unchanged).

- **Mutant A (equivalent, not detected, as it should not be).** `store.mjs`,
  `holdsFrom`: loop bound `sequence <= state.lastSequence` changed to
  `sequence < state.lastSequence`. The last element of a window is always present
  (`lastSequence` is the highest item kept, and trimming removes only the oldest), so
  this changes no observable state. The suite stays green, which is correct.
- **Mutant B (detected).** `store.mjs`, `updateWindowGap` and `applyItem`: the client
  window-gap start `trimmed[0].sequence` changed to `trimmed[trimmed.length - 1].sequence`.
  Command:

  ```sh
  VITE_WEBCHAT_LEGACY_ASSETS=/tmp/webchat-mutant/assets npx vitest run src/core/differential.test.ts
  ```

  Result: exit 1; `Tests 1 failed | 8 passed`; the failing assertion is
  `differential: store reducers > the full store state matches the legacy state after every step`
  at `run 77 step 169 state`. The unmutated suite passes 9 of 9.

## 4. Deliberate differences from the legacy modules

These are the interface changes the port makes. Behaviour for every input the
legacy tests and the differential exercise is identical.

1. **Injected browser facilities.** `createStream` requires `isOnline`, `setTimer`,
   `clearTimer` and `createAbortController`; the legacy defaults that read
   `navigator`, `setTimeout` and `AbortController` are gone. `random` still defaults
   to `Math.random`. `fetchSnapshot(transport, signal?)` and
   `openEventStream(factory, handlers)` take `fetch` and `EventSource` as parameters.
   The legacy reads of `globalThis.fetch` and `globalThis.EventSource` are gone.
2. **Two layers in `api.ts`.** The raw transport (`fetchSnapshot`, `openEventStream`)
   returns decoded JSON unchanged, because `api.test.mjs` depends on it. The
   validated wrappers (`fetchValidatedSnapshot`, `openValidatedEventStream`) run the
   strict validator at the JSON boundary, and a payload that fails rejects or goes to
   `onError`. Phase 3 wiring should use the validated wrappers.
3. **Read model vs boundary types.** `format`, `filters` and `trajectory` take
   `ItemView` (`Partial<ItemFields>`), because the legacy tests and the read model
   handle partial elements. `store` and `stream` take the validated `Item` and
   `Snapshot`.
4. **Verified gate as a type.** A success is `ok` only with a `VerifiedEvidence`
   value, which only `verifyItem` creates. `successChipClass` reads `reported`
   without one.
5. **`setFilter` takes `FilterId`.** The legacy accepted any value. Runtime behaviour
   for a value outside the set is unchanged, but the type refuses it.
6. **Strict validator.** `validate.ts` is a TypeScript mirror of `validate.go`
   (§5). Where it is stricter than Go, the difference is listed there.

## 5. Validator vs `validate.go` (strictness only)

`validate.ts` accepts every document and snapshot the Go side accepts, except these
(the TypeScript side refuses them, and each is fail-closed):

- `null` where Go decodes it as the zero value.
- A forbidden field with a zero value, for example `"kind": ""` on a message. Go's
  `forbiddenField` tests for a non-zero value, so Go accepts it. The TypeScript side
  refuses any field outside the type's closed key set.
- An unknown `authority` or `verification_scope` in a document. Go overwrites it
  with the downgrade before checking. The TypeScript side refuses it first.
- `h1` and `containment` are closed to the values a document may carry
  (`unexecuted`, `unavailable`; `not-established`, `unavailable`). Go accepts any
  label for a served snapshot. This is stricter only in theory, because the gateway
  serves only documents today.

## 6. Test-file changes (assertions kept)

Every legacy assertion is kept. The changes are structural, and each is disclosed
by the agent that made it:

- `node:test` → `vitest` `test`. `node:assert/strict` kept, typed by
  `testing/node-assert.d.ts` (a declaration of the subset the tests use; the project
  has no `@types/node` and adds no dependency).
- Partial legacy fixtures → `messageItem`/`activityItem`/`decisionItem`/`resultItem`/
  `snapshotOf` builders with the same ids, sequences and turn ids. `api.test`
  `globalThis` stubs → injected transport and event-source factory.
- `stop-sequence.test` gained one `assert.ok(intent !== null)` before a narrowing
  read; the legacy would throw a `TypeError` in the same case.
- `api.test` `FakeEventSource.dispatch` throws an explicit `Error` when no listener is
  registered, where the legacy threw a `TypeError`. No test reaches that path.
- Twelve `@ts-expect-error` directives, each on one deliberately malformed value and
  each with a reason: `stream.test` (3), `store.test` (5), `trajectory.test` (2),
  `stop-sequence.test` (2). A directive that stops being an error fails `tsc`.
- One documented boundary cast: `differential.test.ts`, the legacy loader (`mod as T`
  after each export is checked at run time). No `any`, no `as unknown as`, no
  `@ts-ignore` anywhere in `src/core`.

## 7. Legacy behaviour found while porting (reported, not fixed)

Legacy code is not changed in this phase. The TypeScript port keeps the same
behaviour so the parity net holds.

1. **Prototype keys read inherited properties (low).** Lookup tables indexed by a
   wire string (`TOOL_GLYPH`, `DOT_BY_VARIANT`, `SOURCE_LABELS`, `VERDICT_WORDS`,
   `STATE_WORDS`, `TONE_WORDS`) return an inherited property for keys such as
   `constructor` or `toString`. Verified in Node:
   `recordDot({type:'result', state:'constructor'})` returns the `Object` function,
   and `toolGlyph('toString')` returns a function. The Go contract rejects such
   values, so the gateway cannot deliver them. The TypeScript type `Tone` is not true
   for those inputs, and the port's comments say so.
2. **`preview()` splits a surrogate pair (low, cosmetic, reachable).**
   `trajectory.mjs` `preview` slices by UTF-16 code unit. An emoji that straddles
   `MAX_PREVIEW` leaves a lone high surrogate before `…`. Verified in Node: a 159-unit
   text plus one emoji gives a 161-unit preview ending in `d83d`. Transcript text can
   contain such characters.
3. **Go: documents are downgraded before they are refused (documentation mismatch,
   not a regression).** `decodeDocument` runs `downgradeDocument` before
   `ValidateSnapshot`. So `authority: "authoritative"` or `"observed"` in a fixture or
   replay is silently downgraded to `diagnostic` and accepted. `web-chat.md` §4.1
   says both are refused. The downgrade is fail-safe, but the document and the code
   disagree. Not changed here.
4. **Go: `forbiddenField` tests for a non-zero value.** A forbidden field with a zero
   value is accepted (§5). Low.

## 8. Limits

- The UI is not ported. `render.mjs` and `app.mjs` are Phase 3, so no browser or
  DOM behaviour is covered here.
- The purity gate is a regex scan, not a parser. It flags the named references. It
  cannot see indirect access (for example `globalThis[name]`). Local bindings named
  `document` or `window` are flagged too, so the fixtures test renamed its locals.
- The differential shows the port matches the legacy module. It does not show that
  the legacy module is correct.
- The corpus generator is seeded, so the corpora are reproducible. They are not
  exhaustive.
- Vitest runs in WSL/Linux (CI and the frontend notes). The legacy path is derived
  from `import.meta.url`, which on native Windows gives a `/C:/...` pathname that
  Node will not resolve. Run the differential suite under WSL.
- Strict validation is wired only into the new typed transports. No UI consumes them
  yet.

## 9. Verification receipts

Frontend directory: `extensions/webchat/frontend`. Node 24.15.0, npm 11.12.1, Vitest
5.0.4, TypeScript 7.0.2, all in WSL `Ubuntu`. Exit codes are from the final run.

| Command | Exit | Result |
|---|---:|---|
| `npm run typecheck` | 0 | `tsc --noEmit`, whole project |
| core-only typecheck: `npx tsc --noEmit -p /tmp/core-tsc/tsconfig.json` | 0 | `tsconfig` extending the project's, `include` = `src/core/**` (isolates the core from in-progress `src/markdown`) |
| `npm run test` | 0 | 24 files, 313 tests passed, 0 failed, 0 skipped |
| `npx vitest run src/core` | 0 | 13 files, 214 passed, 0 skipped |
| `npm run verify` | 0 | typecheck; test 313; build; `check:sinks`; `check:deps` |
| `npm run repro` | 0 | builds identical (4 files); committed `dist/` identical to a clean rebuild; tree sha256 `85f7686c45dca416c00df825623098c7687492fd08982125dfb105ecf401add8` (the P1 value) |
| `node --test internal/webchat/jstest/*.test.mjs` (from `extensions/webchat`) | 0 | tests 258, pass 258, fail 0, skipped 0 |
| `go build ./...` (from `extensions/webchat`) | 0 | |
| `go vet -mod=readonly ./...` | 0 | |
| `gofmt -l .` | 0 | no output |
| `go test -race -mod=readonly -count=1 ./...` | 0 | `tbound/webchat/cmd/tbound-webchat` ok, `tbound/webchat/internal/webchat` ok |

Source hash: `sha256sum` over the 27 `*.ts` files under `frontend/src/core` (sorted
by path, including `testing/` and `*.test.ts`), piped to `sha256sum`, gives
`c66cd1394b43dea6f1535aefd70e57265640d7e0ab7ca2a8a2fd6d730bdc270c`. This hash was computed after the last edit to `src/core`, with the test run in §9 done on the same tree.

Worktree note. At the time of the run, other uncommitted work shared this worktree
(since committed in `2583f5a` and `4700ca5`). The gate scripts
`frontend/scripts/check-sinks.mjs`, `check-deps.mjs` and `repro-check.mjs`,
`internal/webchat/handler.go`, `app.go` and `app_test.go` were modified, and
`frontend/src/markdown/` and `docs/phase2-markdown.md` were untracked. My gate runs
used those files as they were, and they passed. `internal/webchat/assets/`,
`jstest/`, `package.json`, the lockfile, `vite.config.ts` and `tsconfig.json` are
unchanged by this phase.
