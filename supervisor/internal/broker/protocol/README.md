# Pi proposal protocol boundary

`Stream` accepts broker-owned raw provider arguments separately from the
untrusted Pi JSON request emitted by the four proxy tools. The Pi request is
exactly `schema_version`, `tool_call_id`, `tool`, and `arguments`. Unknown
fields, duplicate JSON keys, malformed UTF-8, unpaired UTF-16 surrogate
escapes, trailing values, frames over 1 MiB, and JSON nesting beyond 64 levels
close the stream. Valid escaped surrogate pairs represent Unicode scalar
values and remain admissible. Top-level field names are case-sensitive and
must match those four names exactly; this avoids the case-insensitive matching
performed by `encoding/json` when decoding structs. The transport must enforce
the 1 MiB frame limit while reading, before it allocates the byte slice passed
to `Stream.Propose`.

`NewStream` pins both the registered tool-call issuer and response-ID issuer.
Every capture must use those namespaces. The tool-call ID, response ID,
sequence, generation, and raw arguments come from `TrustedCapture`; they cannot
be populated from Pi proposal metadata. `Capture` computes the capture digest
from broker-owned raw argument bytes with the same function used for proposals
and checks any asserted broker digest against that result. A digest without raw
captured arguments is rejected. The stream binds its opaque call ID to the
registered issuer and passes the computed digest to the existing correlator.
Unknown, duplicate, replayed, out-of-order, mismatched, and malformed calls
fail closed.

Canonical argument bytes follow [RFC 8785](https://www.rfc-editor.org/rfc/rfc8785)
using the pinned
`github.com/gowebpki/jcs v1.0.1` implementation. The dependency is pinned in
`go.mod` and `go.sum`; the two checksums were obtained from the authenticated
Go checksum database lookup
[`https://sum.golang.org/lookup/github.com/gowebpki/jcs@v1.0.1`](https://sum.golang.org/lookup/github.com/gowebpki/jcs@v1.0.1),
log record `20078570`. The inspected implementation source is the upstream
[`v1.0.1` tag](https://github.com/gowebpki/jcs/tree/v1.0.1), whose `Transform`
API canonicalizes raw UTF-8 JSON bytes.

The protocol retains strict input checks before canonicalization: duplicate
object names, non-UTF-8 data, unpaired surrogates, non-finite or non-binary64
numbers, excessive depth/size, and trailing JSON values are rejected. Valid
Unicode is preserved without normalization; JCS sorts object property names
by UTF-16 code units and emits ECMAScript-compatible strings and numbers.
Verified [RFC 8785 Erratum 7920](https://www.rfc-editor.org/errata/eid7920)
is applied: numeric tokens that parse as negative zero, including `-0`,
`-0.0`, and `-0e0`, are rejected before canonicalization.
The exact argument schemas remain `read`, `write`, `edit`, and `bash`. Read
`offset`/`limit` and bash `timeout` match Pi's `Type.Number` schema, so they are
not restricted to integer tokens. Bash timeout separately mirrors the pinned
Pi v0.87.1 execution range: it must be finite, greater than zero, and no more
than 2,147,483.647 seconds, as in the pinned
[`bash.ts` source](https://github.com/earendil-works/pi/blob/v0.87.1/packages/agent/src/harness/tools/bash.ts).
The executor must still perform its own validation.

`CanonicalArguments` returns canonical argument bytes;
`CanonicalArgumentsDigest` returns
`tbound-args-jcs-rfc8785/v1:sha256:` followed by lowercase SHA-256 hex. Tests
include independent RFC 8785 primitive/number and UTF-16 property-ordering
vectors, HTML and U+2028/U+2029 string output, negative-zero rejection, Pi
number-schema behavior, and raw capture/proposal parity.

This package defines and tests a local boundary contract. It does not parse or
authenticate a real provider exchange, create provider requests, transport IPC,
execute tools, or establish Pi closure. The broker-owned raw capture field is
the required integration input; no provider-specific raw-capture fixture or
cross-implementation provider-decoder parity test exists. No provider
interception or WP1/G1 evidence is present.
