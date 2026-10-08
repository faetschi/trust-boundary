// Package privategit creates an independent, supervisor-owned session tree
// from a verified workspace descriptor and initializes a new private Git
// administration directory in that tree.
//
// This package is deliberately an internal staging primitive. It is not a
// model-visible Git tool, does not clone a repository, and does not wire a
// runtime or command-cell integration. The Linux implementation delegates the
// ordinary filtered byte copy and manifest verification to workspace.Import;
// the Git-specific checks here reject live administration metadata and verify
// the newly-created administration directory.
package privategit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sync"

	"tbound/supervisor/internal/delta"
)

var (
	ErrInvalidOptions      = errors.New("invalid private Git options")
	ErrUntrustedParent     = errors.New("private Git parent is not supervisor-owned and private")
	ErrSourceContainsGit   = errors.New("source contains live Git administration metadata")
	ErrGitExecutable       = errors.New("Git executable is not a pinned trusted host executable")
	ErrGitInitialization   = errors.New("private Git initialization failed")
	ErrGitMetadata         = errors.New("private Git administration metadata is unsafe")
	ErrSourceBinding       = errors.New("source descriptor lacks trusted binding evidence")
	ErrCleanupIncomplete   = errors.New("private Git cleanup was incomplete")
	ErrClosed              = errors.New("private Git repository is closed")
	ErrUnsupportedPlatform = errors.New("private Git staging is unsupported on this platform")
)

// Limits are copied into the package boundary so callers can pass a public
// descriptor without exposing workspace implementation state. Linux creation
// converts these values to workspace.Limits and therefore retains that
// package's streaming and descriptor-relative checks.
type Limits struct {
	MaxObjects    int
	MaxDepth      int
	MaxFileBytes  int64
	MaxTotalBytes int64
}

// DefaultLimits returns the bounded staging profile used by workspace.Import.
func DefaultLimits() Limits {
	return Limits{
		MaxObjects:    delta.MaxObjectsPerManifest,
		MaxDepth:      256,
		MaxFileBytes:  256 << 20,
		MaxTotalBytes: 1 << 30,
	}
}

// XattrVisibilityAttestation is the host-profile assertion required before a
// workspace scan can be treated as complete. It is intentionally not inferred
// from a successful listxattr call.
type XattrVisibilityAttestation struct {
	ProfileDigest string
	Complete      bool
}

// SourceDescriptor is a supervisor-owned, already-open source descriptor and
// the policy identity under which it is captured. Binding must be authenticated
// by the trusted source owner; the shape fields alone are not source evidence.
// Root is consumed only as a descriptor; its pathname is never passed to Git.
type SourceDescriptor struct {
	Root                 *os.File
	Generation           string
	MetadataPolicyDigest string
	BindingDigest        string
	Limits               Limits
	Quiescent            bool
	XattrVisibility      XattrVisibilityAttestation
	Binding              SourceBindingVerifier
}

// SourceBindingVerifier is supplied by the trusted session/source owner. It
// must authenticate that root is the exact sealed source descriptor bound to
// generation, metadataPolicyDigest, xattrProfileDigest, and bindingDigest.
// The privategit package cannot infer that provenance from a bool or pathname.
type SourceBindingVerifier interface {
	VerifySource(root *os.File, generation, metadataPolicyDigest, xattrProfileDigest, bindingDigest string) error
}

// CleanupBoundary records the trusted host assumption needed for the final
// parent/name unlink. Linux has no unlink-by-inode primitive; callers must
// establish exclusive ownership of the private parent namespace and the
// created child tree for the lifetime of Create/Destroy. Description is
// retained to make that assumption explicit in integration evidence.
type CleanupBoundary struct {
	ExclusiveParent bool
	Description     string
}

// Options selects the pinned host Git executable, deterministic branch name,
// and trusted exclusive-parent cleanup boundary. The package does not accept
// command arguments, environment overrides, Git directories, templates, hooks,
// or worktree paths from this type.
type Options struct {
	Git             PinnedGit
	InitialBranch   string
	CleanupBoundary CleanupBoundary
}

// PinnedGit is constructed once by PinGitExecutable. It holds the approved
// executable descriptor; callers cannot replace the command or append
// arbitrary arguments for a single operation.
type PinnedGit struct {
	path     string
	file     *os.File
	identity executableIdentity
	verify   GitExecutableVerifier
	boundary string
}

// Close releases the held approved executable descriptor. A pinned executable
// must remain open until all Git operations using it have completed.
func (p *PinnedGit) Close() error {
	if p == nil || p.file == nil {
		return nil
	}
	err := p.file.Close()
	p.file = nil
	return err
}

// GitExecutableIdentity is measured from the held executable descriptor.
// Digest is the content identity approved by the trusted host verifier.
type GitExecutableIdentity struct {
	Device    uint64
	Inode     uint64
	Size      int64
	Mode      uint32
	MtimeSec  int64
	MtimeNsec int64
	Digest    string
}

