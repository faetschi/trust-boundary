# Qualification and research procedure — 2026-10-14

Status: adopted for the implementation work by the user's explicit instructions
on 2026-10-14. Branch: `codex/tbound-prototype`.

## Host qualification

Adjust the existing Hyper-V setup to provide the implementation's exact Landlock
ABI 8 profile. The user authorizes disposal of the existing development VM and
replacement with a fresh disposable qualification guest with similar attributes.
The observed reference resources are 8 vCPUs, 16 GiB fixed RAM and a 40 GiB disk;
see [the inventory receipt](../evidence/host-inventory-20261014.json).
Inspect and preserve required artifacts before disposal. Verify the replacement's
live ABI, filesystem, runtime closure and service profile before qualification.
Use distinct private provisioning paths and keep credentials out of source control.

## Final Research Claims milestone

Research Claims remains last, after engineering, deployment and product operations.
The user, guided by the thesis advisor, will perform construct review. OSF/Zenodo
accounts and multiple independent human construct reviewers are not required for
this implementation completion procedure. The former external-registration and
role-separated human-review requirements are superseded here; the thesis sources
have not been rewritten by this decision.

Freeze the executed protocol, corpus, analysis and artifact identities locally in
version control. Retain review records and raw results. Describe the actual review
and registration procedure in reports; do not label self-review independent or
claim external registration. Record the resulting validity limitations.

Independent runtime/effect oracles, the engineering gates, D14/D20 constraints,
and the security/calibration/friction acceptance criteria remain requirements.
G1 and H1 stay unestablished until their relevant evidence has been executed and
reviewed. OpenRouter is the intended provider route; define and record the model,
request/token limits and monetary budget before provider evaluation runs.

The user subsequently fixed the total OpenRouter spend at **US$5 maximum** and
selected **DeepSeek V4.1 Flash**, model ID `deepseek/deepseek-v4.1-flash`.
The [OpenRouter model page](https://openrouter.ai/deepseek/deepseek-v4.1-flash)
confirmed that ID on 2026-10-14. Freeze provider routing, current price ceilings,
request/token limits and the complete trial cost estimate before paid execution.
Reserve each request's worst-case cost against the shared durable budget before
sending it; retain uncertain charges across restart. Include pilot, retry,
calibration and evaluation calls in the US$5 total. If the complete protocol
cannot fit, report the unfinished evidence rather than reduce its denominator
or exceed the cap. No paid runs have been executed under this decision.

## Delegation

Allow the current Claude Haiku tasks to finish. All subsequent delegated work uses
GPT 6 Luna with reasoning `max`, superseding the earlier Haiku preference.
