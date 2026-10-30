package drainproof

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"tbound/supervisor/internal/workflowrecovery"
)

// ErrNotDrained reports that the owner cannot be proven drained. Every refusal
// that is not an unknown outcome wraps this sentinel.
var ErrNotDrained = errors.New("drainproof: owner not proven drained")

// ErrUnknown reports that the drain outcome cannot be determined, for example
// because a populated scope did not empty before its deadline (IC-08). Callers
// must not start new effects on this outcome.
var ErrUnknown = errors.New("drainproof: owner drain state unknown")

// ScopeObservation is what the host observed about the owner's cleanup scope.
type ScopeObservation struct {
	Absent         bool
	Populated      bool
	ProcCount      int
	IdentityDigest string
	// NeverBound marks an authenticated durable never-exposed receipt: the owner
	// was cleaned up before any worker scope was created, so no scope identity
	// exists. It requires Absent and an empty identity, receipt and expectation,
	// and it is digested, so it can never be confused with a bound-scope receipt.
	NeverBound bool
	// WorkflowID and ScopeName identify the workflow and the owner scope name that
	// a NeverBound receipt was issued for. A never-bound receipt has no scope
	// identity to make its digest unique, so these two take that role. Both are
	// required with NeverBound.
	WorkflowID string
	ScopeName  string
	RemoveErr  error
}

// OwnerObservation is one owner's observed facts, compared against the
// recorded identity it was acquired under.
type OwnerObservation struct {
	Owner                     workflowrecovery.OwnerEpochIdentity
	BootID                    string
	ObservedBootID            string
	StartTicks                uint64
	ObservedStartTicks        uint64
	ProcessLive               bool
	Scope                     ScopeObservation
	CleanupReceiptIdentity    string
	ExpectedScopeIdentity     string
	PendingEffects            int
	PopulatedDeadlineExceeded bool
}

// Proof is a successful drain decision. Digest commits to the observed facts.
type Proof struct {
	Digest string
	Basis  string
}

// digestLabel separates this digest from any other SHA-256 over similar bytes. It
// names the encoding shape. v3 adds the scope's WorkflowID and ScopeName to the v2
// shape, so a v2 digest can never be read as a v3 one.
const digestLabel = "tbound/supervisor/drainproof/observation/v3"

// Prove decides whether obs proves the owner drained. It is fail-closed: any
// fact that is missing, inconsistent, or unexpected yields an error, and the
// returned Proof is then the zero value.
func Prove(obs OwnerObservation) (Proof, error) {
	if err := validateOwner(obs); err != nil {
		return Proof{}, err
	}
	liveness, err := proveNotLive(obs)
	if err != nil {
		return Proof{}, err
	}
	if obs.PopulatedDeadlineExceeded {
		return Proof{}, fmt.Errorf("%w: scope did not empty before its deadline", ErrUnknown)
	}
	scope, err := proveScope(obs)
	if err != nil {
		return Proof{}, err
	}
	if obs.PendingEffects != 0 {
		return Proof{}, notDrainedf("%d pending effects", obs.PendingEffects)
	}
	return Proof{
		Digest: observationDigest(obs),
		Basis:  liveness + ";" + scope + ";pending=0",
	}, nil
}

// ProveAll proves every observation or returns the first refusal, wrapped with
// the owner's index. An empty slice is refused, because a drain proof over no
// owners would be vacuous.
func ProveAll(obs []OwnerObservation) ([]Proof, error) {
	if len(obs) == 0 {
		return nil, notDrainedf("no owners to prove drained")
	}
	proofs := make([]Proof, 0, len(obs))
	for i, o := range obs {
		p, err := Prove(o)
		if err != nil {
			return nil, fmt.Errorf("owner %d: %w", i, err)
		}
		proofs = append(proofs, p)
	}
	return proofs, nil
}

func notDrainedf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrNotDrained, fmt.Sprintf(format, args...))
}

// validateOwner requires a usable recorded identity, and requires the
// observation's recorded boot id and start ticks to agree with the owner
// incarnation. A disagreement means the recorded identity is inconsistent.
// PIDNamespaceInode and EffectiveUID are not required here: UID 0 is a valid
// root owner, and namespace validation belongs to the host boundary.
func validateOwner(obs OwnerObservation) error {
	id := obs.Owner
	switch {
	case id.Epoch == 0:
		return notDrainedf("owner epoch is zero")
	case id.Incarnation.Digest == "":
		return notDrainedf("owner incarnation digest is empty")
	case id.Incarnation.BootID == "":
		return notDrainedf("owner incarnation boot id is empty")
	case id.Incarnation.PID <= 0:
		return notDrainedf("owner incarnation pid is not positive")
	case id.Incarnation.StartTimeTicks == 0:
		return notDrainedf("owner incarnation start ticks are zero")
	case id.Incarnation.CgroupIdentityDigest == "":
		return notDrainedf("owner incarnation cgroup identity digest is empty")
	case obs.BootID == "":
		return notDrainedf("recorded boot id is empty")
	case obs.BootID != id.Incarnation.BootID:
		return notDrainedf("recorded boot id disagrees with owner incarnation")
	case obs.StartTicks != id.Incarnation.StartTimeTicks:
		return notDrainedf("recorded start ticks disagree with owner incarnation")
	}
	return nil
}

