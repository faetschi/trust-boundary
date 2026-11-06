# Phase 2b: assistant markdown as a typed AST

> **Historical.** This document records the Phase 2b markdown AST and its differential evidence. The legacy vanilla client it compares
> against (`internal/webchat/assets/`, `internal/webchat/jstest/`) was retired by audit
> finding #5; the code and the legacy test suites remain in git history at the commit
> before the retirement (`git log --diff-filter=D -- extensions/webchat/internal/webchat/assets`).
> Legacy-only commands and paths named below no longer run at HEAD. The retirement mapping
> is in phase2-parity.md section 11.

Status at `4700ca5`: the parser, the typed AST, the incremental parser and the differential net
are implemented and committed in `extensions/webchat/frontend/src/markdown/`. At that commit they
were not wired into the app. The legacy renderer (`internal/webchat/assets/markdown.mjs`) still
served the page, and it was unchanged. Phase 3 has since replaced it in the React client with
views that map this AST (`57bcd95`). Behaviour is the legacy renderer's
(web-chat.md §4.6), including its known simplifications.

## Layout

| File | Role |
|---|---|
| `ast.ts` | Closed discriminated unions for blocks and inlines; the `SafeHref` brand; `assertNever`. |
| `parse.ts` | `parseMarkdown`, `segment`, `splitBlocks`, `safeHref`, the truncation notice, `scanWork`. Pure: no DOM, no markup. |
| `incremental.ts` | `createIncrementalParser().update(fullText)`: stable-prefix reuse with object identity. |
| `toDom.ts` | **Test and diagnostic only.** AST to DOM through `createElement` and `createTextNode`. |
| `toHtmlString.ts` | **Test and diagnostic only.** AST to escaped HTML, cross-checked against the DOM mapping. |
| `*.test.ts` | Vitest suites: ported legacy tests, security properties, performance, and the differential. |
| `test-support/` | Test-only DOM shim, legacy loader, seeded corpora, and `legacy-fixtures.json`. |

No file in `src/markdown` imports React. The React mapper is Phase 3 work and is not part of this change.

## AST

Blocks: `paragraph{children}`, `heading{level: 3|4|5|6, children}`,
`list{ordered, start, items}` where an item is `layout: "inline"` (tight text) or
`layout: "blocks"` (nested structure), `blockquote{children}`,
`code{language: string | null, text}`, and `notice{kind: "truncated", text}`, which
is appended last when the source was cut.

Inlines: `text{value}`, `code{value}`, `em{children}`, `strong{children}`, and
`link{href: SafeHref, children: TextInline[]}`.

Invariants. The types enforce the first three; `security.test.ts` checks all of them
on parsed corpora.

- There is no raw-HTML node and no image node. Source markup is text.
- Every link `href` is a `SafeHref`, which only `safeHref` produces. A cast could
  still forge one, so the test counts `as SafeHref` in each source file (one, in
  `parse.ts`).
- Link children are text only, so a link never contains a link. This is a
  compile-time check too.
- No node has a style, attribute or event-handler key. Every key is in a closed set.
- A code language is `null` or matches the legacy token set `[A-Za-z0-9_+#.-]{1,32}`.
- Inline text is never empty. Adjacent text nodes can occur, as they do in the
  legacy DOM. Renderers must not depend on text-node boundaries.

A React mapper is one exhaustive `switch` on `type` per node family, ending in
`assertNever`. Adding a node kind is then a compile error until every mapping
handles it.

## Parser API

- `parseMarkdown(text, limits?)` returns `{blocks, truncated}`.
  `truncated` is `null` or `{chars?: {shown, total}, blocks?: {shown, total, ofShownText}}`.
- `createIncrementalParser(limits?).update(fullText)` returns
  `{blocks, reused, rendered}`. `reused` and `rendered` count source blocks, as the
  legacy renderer did, not AST nodes. A reused block is the same object in the new
  result, so a view can key its memoised rows on identity.
