# Global Agentic Instructions

## Atomic Commits

After completing each task, create a git commit immediately. Do not batch multiple tasks into a single commit.

**Rules:**

1. One commit per completed task — commit as soon as the task is done. This is standing authorization, do not wait for per commit approval.

2. Stage only the files changed for that specific task (use `git add <files>`, not `git add .`)

3. Write a concise commit message: `<type>: <what changed>` (e.g., `feat: add user auth endpoint`, `fix: resolve null check in parser`)

4. Valid types: `feat`, `fix`, `refactor`, `docs`, `test`, `chore`, `style`

5. Do NOT push automatically — commits stay local until the user explicitly requests a push

6. If a task touches multiple concerns, prefer splitting into smaller sequential commits

## Delegating to sub-agents

Model tiers for ANY delegated work — Agent-tool calls and Workflow-script `agent()` calls alike. Set the `model` parameter explicitly on every call; never omit it (omission silently inherits the session model):

- `haiku` — mechanical bulk work: renames, boilerplate, format conversion, log triage

- `sonnet` — default for well-specified implementation with clear acceptance criteria

- `opus` — genuinely tricky work: concurrency, subtle algorithms, adversarial verify/judge panels, gnarly debugging

- `fable` — rare; only when independence from your own context is the point (e.g. adversarial review of your own plan or a large diff). If you want to call a Fable sub-agent because the complexity of the task warrants it, ALWAYS check with me first — never spawn one unprompted.

When unsure between tiers, pick the cheaper and escalate on failure.

### Fable sessions: manage, don't execute

Applies ONLY if your system prompt says you are powered by a Fable (Mythos-class) model AND you are the top-level session. Sonnet/Opus/Haiku sessions, and spawned subagents of ANY model (including `fable` subagents): skip this section and work directly.

You are the most expensive tier. Your comparative advantage is decomposition, judgment, verification, and integration — spend your tokens there and route execution to cheaper models. These are defaults with criteria, not absolutes.

**Act directly (no subagent) when:**

- Understanding, not writing: questions, explanations, reads/greps. Delegate reading only for bulk surveys whose raw output would pollute your context.

- The change is small (roughly ≤30 lines across 1–2 files), or emerges from an active review loop where you already hold the context.

- Writing a self-contained brief would cost more than the task itself — that is the signal delegation is wrong for this task.

- Git operations: you own all staging and commits per the atomic-commits policy above.

**Delegate via the Agent tool when** implementation spans 3+ files, is mechanical/repetitive, or splits into independent pieces that can run in parallel (fan out only when work partitions cleanly by file with no overlapping writes). Pick the model per "Delegating to sub-agents" above. Delegate to minimize total cost and latency, not to maximize delegation — prefer one subagent with a complete brief over many fragments.

**Briefing standard:** subagents see none of our conversation. Every brief must be self-contained — exact file paths, the specific change, acceptance criteria, and the verification command to run — and must state commit policy: default "do NOT commit; the parent session owns commits." Exception — a subagent that is the sole writer in its worktree and is executing multiple sequential tasks may be granted "commit atomically per task as you go"; never grant commit authority to parallel subagents sharing a worktree.

**Reserve for yourself:** planning, architecture, judgment calls, resolving ambiguity with me, and final acceptance of delegated work. You hold the full conversation context; a context-free `fable` subagent doing these is equal cost with worse judgment.

**Verify, then commit:** never accept a subagent's self-report as done. Read the actual diff, run the project's verification gate, and check your acceptance criteria — then stage exactly that task's files and commit per the atomic-commits policy above. If you granted a subagent commit authority, verification shifts to its commit series: review `git log -p` over its range and run the gate before treating the work as accepted.

## Engineering Principles
- **Think before coding:** Identify assumptions, ambiguities, and tradeoffs. Clarify material uncertainties; use judgment for trivial ones
- **Simplicity first:** Implement the minimum solution. Avoid speculative features, unnecessary abstractions, and overengineering
- **Surgical changes:** Touch only relevant code, preserve existing conventions, and avoid unrelated refactors
- **Verifiable goals:** Define clear success criteria before implementation and verify outcomes

## Testing & Verification
- Prefer **TDD (red → green → refactor)**: reproduce the bug with a failing test first; add tests for new behavior
- Watch skips: a test that ran before and skips now is a regression. If the tests for your change are skipped here, it is **not verified** — say so, don't call it green
- Test crashes/recovery in a fresh process (see `internal/audit/audit_test.go`); never assert recovery from the same in-memory state
- Keep a receipt: exact command, exit code, and source hash. "Passed" without them is a story. Report unrun/skipped tests
- In PowerShell, stop on errors before editing files. Write generated source/JSON to a temporary file on the target volume, validate it, then replace the target; never overwrite from a failed or null conversion pipeline. Use Windows PowerShell 5.1-compatible commands.
- Keep tests focused and proportional. Run broader checks when risk warrants
- Reproduce bugs with failing tests before fixing; add tests for new behavior

## Task State (`TASK_STATE.md`)
- Read at session start if relevant; verify against current code
- TASK_STATE focuses on tbound for the master thesis in @MSE_MA_Thesis, so it should include research state, next steps, completed milestones etc.
- Update at major milestones or before ending unfinished work
- Record progress, decisions, blockers, and next steps
- Keep concise; remove stale information and avoid duplication