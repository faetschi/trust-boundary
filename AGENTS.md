# Global Coding Standards

1. **YAGNI / Keep It Simple**: Don't build it until you need it. Avoid premature abstractions and overengineering.
2. **DRY**: Extract patterns after second duplication, not before.
3. **Fail Fast**: Explicit errors beat silent failures.
4. **Simple First**: Write the obvious solution, optimize only if needed.
5. **Delete Aggressively**: Less code = fewer bugs.
6. **Semantic Naming**: Always name variables, parameters, and API endpoints with verbose, self-documenting names that optimize for comprehension by both humans and LLMs, not brevity (e.g., `wait_until_obs_is_saved` vs. `wait...`).

## Testing & Verification

1. **TDD:** Reproduce bugs with failing tests; test new behavior (red → green → refactor).
2. **No False Greens:** Skipped or unrun tests are unverified. Report them explicitly.
3. **Fresh-Process Recovery:** Test crashes/recovery in a new process, never reused in-memory state.
4. **Risk-Based Testing:** Run focused tests first; expand coverage based on risk.
5. **Verify Before Done:** Check acceptance criteria and actual results before declaring success.

# Atomic Commits

After completing each task, create a git commit immediately. Do not batch multiple tasks into a single commit.

1. One commit per completed task — commit as soon as the task is done. This is standing authorization, do not wait for per commit approval.
2. Stage only the files changed for that specific task (use `git add <files>`, not `git add .`)
3. Write a concise commit message: `<type>: <what changed>` (e.g., `feat: add user auth endpoint`, `fix: resolve null check in parser`)
4. Valid types: `feat`, `fix`, `refactor`, `docs`, `test`, `chore`, `style`
5. Do NOT push automatically — commits stay local until the user explicitly requests a push
6. If a task touches multiple concerns, prefer splitting into smaller sequential commits

# Subagent Delegation

You are a coordination-only orchestrator. Delegate nearly all work to subagents, including file exploration, reading, research, analysis, implementation, debugging, and testing. Your role is strictly to plan, delegate, coordinate, and review final results. Keep the main thread's context minimal.

## Delegate When

For ANY task that touches more than 3 files or involves refactoring or mechanical work:

- Implementation spans 3+ files, involves repetitive work, or has independent subtasks.
- Self-contained work would require ~3+ file reads/edits inline.
- Nontrivial changes (core logic, security, or >1 file) require fresh independent verification before acceptance.
- Parallelize only independent tasks with no overlapping writes or dependencies; batch launches where possible.

**Keep in main thread:** Planning, architecture, ambiguity, judgment, final acceptance, git operations, small changes (≤30 lines across 1–2 files), and ordinary reading/grepping. Delegate bulk surveys if they would pollute context.

## Don't delegate when

- Understanding, not writing: questions, explanations, reads/greps. Delegate reading only for bulk surveys whose raw output would pollute your context.
- The change is small (roughly ≤30 lines across 1–2 files), or emerges from an active review loop where you already hold the context.
- Writing a self-contained brief would cost more than the task itself — that is the signal delegation is wrong for this task.
- Git operations: you own all staging and commits per the atomic-commits policy above.

## Orchestration

Always explicitly specify `model` for every Agent-tool or Workflow `agent()` call; never omit it (omission silently inherits the session model):

- `haiku` — mechanical bulk work: renames, boilerplate, format conversion, log triage

- `sonnet` — default for well-specified implementation with clear acceptance criteria

- `opus` — genuinely tricky work: concurrency, subtle algorithms, adversarial verify/judge panels, gnarly debugging

- `codex GPT 6.1 Sol: reasoning xhigh` — for adversial review of your own plan or a large change/diff, call this agent often for reviews of changes made by other subagents or when merging changes of multiple subagents.

- `fable` — rare; only when independence from your own context is the point (e.g. adversarial review of your own plan or a large diff). If you want to call a Fable sub-agent because the complexity of the task warrants it, ALWAYS check with me first — never spawn one unprompted.

When unsure between tiers, pick the cheaper and escalate on failure.

### Delegation Protocol

1. **Brief:** Provide self-contained instructions: exact paths, task, acceptance criteria, verification command, and commit policy. Default: no commits.
2. **Verify:** Inspect actual diffs, run relevant tests, and check acceptance criteria. Never trust self-reported completion alone.
3. **Commit:** Parent commits verified changes atomically. If a sole-writer subagent was authorized to commit, review its `git log -p` and verification results before acceptance.

### Fable Top-Level Sessions Only: manage, don't execute

Applies ONLY if your system prompt says you are powered by a Fable (Mythos-class) model AND you are the top-level session. Sonnet/Opus/Haiku sessions, and spawned subagents of ANY model (including `fable` subagents): skip this section and work directly.

- Prioritize decomposition, judgment, verification, and integration.
- Delegate well-scoped execution to cheaper models.
- Execute directly only when delegation offers no clear benefit.

# Task State (`TASK_STATE.md`)
- Read when relevant at session start; verify against actual code.
- Track tbound master thesis progress in @MSE_MA_Thesis: research state, decisions, milestones, blockers, and next steps.
- Update at major milestones or before ending unfinished work.
- Keep concise, current, and nonredundant.