- `splitBlocks(text)` and `safeHref(raw)` keep the legacy semantics.
- `scanWork.steps` is the look-ahead work counter, with the legacy meaning.
- Defaults: 65 536 characters, 512 top-level blocks, nesting depth 4. Limits must be
  positive integers.

## Parity: legacy test to ported test

The legacy suite (`internal/webchat/jstest/markdown.test.mjs`) has 56 tests. Every
title is kept, so the mapping is by name. No assertion was dropped. The DOM-only
parts were moved to the mapping contract in `render.test.ts`, or restated on the AST.

| # | Legacy test | Ported to | Notes |
|---|---|---|---|
| 1–18 | hostile payload renders as literal text: *(18 names)* | `hostile.test.ts` | Same titles. AST has no link, and the DOM mapping creates only allowed tags. |
| 19 | no script, image, frame or style element is ever created | `hostile.test.ts` | The AST cannot create elements. The spy is on the DOM mapping's `createElement`. |
| 20 | random markup-shaped input never creates a forbidden element… | `hostile.test.ts` | Same generator, alphabet and 300 rounds. Checks AST hrefs and DOM tags. |
| 21 | the markdown module contains no HTML parsing or markup sinks | `security.test.ts` | Retargeted to the new modules. The legacy file is unchanged and no longer scanned. |
| 22 | safeHref allows only http, https and mailto, after cleaning | `parse.test.ts` | Same assertions. |
| 23 | an allowed link is an anchor with the hardened attributes | `parse.test.ts` + `render.test.ts` | Split: AST href and label; mapping `target` and `rel`. |
| 24 | an image is literal source text, never an image element | `parse.test.ts` | |
| 25 | splitBlocks divides at blank lines, headings and fences | `parse.test.ts` | |
| 26 | a blank line inside an unclosed fence does not end the block | `parse.test.ts` | |
| 27 | a fence closes only on a fence of the same marker and length | `parse.test.ts` | |
| 28 | a fenced block keeps its blank lines and its language | `parse.test.ts` + `render.test.ts` | Language on the AST; `language-js` class on the mapping. |
| 29 | a long code block is a labelled, focusable scroll region | `render.test.ts` | `role`, `tabindex` and `aria-label` are mapping attributes, not AST fields. |
| 30 | an unclosed fence streams as a code block with the content so far | `parse.test.ts` | |
| 31 | markdown and markup inside a fence stay literal | `parse.test.ts` | |
| 32 | headings render at h3 and deeper, never as page-level h1 | `parse.test.ts` + `render.test.ts` | Levels on the AST; tags on the mapping. |
| 33 | inline emphasis and code spans | `parse.test.ts` | |
| 34 | a code span drops one padding space from each side | `parse.test.ts` | |
| 35 | intraword underscores and escaped delimiters are literal | `parse.test.ts` | Absence of an `em` node. |
| 36 | bulleted and ordered lists, with the ordered start preserved | `parse.test.ts` + `render.test.ts` | `start` attribute on the mapping. |
| 37 | a nested list sits inside its item | `parse.test.ts` | |
| 38 | a block that starts with an indented item terminates and renders the list | `parse.test.ts` | |
| 39 | a lazy line after an item continues that item | `parse.test.ts` | |
| 40 | a blockquote holds its own paragraphs | `parse.test.ts` | |
| 41 | nested blockquotes are bounded by depth | `parse.test.ts` + `bounds.test.ts` | `bounds.test.ts` pins five container levels. |
| 42 | a soft line break inside a paragraph stays part of the paragraph | `parse.test.ts` | |
| 43 | every streamed prefix renders the same DOM as a fresh render | `incremental.test.ts` | AST equal to a fresh parse, and equal DOM fingerprint. |
| 44 | completed leading blocks keep their DOM node while the tail streams | `incremental.test.ts` | DOM node identity becomes AST object identity. |
| 45 | a text that does not extend the previous text fully re-renders | `incremental.test.ts` | |
| 46 | shrinking the text never leaves stale DOM behind | `incremental.test.ts` | |
| 47 | input beyond the character bound is truncated with a visible marker | `bounds.test.ts` | |
| 48 | input at the character bound carries no marker | `bounds.test.ts` | |
| 49 | a surrogate pair is never split at the truncation point | `bounds.test.ts` | |
| 50 | block count is bounded with a visible marker | `bounds.test.ts` | |
| 51 | a block count in the notice is the count of the original input | `bounds.test.ts` | Exact wording. |
| 52 | a block count in the notice says which text it counts | `bounds.test.ts` | Exact wording. |
| 53 | pathological delimiter runs stay within the work and time bounds | `perf.test.ts` | Work: at most 64 positions per character. Time: 1500 ms backstop. |
| 54 | 512 adversarial 4096-character messages stay within the work and time bounds | `perf.test.ts` | Measured: 3.37 positions per character. |
| 55 | a 4096-character message streamed one character at a time stays within the time bound | `perf.test.ts` + `differential.test.ts` | Time bound kept. Per-update work equal to legacy. |
| 56 | ordinary markdown renders the same DOM as before the look-ahead rewrite | `golden.test.ts` | The 31 legacy digests are reproduced. |

