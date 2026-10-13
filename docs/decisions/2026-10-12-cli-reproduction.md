# Decision: reproduce Pi through the official CLI

**Date:** 2026-10-12

**Status:** selected implementation direction; offline CLI development route
verified, production integration and qualification remain blocked.

TBound will run the official Pi CLI at version 0.87.1 in RPC mode and load one
TBound extension. That extension exposes exactly `read`, `write`, `edit`,
and `bash` as broker-backed proxy tools. Go remains responsible for provider
credentials and exchange, authorization, durable effects, command execution,
and publication. The extension does not construct or host a Pi agent session.

The old embedded SDK worker and its resource-loader closure report do not
qualify this architecture. A Pi version check and executable SHA-256 fingerprint
identify the CLI file used by doctor; they do not authenticate the executable,
pin its transitive dependency closure, or admit the full runtime. Admission must
bind the CLI, extension, arguments, environment, descriptors, and runtime
dependencies to the signed profile, then pass closure and effect checks.

The intended profile arguments are:

```text
pi --mode rpc --no-session --no-extensions --no-builtin-tools
  --no-skills --no-prompt-templates --no-themes --no-context-files
  --no-approve --offline
  --extension <isolated-tbound-extension>
  --provider tbound-go-broker
  --model tbound-go-broker/nvidia/nemotron-3.5-lightning:free
```

This is the profile argv shape exercised by the offline development launcher.
The fixture and an isolated extension test passed on the existing VM, but
they do not qualify production. The profile tooling and production bindings
now use the official CLI argument contract. The D09 Pi CLI launcher still
refuses unconditionally. Complete and verify signed runtime closure admission,
managed-service integration, containment and settlement before enabling a
production profile; signing alone does not enable production.

Pin Pi at 0.87.1 for the candidate reproduction. Pin Node.js at 24.21.0 for
reproducibility; Pi's documented minimum of Node 22.19 is a compatibility floor,
not the experiment runtime. Install Pi inside Linux. Do not install or maintain
a private Pi SDK package tree for the worker.

This direction keeps the thesis CLI-first operator path. It does not establish
a positive governed run, D06 provenance, Linux containment, publication, or G1.
The existing VM reports Landlock ABI 4 while the cell entrypoint requires ABI 8;
source qualification, signed-profile integration, cleanup, and independent
effect oracles remain open. Pi installation on the existing VM was verified,
but the host is not a clean qualified image.

The recovery plan remains 48 trials (16 scenarios × 3). The eighth crash point
is E07 publication conformance and is outside that denominator. Before protocol
freeze or registration, cross-check this decision against the normative recovery
specification and record a dated amendment there. Do not silently alter either
count or classification.
