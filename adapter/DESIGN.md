# Pi closure probe: failure conditions

This is a provider-independent feasibility probe for the pinned Pi SDK. It
does not implement the trusted broker, authenticate proposals, or establish
the full `SurfaceClosed` predicate from the thesis contract.

The probe must fail closed if any of these conditions occurs:

1. The installed Pi package version differs from the pinned version, or an
   SDK/API shape changes so inventory cannot be inspected.
2. Pi selects or activates any tool outside `read`, `write`, `edit`, and
   `bash`, omits one of those tools, or reports an expected tool whose
   registered source is not the SDK custom-tool source.
3. A custom tool definition fails to replace its same-named native built-in,
   or any declared JSON schema is missing, malformed, or has unexpected
   fields.
4. The resource loader returns an extension, extension error, skill, prompt,
   theme, context file, system prompt, or appended system prompt; accepts
   resource additions; or permits reload/discovery to restore resources.
5. A negative fixture containing an undeclared tool or injected resource is
   accepted by the same admission predicates used for the live inventory.
6. Invoking a proxy tool does anything other than send exactly one structured
   proposal to the injected sender and return the sender's denial. Proxy action
   code must not itself read or mutate workspace files, spawn commands, or make
   network calls. The probe reads its package/profile metadata and writes only
   its report artifact as part of verification.
7. A real provider model is selected, a prompt is submitted, or a provider
   stream is attempted during the probe. Model selection is forced empty; Pi's
   internal `unknown` placeholder is permitted, and the `ModelRuntime` stream
   method throws and counts if any provider call is attempted.

The probe runs an actual SDK session for inventory, then invokes the four
custom tool implementations only against an in-memory fake sender. It does
not execute any Pi native tool. Negative evidence is labeled per case as
`SDK-session` when a real `createAgentSession` is constructed or inspected;
the evidence text says what the session case actually checks. A session being
constructed with an injected resource loader does not prove Pi consumed that
resource. `comparator-fixture` means only a standalone admission predicate or
resource-loader guard receives synthetic input. In particular, poisoned
extension/skill/context shapes and altered schema expectations are comparator
fixtures. The extra-tool and same-name fallback cases inspect actual Pi tool
inventories; the injected context case supplies a poisoned loader to a real
session, then applies the separate resource admission predicate. Unavailable
native-tool and session-reload cases also use actual SDK sessions.

The host broker manifest and independent
broker enforcement are absent from this adapter-only spike, so a passing
probe is evidence of Pi-side configuration and inventory only; it must never
be reported as complete thesis `SurfaceClosed` evidence. A successful run
writes a JSON report under `artifacts/` in this package. The probe logic is
repeatable, while the artifact includes run time, runtime, lockfile, and profile
hashes, so its contents vary between runs.