Counts: 97 tests in `src/markdown`, all passing and none skipped. Of these, 57 carry
a legacy title: the 56 legacy tests, with the allowed-link test split across two
files under one title. The other 40 are new. They cover the differential, security
properties, the shim contract, the incremental API, and the limits.

## Differential corpus

The differential suite runs the unchanged legacy module, loaded from its file, against
the new parser. An input agrees when the DOM fingerprint matches (tags, classes,
attributes and text-node boundaries), the AST's HTML equals the serialised DOM, and
the look-ahead work counter is identical.

| Corpus | Size | Seed | Mismatches |
|---|---|---|---|
| Golden ordinary samples (legacy digests) | 31 | fixed | 0 |
| Hostile payloads | 18 | fixed | 0 |
| Adversarial 4096-character messages | 6 | fixed | 0 |
| Seeded markdown-token fuzz (up to 40 tokens) | **100 000** | 0x5eed0001 | 0 |
| Link-shaped inputs (safe and unsafe schemes, noise) | 20 000 | 0x5eed0005 | 0 |
| Look-ahead window boundaries (distances 1 to 1025, URL 2046 to 2050) | 80 | fixed | 0 |
| Block-heavy inputs over the 512-block bound | 300 | 0x5eed0002 | 0 |
| `splitBlocks` and `safeHref` | 100 000 and 50 000 | 0x5eed0003 | 0 |
| Streamed messages (every update compared for reuse and render counts; final result compared) | **2 500 messages, 76 874 updates** | 0x5eed0004 | 0 |
| Legacy 170-character stream, every prefix (DOM and counts) | 170 | fixed | 0 |
| Per-update look-ahead work of a 4096-character streamed `*` message | 4096 updates, 16 773 114 positions in total | fixed | 0 |
| Bounds: 64 KiB cut, 512-block cut, depth 4, notice text | 16 | fixed | 0 |

Each corpus asserts its own minimum size. The security corpus is separate: 3 000
random markdown, 1 000 link-shaped inputs, the legacy fixtures, and the adversarial set.

## Mutation proof

Two mutants were applied to `parse.ts` in place, run, and then restored from a backup.
The file's SHA-256 was checked before and after each run.

1. `safeHref` allows `javascript:`. Result: 11 failing tests in 5 files, including the
   legacy `safeHref` and hostile tests, the fixed-point and scheme security test, and
   the golden, hostile, fuzz and link-shaped differentials (1 golden, 2 hostile,
   4 fuzz and 1 422 link-shaped mismatches).
