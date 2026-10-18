//go:build !linux

package workflowrecovery

import (
	"context"
	"time"
)

// TrustedRoot has the same required host-boundary contract on every platform.
type TrustedRoot interface {
	TrustedRootIdentityDigest() string
	VerifyHostBoundary(ctx context.Context, workflow string, store OwnerStoreIdentity, incarnation OwnerIncarnation) error
	VerifyWorkflowDeadline(ctx context.Context, workflow string, deadline time.Time) error
	VerifyFence(ctx context.Context, workflow string, ownerEpoch uint64) error
	AcquireOwnerEpoch(ctx context.Context, workflow string, previousEpoch, nextEpoch uint64, incarnation OwnerIncarnation) error
	VerifyOwnersDrained(ctx context.Context, workflow string, afterEpoch, beforeEpoch uint64) error
	VerifyCheckpoint(ctx context.Context, workflow string, ownerEpoch uint64, digest, ownerIncarnationDigest string) error
	AuthorizeFreshWorkflow(ctx context.Context, workflow string, ownerEpoch uint64, initialDigest string,
		incarnation OwnerIncarnation, deadline time.Time) error
	CompareAndSwapCheckpoint(ctx context.Context, workflow string, expectedOwnerEpoch uint64, expectedDigest string,
		nextOwnerEpoch uint64, nextDigest string, incarnation OwnerIncarnation) error
}

// SQLiteOwner is the fail-closed non-Linux placeholder.
type SQLiteOwner struct{}

var errSQLiteUnsupported = ErrOwnerStoreUnsupported

func OpenExistingSQLiteOwner(string, TrustedRoot) (*SQLiteOwner, error) {
	return nil, errSQLiteUnsupported
}
func CreateFreshSQLiteOwner(string, TrustedRoot) (*SQLiteOwner, error) {
	return nil, errSQLiteUnsupported
}
func (*SQLiteOwner) TrustedRootIdentityDigest() string  { return "" }
func (*SQLiteOwner) OwnerIncarnationDigest() string     { return "" }
func (*SQLiteOwner) OwnerIncarnation() OwnerIncarnation { return OwnerIncarnation{} }
func (*SQLiteOwner) OwnerStoreIdentity(context.Context, string) (OwnerStoreIdentity, error) {
	return OwnerStoreIdentity{}, errSQLiteUnsupported
}
func (*SQLiteOwner) NextOwnerEpoch(context.Context, string) (uint64, error) {
	return 0, errSQLiteUnsupported
}
func (*SQLiteOwner) VerifyFence(context.Context, string, uint64) error { return errSQLiteUnsupported }
func (*SQLiteOwner) VerifyTerminalEffectOwner(context.Context, string, uint64, string, time.Time) error {
	return errSQLiteUnsupported
}
func (*SQLiteOwner) AcquireOwnerEpoch(context.Context, string, uint64, uint64) error {
	return errSQLiteUnsupported
}
func (*SQLiteOwner) VerifyOwnersDrained(context.Context, string, uint64, uint64) error {
	return errSQLiteUnsupported
}
func (*SQLiteOwner) InitializeFreshCheckpoint(context.Context, string, uint64, Checkpoint) (string, error) {
	return "", errSQLiteUnsupported
}
func (*SQLiteOwner) LoadCheckpoint(context.Context, string) (Checkpoint, string, bool, error) {
	return Checkpoint{}, "", false, errSQLiteUnsupported
}
func (*SQLiteOwner) CompareAndSwapCheckpoint(context.Context, string, uint64, string, Checkpoint, TransitionProof) (string, error) {
	return "", errSQLiteUnsupported
}
func (*SQLiteOwner) Close() error { return nil }
