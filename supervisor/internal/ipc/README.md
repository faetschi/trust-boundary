# Session IPC

`ipc` carries one supervisor-session's proposals and results over a bounded
stream. `NewPipe` uses `net.Pipe` for portable tests; Linux builds also expose
`ListenUnix`/`DialUnix` for a Unix-domain socket. The listener enforces a
supervisor-owned mode-0700 parent, creates a mode-0600 socket, and accepts only
one connection for the session. A binding token prevents accidental or
cross-session frame mixing; it is **not** peer authentication. Peer credentials,
cgroup/pidfd membership, deadlines, durable audit, and lifecycle/admin channels
remain separate work.

## Wire format and bounds

Each message is a strict UTF-8 JSON object preceded by an unsigned 4-byte
big-endian byte length. The length excludes the prefix and must be in
`[1, 1,048,576]`; therefore a complete wire frame is at most `1,048,580`
bytes. The implementation reads the prefix first, rejects zero/oversized
lengths before allocation. Every wire buffer is capped by that limit; the
implementation does not use read-all/growing buffers or pipeline messages.

The envelope has exactly these fields (no duplicate or unknown fields):

```json
{
  "version": "tbound-ipc/v1",
  "binding_token": "<64 lowercase hex characters>",
  "sequence": 1,
  "kind": "proposal",
  "payload": {
    "schema_version": "tbound-proposal/v1",
    "tool_call_id": "call-1",
    "tool": "read",
    "arguments": {"path": "README.md"}
  }
}
```

`kind` is exactly `proposal` client-to-supervisor or `result`
supervisor-to-client. The payload reuses `broker/protocol`'s strict proposal
shape; result payloads use its bounded result schema. Each direction starts at
sequence 1 and must increase by exactly one, so duplicate, stale, skipped, or
replayed frames close the endpoint. Every frame's token must match the
supervisor-created 256-bit session token. JSON duplicate keys (including nested
payloads), unknown fields/kinds, invalid UTF-8/numbers, malformed lengths,
truncation, wrong direction, and token mismatches fail closed.

Each direction is limited to 1,024 frames and 16 MiB of encoded JSON across the
connection, in addition to the 1 MiB per-message cap. Clean EOF between frames
is reported as `ErrClosed`; partial prefixes and payloads are malformed frames.
Only a valid proposal/result is returned to the caller.

The token and sequence are framing/session-binding controls, not authority.
Every proposal still requires a trusted broker-correlation match and a gate
decision. This prototype transport does not claim authenticated peer identity,
deadline enforcement, durable evidence, or resistance to a compromised host.