2. The closer look-ahead window is doubled (`MAX_SPAN` becomes `MAX_SPAN * 2`). Result:
   the adversarial differential reports 2 mismatches, and the window-boundary
   differential reports 12 mismatches. Both tests fail. The golden sample named "Close
   *after the window" does **not** catch this mutant, because its closer is at
   distance 319 and is inside the window (see findings).

## Findings in the legacy renderer

These are reported. Nothing in the legacy file was changed. The port keeps the legacy
behaviour so that parity holds, and each finding is a decision for the owner.

1. **Escapes do not protect delimiters.** The look-ahead does not skip backslash
   escapes. Probed with the legacy module: `*a\*` renders as `<em>a\</em>`, and
   `[a\](https://a.example)` renders as a link whose label is `a\`. CommonMark and the
   legacy escape intent both say these should be literal. The new parser matches the
   legacy output.
2. **The "after the window" golden sample does not cross the window.** Its closer is
   at distance 319, inside the 512 window. The legacy suite therefore never tests the
   boundary. The new net adds 80 boundary inputs.
3. **Streaming costs O(n²) look-ahead work per block.** A 4096-character `*` message
   streamed one character at a time costs 16.8 million positions in total (523 ms in
   the perf test). A single parse is linear, as the work counter confirms. Each update
   is still a full re-parse of the tail block.

## Known simplifications (unchanged from web-chat.md §4.6)

- Fenced code inside a list or a blockquote is split out of its container.
- A loose list (blank lines between items) is split into separate lists.
- There is no autolink: a bare URL is text, not a link.
- Headings are demoted: `#` is `h3`, `##` is `h4`, and deeper levels cap at `h6`.
- A link label is literal text, so emphasis inside a label is not parsed. This is
  visible in the code and in `parse.test.ts`. It is not stated in §4.6.

## Deviations from the brief

- **Markup writes are refused by non-extensible elements, not by named setters.** The
  test DOM makes elements and text nodes non-extensible. Any write to a property it
  does not define throws a `TypeError` in strict mode, and so does calling an
  unimplemented method. The brief asked for `innerHTML`, `outerHTML` and
  `insertAdjacentHTML` to throw explicitly. Naming those sinks in a non-test source
  file would fail `check:sinks`, which scans every non-`.test.ts` file under `src/`.
  `dom-shim.test.ts` pins the behaviour, and it names the sinks.
- **The legacy fixtures are extracted, not retyped.** `legacy-fixtures.json` holds the
  hostile table, the 31 golden rows and the streaming sample, extracted from the legacy
  test file by a one-off script outside the repository. The 31 digests match the
  legacy values, so a transcription error would fail `golden.test.ts`.
- **Not updated by this change:** the phase status table in the
  ADR. It was outside its scope. The ADR table now records Phase 2 as committed.

## Receipts

Environment: WSL Ubuntu, Node v24.15.0, npm 11.12.1, Vitest 5.0.4, TypeScript 7.0.2.
Commands run from `extensions/webchat/frontend`.

| Command | Exit | Result |
|---|---|---|
| `npm run verify` (typecheck, test, build, check:sinks, check:deps) | 0 | 22 files and 296 tests pass, 0 skipped. The 296 include K's concurrent `src/core` tests, which pass. |
| `npm run test` (markdown only, verbose) | 0 | 10 files and 97 tests pass. |
| `npm run check:sinks` | 0 | 28 source files scanned, 0 sinks. |
| `npm run repro` | 0 | Two clean builds are identical. The committed `dist/` matches a rebuild (tree SHA-256 `85f7686c45dca416c00df825623098c7687492fd08982125dfb105ecf401add8`). `git status` shows no change under `dist/`. |
| `sh scripts/check-extension-boundary.sh` | 0 | Boundary OK. |

Source hash of `src/markdown` at the verified state: SHA-256 `2e7d45ea92a8d6fd7a2b562e1e022953cef2fe10125c7fb7bb37f6d8fba1c42b`
over the sorted `sha256sum` list of its 20 files.
