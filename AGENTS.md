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
- Prefer **TDD (red → green → refactor)**
- Reproduce bugs with failing tests before fixing; add tests for new behavior
- Keep tests focused and proportional to the change. Run broader checks when risk warrants
- Never claim success without appropriate verification; report unrun tests

## Execution Efficiency
- Search before reading full files; expand only when necessary
- Bound tool outputs; save full logs and retrieve relevant excerpts when needed
- Reuse findings; avoid redundant searches, reads, and checks
- Batch related independent operations while preserving errors and exit statuses
- Stop exploration once sufficient evidence exists. Avoid unrelated work

## Task State (`TASK_STATE.md`)
- Read at session start if relevant; verify against current code
- Update at major milestones or before ending unfinished work
- Record progress, decisions, blockers, and next steps
- Keep concise; remove stale information and avoid duplication