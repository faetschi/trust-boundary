---
name: Delete Comment
description: Run one tranche of the tbound comment sweep. Use when asked to delete comments, continue the comment ban, lower the comment baseline, or repeat the comment audit.
---

## Policy

The owner-approved reading of the tbound comment ban:

- **Tautology** (restates what the code shows): delete.
- **Stale** (references code or files that no longer exist): delete. In a mixed
  comment, delete the stale clause and keep the live claim verbatim.
- **Excuse** (`shortcut:`, TODO, hack justification): file one GitHub issue per
  distinct hack, then delete the comment. The issue must carry file, line, the
  limit, and the recorded upgrade condition.
- **Decision Record** (non-obvious contract or choice): keep per-symbol godoc
  in place. If it is cross-cutting and no docs/design or docs/decisions file
  covers it, write a dated record in docs/decisions/ and delete the comment.

Never delete on a grep match alone. Read every hit in its file first; the
codebase uses words like "legacy", "stale" and "temporary" as domain terms.

Never touch: `//go:` directives, shebangs, `@ts-`, `@vitest`,
`eslint-disable` and other pragmas; `corpus/`, `evidence/`, `dist/`,
`node_modules/` (the counter skips them too).

## Workflow

1. Pick one tranche: a package, a directory, or a marker sweep across the
   tree. Do not attempt the whole tree in one pass.
2. Hunt candidates with both comment forms; a slash-only sweep misses shell
   and python:
   - `git grep -I -n -i -E '^\s*//.*(TODO|FIXME|HACK|shortcut:|workaround|for now)' -- '*.go' '*.ts' '*.tsx' '*.mjs' '*.js'`
   - the same pattern with `^\s*#` over `'*.sh' '*.py' '*.ps1'`
   - stale pointers: grep for paths of deleted files (verify with
     `git ls-files` before calling a reference stale)
3. Categorize each hit by reading it in context. Delete tautologies and stale
   text. For excuses, file the issue first:
   `gh issue create --repo faetschi/trust-boundary --title "<what the hack is>" --body "<file:line, the limit, the upgrade condition. Source: the explanatory comment removed by the comment-ban sweep.>"`
4. For a cross-cutting decision with no doc home, mirror the format of the
   existing files in docs/decisions/ (`# Decision: ...`, **Date:**, **Status:**,
   prose), then delete the comment.
5. Verify the tranche:
   - `gofmt -l` every touched Go file (empty output passes);
   - `sh scripts/check-comments.sh --update` (run only after deletions; it
     refuses any growth and lowers the baseline);
   - `sh scripts/check-comments.sh` (must print the OK line);
   - `sh scripts/check-comments.sh --self-test`.
   On a Windows host use Git's sh: `& "C:\Program Files\Git\bin\sh.exe" scripts/check-comments.sh ...`
6. Commit the code deletions and the lowered baseline together. The baseline
   total must not grow. New files must arrive with zero comment lines; the
   gate enforces this, not review.
