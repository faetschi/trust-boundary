# Audit journal prototype

On Linux, `Open` obtains a nonblocking exclusive advisory `flock` held for the
handle lifetime. It walks the path through directory handles and rejects
symlinks. Every ancestor must be owned by root or the supervisor effective UID
and must not be group- or world-writable. The selected parent must be
supervisor-owned with mode `0700`; the journal must be supervisor-owned with
mode `0600`. This rejects `/tmp`-style ancestry. It verifies the existing JSONL
trace and synchronizes the journal file and its parent directory before
returning a writer. A second
`Open` of the same file fails while the lock is held. `Close` releases it, and
the OS releases it if the process exits. Other platforms fail `Open` closed
because this slice has no qualified lock implementation there. `Append` writes and syncs one
non-effect event. `RunEffect` syncs an intent before invoking its callback, then
syncs the callback outcome before returning successful result bytes. The
callback runs under the journal writer lock and must not call back into the same
journal. A write or sync error poisons the in-process writer. Intent failure
skips the callback; outcome failure withholds the result and reports an unknown
outcome.

`Verify` checks canonical frames, sequence numbers, the predecessor hash chain,
and intent/outcome pairing, then reconstructs complete and unresolved effects.
Any unresolved effect may or may not have run and quarantines every later
effect ID, both in-process and after reopen. A callback panic or an outcome
record that cannot be written also leaves the attempt unresolved. A callback
error attempts a durable `unknown` record and leaves the journal quarantined
whether or not that record can be synced. Recovery requires external
reconciliation before retry; this slice has no reconciliation API.

This prototype has an 8 MiB frame limit and expects one active writer. Its
evidence is limited to observed writes, successful file/directory `Sync` calls,
and the OS advisory lock result. The hash chain does not prevent rewriting the
entire file. The Linux filesystem, mount, storage, and power-loss behavior
remain unqualified; this package does not satisfy the WP1/G1 durability gate by
itself.
