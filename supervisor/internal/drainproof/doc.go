// Package drainproof decides whether a previously acquired host owner is
// drained, so that a higher epoch may be bound. It is a pure, fail-closed
// decision function: it performs no I/O, reads no clock, and starts no
// goroutines. It is additive. It is not wired into workflowrecovery, and it
// does not change any existing digest or fence.
//
// Assumptions. Each one is conservative. Where the decision is uncertain, the
// result is a refusal, never a proof.
//
//  1. Owner identity. An owner is valid only when Epoch > 0, the incarnation
//     Digest, BootID and CgroupIdentityDigest are non-empty, PID > 0 and
//     StartTimeTicks > 0, the observation's BootID is non-empty, and the
//     observation's BootID and StartTicks equal the owner incarnation's BootID
//     and StartTimeTicks. Otherwise the result is ErrNotDrained. PIDNamespaceInode
//     and EffectiveUID are not required here. UID 0 is a valid root owner, and
//     namespace validation belongs to the host boundary.
//
//  2. Liveness. ProcessLive is the host's observation of the recorded process.
//     The process counts as live only if ProcessLive is true, ObservedBootID
//     equals BootID, and ObservedStartTicks equals StartTicks. A differing boot
//     id or start ticks means PID reuse or a reboot, so the original process is
//     gone. A live observation whose observed identity is incomplete (empty
//     ObservedBootID or zero ObservedStartTicks) cannot be attributed to the
//     owner, so it is refused with ErrNotDrained.
//
//  3. Deadline and IC-08 taxonomy. PopulatedDeadlineExceeded yields ErrUnknown
//     whenever it is set, whatever the scope facts say. A timed-out wait is not
//     evidence of a drain, and an unknown outcome means no new effects (IC-08).
//     The check runs after the liveness check, so a live process is still
//     reported as ErrNotDrained. ErrUnknown is returned for the deadline flag only.
//     Other unfinished states are refusals (ErrNotDrained), not unknown outcomes.
//     These are a populated or counted scope before its deadline, a remove error,
//     and a live recorded process. Each one proves the owner is not drained. None
//     of them shows the outcome is unknown. The specification's "otherwise retain
//     UNKNOWN" is implemented for the deadline case only. This is a documented
//     reading and is not changed here.
//
//  4. Absent scope. Scope.Absent requires that the scope is not populated, has
//     no processes, and has no remove error. It also requires a non-empty
//     CleanupReceiptIdentity equal to ExpectedScopeIdentity. An absent scope
//     with no receipt is refused. This is OPEN spec question Q1.
//
//  5. Present scope. When Scope.Absent is false, the scope must be unpopulated,
//     have ProcCount == 0 (a negative count is refused), and have no remove
//     error. The scope must also have been observed, so Scope.IdentityDigest
//     must be non-empty. A zero-value ScopeObservation is therefore refused.
//     Scope.IdentityDigest must equal the owner's CgroupIdentityDigest. A foreign
//     scope is refused, and the cgroup digest is required by item 1, so this
//     check always applies. When ExpectedScopeIdentity is non-empty,
//     Scope.IdentityDigest must also equal it. Otherwise the result is
//     ErrNotDrained. When ExpectedScopeIdentity is empty, that equality check is
//     skipped, and only the observation and the cgroup binding are required. The
//     empty-expectation case is OPEN question Q4. The receipt does not enter the
//     decision on this path, and it is still digested.
//
//  6. Pending effects. Any PendingEffects other than zero is refused. This is an
//     extra refusal, because the spec does not say whether pending effects
//     belong to this gate or to a separate ledger gate (OPEN question Q2).
//
//  7. Errors. Refusals wrap ErrNotDrained, and unknown outcomes wrap ErrUnknown.
//     Invalid owners and an empty ProveAll input are refusals, because a drain
//     cannot be established. Error text is for humans and is not a stable API.
//     The zero Proof is returned with every error.
//
//  8. ProveAll. The result is all proofs in input order, or the first refusal
//     wrapped with the owner's index. There are no partial results. An empty
//     slice is a refusal, because a drain proof over no owners would be vacuous.
//
//  9. Digest. Proof.Digest is the lowercase hex SHA-256 of a canonical encoding
//     of the observed facts. The encoding starts with the domain label
//     "tbound/supervisor/drainproof/observation/v1". Integers are fixed-width
//     big-endian, booleans are one byte, and strings are length-prefixed. The
//     digest covers every Owner field, both boot ids, both start ticks, the
//     liveness flag, every Scope field, the receipt identity, the expected scope identity, the pending count,
//     and the deadline flag. A RemoveErr is recorded as presence plus its message
//     text, so two errors with the same text are indistinguishable. Basis is
//     derived and is not digested. ExpectedScopeIdentity is digested, so two
//     proofs that differ only in the expectation have different digests.
package drainproof
