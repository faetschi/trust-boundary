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
//     Digest and BootID are non-empty, the observation's BootID is non-empty,
//     and the observation's BootID and StartTicks equal the owner incarnation's
//     BootID and StartTimeTicks. Otherwise the result is ErrNotDrained.
//
//  2. Liveness. ProcessLive is the host's observation of the recorded process.
//     The process counts as live only if ProcessLive is true, ObservedBootID
//     equals BootID, and ObservedStartTicks equals StartTicks. A differing boot
//     id or start ticks means PID reuse or a reboot, so the original process is
//     gone. A live observation whose observed identity is incomplete (empty
//     ObservedBootID or zero ObservedStartTicks) cannot be attributed to the
//     owner, so it is refused with ErrNotDrained.
//
//  3. Deadline. PopulatedDeadlineExceeded yields ErrUnknown whenever it is set,
//     whatever the scope facts say. A timed-out wait is not evidence of a drain,
//     and an unknown outcome means no new effects (IC-08). The check runs after
//     the liveness check, so a live process is still reported as ErrNotDrained.
//
//  4. Absent scope. Scope.Absent requires that the scope is not populated, has
//     no processes, and has no remove error. It also requires a non-empty
//     CleanupReceiptIdentity equal to ExpectedScopeIdentity. An absent scope
//     with no receipt is refused. This is OPEN spec question Q1.
//
//  5. Present scope. When Scope.Absent is false, the scope must be unpopulated,
//     have ProcCount == 0 (a negative count is refused), and have no remove
//     error. The receipt and Scope.IdentityDigest do not enter the decision on
//     this path. They are still digested. ExpectedScopeIdentity is not compared
//     with Scope.IdentityDigest (OPEN question Q3).
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
//     liveness flag, every Scope field, the receipt identity, the pending count,
//     and the deadline flag. A RemoveErr is recorded as presence plus its message
//     text, so two errors with the same text are indistinguishable. Basis is
//     derived and is not digested. ExpectedScopeIdentity is not digested. On an
//     accepted proof the receipt equals it, so the receipt already commits to it.
package drainproof
