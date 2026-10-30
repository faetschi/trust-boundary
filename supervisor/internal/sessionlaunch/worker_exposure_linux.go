//go:build linux

package sessionlaunch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/gowebpki/jcs"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/sessionrepo"
)

// ErrD09ExposureSource is returned when a prepared session cannot issue or
// revalidate a D09-only sealed SOURCE capability. It does not mean that a
// consumer-side read-only mount has been established; only the trusted D09
// Podman/crun launcher and in-cell entrypoint can prove that second phase.
var ErrD09ExposureSource = errors.New("verified D09 sealed source is unavailable or unverified")

// VerifiedD09ExposureSource is the sessionlaunch wrapper over the concrete
// sessionrepo-issued source capability. It binds that exact Store generation
// source to the durable D06 source-binding/private-Git provenance and retains
// the PreparedSession/private-Git owner for revalidation.
//
// The exported descriptor is SOURCE material for trusted D09 staging only. Its
// fstat/fdinfo fields identify the host source; they are deliberately not a
// claim about a read-only consumer mount. A must bind it read-only into the
// actual container namespace, verify the consumer inode/dev/mount/readonly
// state, replace and close the raw source FD before starting Node, and admit no
// worker/prompt if its independent consumer proof is missing or mismatched.
//
// This type is opaque: fields are package-private and it can only be produced
// from a live PreparedSession. It deliberately does not implement
// piruntime.VerifiedWorkerExposure, whose semantics require an already-verified
// consumer read-only view. A consumes the source-specific D09 methods below.
type VerifiedD09ExposureSource struct {
	mu         sync.Mutex
	owner      *PreparedSession
	store      *sessionrepo.Store
	journal    *audit.Journal
	generation *sessionrepo.Generation
	git        privateGitHandle
	profile    SourceProfile
	provenance Provenance
	capability *sessionrepo.VerifiedSealedSource
	source     sessionrepo.SealedSourceEvidence
	exported   bool
	closed     bool
}

// OpenVerifiedD09ExposureSource is the production source-capability constructor.
// The generation argument is not a path or an authority assertion: it must be
// the exact retained Generation object at the PreparedSession Store's currently
// verified tip. The method re-verifies the Store chain/bundle, complete selected
// manifest/tree, exact journal bytes, D06 source-binding/private-Git-ready
// records, private-Git provenance and private admin tree before asking the Store
// to issue its opaque O_PATH source capability. No Binder or host mount syscall
// is used.
func (p *PreparedSession) OpenVerifiedD09ExposureSource(ctx context.Context,
	generation *sessionrepo.Generation) (*VerifiedD09ExposureSource, error) {
	if p == nil || ctx == nil || generation == nil {
		return nil, ErrD09ExposureSource
	}
	anchors, err := p.verifyD06SourceForGeneration(ctx, generation, true)
	if err != nil {
		return nil, err
	}
	capability, err := generation.ExportSealedSource(ctx)
	if err != nil {
		return nil, fmt.Errorf("issue Store-bound sealed source capability: %w", err)
	}
	closeCapability := true
	defer func() {
		if closeCapability {
			_ = capability.Close()
		}
	}()
	sourceEvidence, err := capability.Evidence()
	if err != nil {
		return nil, errors.Join(ErrD09ExposureSource, err)
	}
	if !capability.IsForGeneration(anchors.store, generation) {
		return nil, ErrD09ExposureSource
	}
	provenanceDigest, err := canonicalDigest(anchors.provenance)
	if err != nil || sourceEvidence.GenerationID != generation.ID() ||
		sourceEvidence.SourceBindingDigest != anchors.provenance.BindingDigest ||
		sourceEvidence.ProvenanceDigest != provenanceDigest ||
		sourceEvidence.TreeDigest != DigestBytes([]byte(generation.TreeDigest())) {
		return nil, errors.Join(ErrD09ExposureSource, err)
	}
	// Recheck the private Git/source owner after capability issuance so a stale or
	// concurrently-closing PreparedSession cannot return a partially bound cap.
	if _, err := p.verifyD06SourceForGeneration(ctx, generation, true); err != nil {
		return nil, err
	}
	if err := capability.Revalidate(ctx, sourceEvidence); err != nil {
		return nil, errors.Join(ErrD09ExposureSource, err)
	}
	source := &VerifiedD09ExposureSource{
		owner: p, store: anchors.store, journal: anchors.journal, generation: generation,
		git: anchors.git, profile: anchors.profile, provenance: anchors.provenance,
		capability: capability, source: sourceEvidence,
	}
	p.mu.Lock()
	if p.closed || p.closing || p.store != anchors.store || p.journal != anchors.journal || p.git != anchors.git {
		p.mu.Unlock()
		return nil, ErrD09ExposureSource
	}
	if p.sources == nil {
		p.sources = make(map[*VerifiedD09ExposureSource]struct{})
	}
	p.sources[source] = struct{}{}
	p.mu.Unlock()
	closeCapability = false
	return source, nil
}