// GitExecutableApproval is an explicit dependency on the trusted host's
// expected Git identity. A same-UID executable that merely prints a Git-like
// version is not accepted unless the verifier approves its held bytes.
type GitExecutableApproval struct {
	ExpectedDigest string
	Verifier       GitExecutableVerifier
	Boundary       string
}

// GitExecutableVerifier authenticates the expected host executable and its
// execution boundary. It must be implemented by trusted host code, not by a
// model-controlled worker. PinGitExecutable still rechecks the approved
// descriptor before each invocation.
type GitExecutableVerifier func(GitExecutableIdentity) error

type executableIdentity struct {
	Device    uint64
	Inode     uint64
	Size      int64
	Mode      uint32
	MtimeSec  int64
	MtimeNsec int64
	Digest    string
}

type nodeIdentity struct {
	Device uint64
	Inode  uint64
}

// Snapshot is the verified source manifest copied into the private tree.
// TreeDigest commits to the canonical manifest and its metadata-policy
// identity; no Git history is imported.
type Snapshot struct {
	Manifest   delta.TreeManifest
	TreeDigest string
	Objects    int
	Bytes      int64
}

// Provenance is deterministic evidence for one private repository creation.
// It contains no source pathname and therefore cannot itself create a live
// workspace reference.
type Provenance struct {
	SchemaVersion        string             `json:"schema_version"`
	Transfer             string             `json:"transfer"`
	FilterPolicy         string             `json:"filter_policy"`
	SourceGeneration     string             `json:"source_generation"`
	SourceTreeDigest     string             `json:"source_tree_digest"`
	SourceManifest       delta.TreeManifest `json:"source_manifest"`
	MetadataPolicyDigest string             `json:"metadata_policy_digest"`
	SourceBindingDigest  string             `json:"source_binding_digest"`
	GitVersion           string             `json:"git_version"`
	GitExecutableDigest  string             `json:"git_executable_digest"`
	GitExecutionBoundary string             `json:"git_execution_boundary"`
	InitialBranch        string             `json:"initial_branch"`
	SourceRootDevice     uint64             `json:"source_root_device"`
	SourceRootInode      uint64             `json:"source_root_inode"`
	PrivateRootDevice    uint64             `json:"private_root_device"`
	PrivateRootInode     uint64             `json:"private_root_inode"`
	PrivateGitDevice     uint64             `json:"private_git_device"`
	PrivateGitInode      uint64             `json:"private_git_inode"`
	XattrProfileDigest   string             `json:"xattr_profile_digest"`
	CaptureLimits        Limits             `json:"capture_limits"`
}

// Digest returns the content digest of the canonical JSON provenance record.
func (p Provenance) Digest() string {
	encoded, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// Repository is a private, newly initialized repository. Root returns a
// borrowed descriptor for trusted supervisor integration; it is never a path
// or a model-visible Git handle. Callers must not close it independently.
type Repository struct {
	mu         sync.Mutex
	root       *os.File
	parent     *os.File
	name       string
	snapshot   Snapshot
	provenance Provenance
	identity   nodeIdentity
	gitAdmin   nodeIdentity
	limits     Limits
	xattr      XattrVisibilityAttestation
	boundary   CleanupBoundary
	closed     bool
	destroyed  bool
}

// Root returns the borrowed descriptor for the private worktree.
func (r *Repository) Root() *os.File {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.destroyed {
		return nil
	}
	return r.root
}

// Snapshot returns a defensive copy of the verified source snapshot.
func (r *Repository) Snapshot() Snapshot {
	if r == nil {
		return Snapshot{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneSnapshot(r.snapshot)
}

// Provenance returns a defensive copy of the creation provenance.
func (r *Repository) Provenance() Provenance {
	if r == nil {
		return Provenance{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.provenance
	p.SourceManifest.Objects = append([]delta.ManifestObject(nil), p.SourceManifest.Objects...)
	return p
}

// Verify re-checks the private Git administration boundary. It is useful to
// an integration owner after descriptor transfer; it does not assert process
// containment or prove that a consumer observed the repository.
func (r *Repository) Verify() error {
	if r == nil {
		return ErrClosed
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.destroyed {
		return ErrClosed
	}
	return verifyRepositoryLocked(r)
}

// Close releases descriptors without deleting the repository. Destroy is the
// explicit bounded owner-controlled cleanup operation.
func (r *Repository) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	var closeErr error
	if r.root != nil {
		closeErr = errors.Join(closeErr, r.root.Close())
	}
	if r.parent != nil {
		closeErr = errors.Join(closeErr, r.parent.Close())
	}
	return closeErr
}

// Destroy performs bounded descriptor-relative cleanup of the one directory
// created by this Repository. It never removes the parent or follows links.
func (r *Repository) Destroy() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.destroyed {
		return nil
	}
	if r.closed {
		return ErrClosed
	}
	if err := destroyRepositoryLocked(r); err != nil {
		return err
	}
	r.destroyed = true
	r.closed = true
	return nil
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	snapshot.Manifest.Objects = append([]delta.ManifestObject(nil), snapshot.Manifest.Objects...)
	return snapshot
}
