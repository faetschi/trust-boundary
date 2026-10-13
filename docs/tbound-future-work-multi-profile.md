# Future work: multiple registered Pi profiles

This is future engineering scope. The thesis candidate remains pinned to one
Pi CLI version and one Linux profile. No multi-profile or bring-your-own-Pi
mode is implemented or admitted.

## Profile boundary

A future profile may select a different Pi CLI release or approved extension
set only after it has its own signed identity and conformance evidence. An
unregistered executable or modified extension cannot inherit a claim from the
candidate profile.

At minimum, a profile would bind:

- Pi CLI version and executable identity;
- TBound extension bundle and exact four-tool schema digests;
- RPC arguments, environment, descriptors, and session settings;
- provider model/profile and broker binding;
- Linux runtime, source, containment, settlement, and publication profiles;
- closure, correlation, and external-effect evidence.

The current direction is the official Pi CLI in RPC mode with a TBound
extension. It does not embed an SDK `AgentSession`. A version check or executable
hash alone is not a signature, full dependency closure, or runtime admission.
See the [CLI reproduction decision](decisions/2026-10-12-cli-reproduction.md).

## Future admission and conformance

If multi-profile support becomes necessary, add a reviewed profile registry and
fail closed on unknown profiles or identity drift. For each candidate profile,
repeat tool-surface closure, provider correlation, schema checks, Linux
containment, process cleanup, and publication recovery tests. Keep custom tools,
extensions, and providers outside the admitted profile until they have their
own proxy and conformance path.

Do not create a permissive mode that inherits the governed label. Any
diagnostic launch of an unregistered Pi configuration must be structurally
separate, clearly non-claim-bearing, and unable to access the production
authority or credentials.

## Thesis guardrails

- Keep the registered experiment on one frozen Pi CLI 0.87.1 profile.
- Changing Pi version or extension bytes invalidates the candidate closure
  qualification until the complete target profile is rechecked.
- Preserve Go ownership of credentials, authorization, durable state, command
  execution, publication, and evidence.
- Keep the four-tool manifest fixed unless the protocol and conformance plan are
  explicitly amended before exposure.
- Treat multi-profile support as optional future work after the current CLI,
  signed-service, containment, and evidence gates pass.