type verifiedD09SourceAnchors struct {
	store      *sessionrepo.Store
	journal    *audit.Journal
	git        privateGitHandle
	profile    SourceProfile
	provenance Provenance
}

// verifyD06SourceForGeneration checks the exact live Store and audit journal
// retained by Prepare, confirms the requested generation is the Store's current
// sealed tip (while the original D06/private-Git seed record remains in the
// verified chain), verifies every durable provenance record, and verifies the
// private Git/admin tree. It never substitutes a path or a caller boolean.
func (p *PreparedSession) verifyD06SourceForGeneration(ctx context.Context,
	generation *sessionrepo.Generation, requireCurrentTip bool) (verifiedD09SourceAnchors, error) {
	if p == nil || ctx == nil || generation == nil {
		return verifiedD09SourceAnchors{}, ErrD09ExposureSource
	}
	p.mu.Lock()
	if p.closed || p.closing || p.recoveryPending || p.store == nil || p.journal == nil || p.git == nil || generation.ID() == "" {
		p.mu.Unlock()
		return verifiedD09SourceAnchors{}, ErrD09ExposureSource
	}
	store, journal, git, profile := p.store, p.journal, p.git, p.profile
	provenance := p.provenance
	provenance.PrivateGit.SourceManifest.Objects = append([]delta.ManifestObject(nil), p.provenance.PrivateGit.SourceManifest.Objects...)
	baseGeneration := p.generation
	p.mu.Unlock()
	if !store.OwnsGeneration(generation) || baseGeneration == nil || !store.OwnsGeneration(baseGeneration) {
		return verifiedD09SourceAnchors{}, ErrD09ExposureSource
	}
	if err := ctx.Err(); err != nil {
		return verifiedD09SourceAnchors{}, errors.Join(ErrD09ExposureSource, err)
	}
	if err := git.Verify(); err != nil {
		return verifiedD09SourceAnchors{}, errors.Join(ErrD09ExposureSource,
			fmt.Errorf("verify supervisor-private Git provenance before D09 source export: %w", err))
	}
	chain, err := store.Verify()
	if err != nil {
		return verifiedD09SourceAnchors{}, errors.Join(ErrD09ExposureSource,
			fmt.Errorf("verify sealed Store chain before D09 source export: %w", err))
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		return verifiedD09SourceAnchors{}, errors.Join(ErrD09ExposureSource, err)
	}
	if !reflect.DeepEqual(chain, bundle.ValidatedChain) || !bundleContainsGeneration(bundle, generation) ||
		!bundleContainsGeneration(bundle, baseGeneration) ||
		generation.Manifest().MetadataPolicyDigest != bundle.MetadataPolicyDigest ||
		bundle.PolicyDigest != provenance.PolicyDigest || bundle.MetadataPolicyDigest != provenance.MetadataPolicyDigest {
		return verifiedD09SourceAnchors{}, fmt.Errorf("%w: requested generation is not the verified current Store tip or D06 seed", ErrD09ExposureSource)
	}
	if requireCurrentTip && validateCurrentGeneration(bundle, chain, generation) != nil {
		return verifiedD09SourceAnchors{}, fmt.Errorf("%w: source capability may only be issued for the current Store tip", ErrD09ExposureSource)
	}
	if err := validateJournalSnapshot(journal, bundle); err != nil {
		return verifiedD09SourceAnchors{}, errors.Join(ErrD09ExposureSource, err)
	}
	if err := verifyPersistedD06Records(bundle.AuditJournal, provenance); err != nil {
		return verifiedD09SourceAnchors{}, errors.Join(ErrD09ExposureSource, err)
	}
	trace, err := audit.Verify(bytes.NewReader(bundle.AuditJournal))
	if err != nil {
		return verifiedD09SourceAnchors{}, errors.Join(ErrD09ExposureSource, err)
	}
	for _, effect := range trace.Effects {
		if effect.Unresolved {
			return verifiedD09SourceAnchors{}, fmt.Errorf("%w: unresolved Store effect %q", ErrD09ExposureSource, effect.ID)
		}
	}
	if err := verifySelectedGenerationContent(store, generation, bundle, profile); err != nil {
		return verifiedD09SourceAnchors{}, errors.Join(ErrD09ExposureSource, err)
	}
	if err := git.Verify(); err != nil {
		return verifiedD09SourceAnchors{}, errors.Join(ErrD09ExposureSource,
			fmt.Errorf("private Git provenance changed during D09 source verification: %w", err))
	}
	return verifiedD09SourceAnchors{
		store: store, journal: journal, git: git, profile: profile, provenance: provenance,
	}, nil
}

