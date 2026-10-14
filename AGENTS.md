## Context Management
- Keep orchestrator context lean. Delegate codebase exploration to subagents with specific, targeted questions
- Read code directly only to verify critical details or resolve ambiguity
- Subagents return concise findings, changed files, decisions, test results, blockers, and next steps—not transcripts
- Give subagents minimal context and clear scope with fresh context; escalate ambiguity or failures
- Prefer deterministic scripts for fully mechanical operations

## Engineering Principles
- **Think before coding:** Identify assumptions, ambiguities, and tradeoffs. Clarify material uncertainties; use judgment for trivial ones
- **Simplicity first:** Implement the minimum solution. Avoid speculative features, unnecessary abstractions, and overengineering
- **Surgical changes:** Touch only relevant code, preserve existing conventions, and avoid unrelated refactors
- **Verifiable goals:** Define clear success criteria before implementation and verify outcomes

## Testing & Verification
- Prefer **TDD (red → green → refactor)**: reproduce the bug with a failing test first; add tests for new behavior
- Verify in Linux/WSL with the pinned Go 1.27.1 (not Windows), from `supervisor/`: `go build ./...`, `go vet -mod=readonly ./...`, `gofmt -l .`, `go test -race -mod=readonly -count=1 ./...` (private short TMPDIR, umask 077). Green locally must mean green in CI
- Watch skips: a test that ran before and skips now is a regression. If the tests for your change are skipped here, it is **not verified** — say so, don't call it green
- Test crashes/recovery in a fresh process (see `internal/audit/audit_test.go`); never assert recovery from the same in-memory state
- Keep a receipt: exact command, exit code, and source hash. "Passed" without them is a story. Report unrun/skipped tests
- **Green results never establish a claim** (`G1`/containment/`H1`); they verify code, not evidence
- Keep tests focused and proportional. Run broader checks when risk warrants
- Reproduce bugs with failing tests before fixing; add tests for new behavior

## Execution Efficiency
- Search before reading full files; expand only when necessary
- Bound tool outputs; save full logs and retrieve relevant excerpts when needed
- Reuse findings; avoid redundant searches, reads, and checks
- Batch related independent operations while preserving errors and exit statuses
- Stop exploration once sufficient evidence exists. Avoid unrelated work

## Operating rules

1. **Default to parallel.** A prompt that expects exactly one answer back is a mistake. If a task can be tried two ways, try both and compare. Two competing attempts that an eval can score is the best reason to fan out.

## Task State (`TASK_STATE.md`)
- Read at session start if relevant; verify against current code
- Update at major milestones or before ending unfinished work
- Record progress, decisions, blockers, and next steps
- Keep concise; remove stale information and avoid duplication
