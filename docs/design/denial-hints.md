# Denial hints

Status: stage 1 (the static table) is merged into `main`; stage 2 is planned after the thesis. The offline decision-model chooser (C-DH) is described separately in [denial hint providers](denial-hint-providers.md).

When the gate denies a call, or an allowed `bash` command fails, the model gets one extra sentence that names what it may do instead. An unattended Pi then gets back onto a compliant route instead of retrying a variant of the denied call. There is no human prompt to wait on: a `DENY` returns immediately and the run continues.

## Stage 1: static table (thesis treatment)

- One fixed sentence per public reason code and for `command_failed`; at most 128 bytes; no paths; derived only from the public policy.
- Appended to the model-facing tool message only. The wire `Result` (`tbound-result/v1`), receipts, audit records and the D32 attacker projection do not change.
- Advisory: it grants no authority and never rewrites or executes a call. Every alternative the model proposes is authorized afresh.
- Frozen with the pilot freeze, before pre-registration (thesis D26, amendment 2026-10-23).
- Measured descriptively by the post-denial continuation metric (`internal/evalreport/post_denial.go`).

## Stage 2: generated, gate-verified hints (planned, post-thesis)

A static table cannot name the best alternative for a given task. Stage 2 generates the hint with a small model (for example Jev) outside the trusted computing base, and keeps the static table as the fallback:

```text
DENY or command_failed -> hint generator (outside the TCB) -> deterministic checks
  -> optional gate dry-run of a suggested call -> model-facing hint | static fallback
```

- **Input:** the reason code, the denied call, the public task statement and a public policy summary. No oracle data, host paths or credentials.
- **Deterministic checks:** fixed schema and length bound; no path that is not already in the task or the denied call; on a failed check, a timeout or a provider error, the static sentence.
- **Verified hints:** a suggested concrete call is dry-run through the same gate and shown only if it would be allowed, so a hint never sends Pi onto another denied route. The dry run authorizes nothing; the model must still propose the call.

Risks to design for:

- **Prompt injection:** the generator reads the task content and the denied call, both of which an attacker controls. A bad hint can mislead but cannot grant authority.
- **Disclosure:** a hint or a dry run can reveal which paths are protected. Dry runs are limited to paths already named in the task or the call, and count against the workflow limits.
- **Latency and cost:** one generator call, and possibly one dry run, per denial; fall back to the static sentence when over budget.

Thesis references: `MSE_MA_Thesis/tbound/tbound-thesis.md` §9.1, `AH-technical/99-future-work.md` §11.