func verifySelectedGenerationContent(store *sessionrepo.Store, generation *sessionrepo.Generation,
	bundle sessionrepo.EvidenceBundle, profile SourceProfile) error {
	path, err := generation.ReadOnlyMountSource()
	if err != nil {
		return err
	}
	root, err := openReadableDirAt(path)
	_ = path.Close()
	if err != nil {
		return err
	}
	stat, err := statNode(root)
	if err != nil {
		_ = root.Close()
		return err
	}
	err = verifyGenerationDescriptor(store, generation, root, stat, bundle, profile)
	closeErr := root.Close()
	return errors.Join(err, closeErr)
}

// ExportD09WorkerSource returns the one caller-owned O_PATH HOST SOURCE
// descriptor for A's trusted Podman/crun staging. This source descriptor is not
// a read-only consumer view and does not implement piruntime.VerifiedWorkerExposure.
// The D09 launcher/entrypoint must prove the actual consumer bind separately.
func (s *VerifiedD09ExposureSource) ExportD09WorkerSource(ctx context.Context) (*os.File, sessionrepo.SealedSourceEvidence, error) {
	if ctx == nil {
		return nil, sessionrepo.SealedSourceEvidence{}, ErrD09ExposureSource
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.owner == nil || s.capability == nil || s.exported {
		return nil, sessionrepo.SealedSourceEvidence{}, ErrD09ExposureSource
	}
	if err := s.verifyLocked(ctx); err != nil {
		return nil, sessionrepo.SealedSourceEvidence{}, err
	}
	descriptor, evidence, err := s.capability.DuplicateForD09(ctx)
	if err != nil {
		return nil, sessionrepo.SealedSourceEvidence{}, errors.Join(ErrD09ExposureSource, err)
	}
	if evidence != s.source {
		_ = descriptor.Close()
		return nil, sessionrepo.SealedSourceEvidence{}, ErrD09ExposureSource
	}
	if err := validateSourceDescriptorIdentity(descriptor, evidence); err != nil {
		_ = descriptor.Close()
		return nil, sessionrepo.SealedSourceEvidence{}, err
	}
	s.exported = true
	return descriptor, evidence, nil
}

// VerifyD09WorkerSource revalidates D06/private-Git provenance and the concrete
// sessionrepo issuer/source descriptor. The evidence remains host-source
// evidence; the D09 entrypoint produces a separate consumer-mount proof.
func (s *VerifiedD09ExposureSource) VerifyD09WorkerSource(ctx context.Context, evidence sessionrepo.SealedSourceEvidence) error {
	if ctx == nil {
		return ErrD09ExposureSource
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || evidence != s.source {
		return ErrD09ExposureSource
	}
	return s.verifyLocked(ctx)
}

// SourceCapability returns the opaque concrete Store-issued cap for the A-side
// TrustedHostVerifier/D09 launcher to bind into its admitted request. The
// returned pointer grants only Revalidate/Evidence/Close; no path or writable
// directory is exposed. The sessionlaunch wrapper remains its lifecycle owner.
func (s *VerifiedD09ExposureSource) SourceCapability() *sessionrepo.VerifiedSealedSource {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	return s.capability
}

func (s *VerifiedD09ExposureSource) SourceEvidence() (sessionrepo.SealedSourceEvidence, error) {
	if s == nil {
		return sessionrepo.SealedSourceEvidence{}, ErrD09ExposureSource
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.capability == nil {
		return sessionrepo.SealedSourceEvidence{}, ErrD09ExposureSource
	}
	return s.capability.Evidence()
}

func (s *VerifiedD09ExposureSource) verifyLocked(ctx context.Context) error {
	if s.owner == nil || s.capability == nil || s.store == nil || s.generation == nil || s.git == nil {
		return ErrD09ExposureSource
	}
	anchors, err := s.owner.verifyD06SourceForGeneration(ctx, s.generation, false)
	if err != nil {
		return err
	}
	if anchors.store != s.store || anchors.journal != s.journal || anchors.git != s.git ||
		anchors.provenance.BindingDigest != s.provenance.BindingDigest {
		return ErrD09ExposureSource
	}
	return s.capability.Revalidate(ctx, s.source)
}

func (s *VerifiedD09ExposureSource) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	// The channel owns and must close its exported source FD before the wrapper
	// releases the held capability/Store registration. If that FD is still open,
	// retain this wrapper so the owner can retry after channel close.
	if s.capability != nil {
		if err := s.capability.Close(); err != nil {
			return err
		}
	}
	s.closed = true
	owner := s.owner
	s.capability, s.owner = nil, nil
	if owner != nil {
		s.mu.Unlock()
		owner.mu.Lock()
		delete(owner.sources, s)
		owner.mu.Unlock()
		s.mu.Lock()
	}
	return nil
}

func validateSourceDescriptorIdentity(file *os.File, evidence sessionrepo.SealedSourceEvidence) error {
	if file == nil || evidence.SourceDevice == 0 || evidence.SourceInode == 0 || evidence.SourceMountID == 0 {
		return ErrD09ExposureSource
	}
	info, err := file.Stat()
	if err != nil || !info.IsDir() {
		return errors.Join(ErrD09ExposureSource, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Dev) != evidence.SourceDevice || stat.Ino != evidence.SourceInode {
		return ErrD09ExposureSource
	}
	mountID, err := descriptorMountID(file.Fd())
	if err != nil || mountID != evidence.SourceMountID {
		return errors.Join(ErrD09ExposureSource, err)
	}
	return nil
}

func canonicalDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	canonical, err := jcs.Transform(encoded)
	if err != nil {
		return "", err
	}
	return DigestBytes(canonical), nil
}

// descriptorMountID reads the mount identity Linux records for an open
// descriptor. This is SOURCE identity; D09 proof separately records the
// consumer mount ID after the read-only bind is checked in its own namespace.
func descriptorMountID(fd uintptr) (uint64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/self/fdinfo/%d", fd))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "mnt_id:" {
			id, err := strconv.ParseUint(fields[1], 10, 64)
			if err == nil && id != 0 {
				return id, nil
			}
			return 0, errors.New("descriptor mount ID is invalid")
		}
	}
	return 0, errors.New("descriptor fdinfo omitted mount ID")
}

func statDevice(info os.FileInfo) (uint64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(stat.Dev), true
}