// proveNotLive returns a basis when the recorded owner process cannot be live.
// A live process counts only when its observed boot id and start ticks both
// match the recorded ones. A mismatch means PID reuse or a reboot, so the
// original process is gone. A live observation without a complete observed
// identity is refused, because it cannot be attributed to the owner.
func proveNotLive(obs OwnerObservation) (string, error) {
	if !obs.ProcessLive {
		return "owner-process-not-live", nil
	}
	if obs.ObservedBootID == "" || obs.ObservedStartTicks == 0 {
		return "", notDrainedf("live process observed without complete identity")
	}
	if obs.ObservedBootID != obs.BootID || obs.ObservedStartTicks != obs.StartTicks {
		return "recorded-process-gone-pid-reused-or-rebooted", nil
	}
	return "", notDrainedf("owner process live on recorded boot and start ticks")
}

// proveScope requires the cleanup scope to be empty. An absent scope must also
// carry a cleanup receipt equal to the expected scope identity. A present scope
// must have been observed: its IdentityDigest must be non-empty and must equal
// the owner's cgroup identity, so a foreign scope is refused. When an expected
// scope identity is set, the observed identity must equal it too.
func proveScope(obs OwnerObservation) (string, error) {
	s := obs.Scope
	if s.NeverBound {
		// No scope ever existed, so there is no identity to receipt or expect. Any
		// identity here would make this a bound-scope observation, so it is refused.
		// The workflow and scope name are required instead: they are what makes the
		// receipt, and so its digest, specific to one workflow and epoch.
		if !s.Absent || s.Populated || s.ProcCount != 0 || s.RemoveErr != nil || s.IdentityDigest != "" ||
			obs.CleanupReceiptIdentity != "" || obs.ExpectedScopeIdentity != "" ||
			s.WorkflowID == "" || s.ScopeName == "" {
			return "", notDrainedf("never-bound scope observation is inconsistent")
		}
		return "scope-never-bound-receipt", nil
	}
	if s.Absent {
		if s.Populated || s.ProcCount != 0 || s.RemoveErr != nil {
			return "", notDrainedf("scope reported absent but also populated, counted, or with a remove error")
		}
		if obs.CleanupReceiptIdentity == "" {
			return "", notDrainedf("scope absent but no cleanup receipt")
		}
		if obs.CleanupReceiptIdentity != obs.ExpectedScopeIdentity {
			return "", notDrainedf("cleanup receipt does not match expected scope identity")
		}
		return "scope-absent-receipt-matched", nil
	}
	if s.Populated || s.ProcCount != 0 {
		return "", notDrainedf("scope populated or processes present")
	}
	if s.RemoveErr != nil {
		return "", notDrainedf("scope removal failed")
	}
	if s.IdentityDigest == "" {
		return "", notDrainedf("present scope has no observed identity")
	}
	// validateOwner has already required a non-empty cgroup identity digest.
	if s.IdentityDigest != obs.Owner.Incarnation.CgroupIdentityDigest {
		return "", notDrainedf("observed scope identity is not the owner's cgroup identity")
	}
	if obs.ExpectedScopeIdentity != "" && s.IdentityDigest != obs.ExpectedScopeIdentity {
		return "", notDrainedf("observed scope identity does not match expected scope identity")
	}
	return "scope-empty-removed", nil
}

// observationDigest commits to every fact the decision reads, including the
// caller's ExpectedScopeIdentity. Two proofs that differ only in the expectation
// therefore have different digests.
func observationDigest(obs OwnerObservation) string {
	var e encoder
	e.str(digestLabel)

	id := obs.Owner
	e.u64(id.Epoch)
	e.str(id.Incarnation.Digest)
	e.str(id.Incarnation.BootID)
	e.i64(int64(id.Incarnation.PID))
	e.u64(id.Incarnation.StartTimeTicks)
	e.u64(id.Incarnation.PIDNamespaceInode)
	e.u64(uint64(id.Incarnation.EffectiveUID))
	e.str(id.Incarnation.CgroupIdentityDigest)

	e.str(obs.BootID)
	e.str(obs.ObservedBootID)
	e.u64(obs.StartTicks)
	e.u64(obs.ObservedStartTicks)
	e.boolean(obs.ProcessLive)

	e.boolean(obs.Scope.Absent)
	e.boolean(obs.Scope.Populated)
	e.boolean(obs.Scope.NeverBound)
	e.str(obs.Scope.WorkflowID)
	e.str(obs.Scope.ScopeName)
	e.i64(int64(obs.Scope.ProcCount))
	e.str(obs.Scope.IdentityDigest)
	e.optionalErr(obs.Scope.RemoveErr)

	e.str(obs.CleanupReceiptIdentity)
	e.str(obs.ExpectedScopeIdentity)
	e.i64(int64(obs.PendingEffects))
	e.boolean(obs.PopulatedDeadlineExceeded)

	sum := sha256.Sum256(e.buf)
	return hex.EncodeToString(sum[:])
}

// encoder writes a canonical byte form. Integers are fixed-width big-endian,
// booleans are one byte, and strings are length-prefixed, so no two distinct
// field sequences share an encoding.
type encoder struct {
	buf []byte
}

func (e *encoder) u64(v uint64) {
	e.buf = binary.BigEndian.AppendUint64(e.buf, v)
}

func (e *encoder) i64(v int64) {
	e.u64(uint64(v))
}

func (e *encoder) boolean(v bool) {
	if v {
		e.buf = append(e.buf, 1)
		return
	}
	e.buf = append(e.buf, 0)
}

func (e *encoder) str(s string) {
	e.u64(uint64(len(s)))
	e.buf = append(e.buf, s...)
}

// optionalErr records presence, then the error text when present.
func (e *encoder) optionalErr(err error) {
	if err == nil {
		e.boolean(false)
		return
	}
	e.boolean(true)
	e.str(err.Error())
}
