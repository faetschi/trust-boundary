//go:build linux

package sessionlaunch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/gowebpki/jcs"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/sessionrepo"
)

// ErrWorkerExposureContract is the fail-closed sentinel for the concrete
// sealed-exposure adapter. It is returned when the registered read-only view
// cannot be established (for example because the Store has no exposure binder),
// when the durable D06 provenance/journal no longer anchors the selected
// generation, or when a supplied evidence record does not match this session.
// Callers must not substitute a fixture, a caller-built descriptor, or a
// caller-provided boolean for a successful verification.
var ErrWorkerExposureContract = errors.New("sealed worker exposure contract is unavailable or unverified")

// SealedWorkerExposure is the sessionlaunch-owned implementation of
// piruntime.VerifiedWorkerExposure for exactly one PreparedSession's registered
// read-only sealed generation view.
//
// It is the D06-safe replacement for handing a worker the private Git admin
// root or the Store root: Export returns a fresh O_PATH descriptor for the
// registered exposure view and Verify re-proves the durable D06
// binding/provenance journal, the current-generation selection, and the exact
// descriptor identity before the launcher may bind child FD 4. Repository.Root,
// AdminParent.root, and the Store generations/admin directories are never
// exported.
type SealedWorkerExposure struct {
	mu         sync.Mutex
	session    *PreparedSession
	view       *WorkerView
	generation *sessionrepo.Generation

	// Captured once at construction from the live PreparedSession so Verify does
	// not read mutable PreparedSession fields without its lock.
	store      *sessionrepo.Store
	journal    *audit.Journal
	profile    SourceProfile
	provenance Provenance

	identity exposureIdentity
	closed   bool
}

// SealedWorkerExposure must satisfy the launcher-owned descriptor interface
// exactly; this is the D06-safe binding the composition root passes as the
// worker exposure capability.
var _ piruntime.VerifiedWorkerExposure = (*SealedWorkerExposure)(nil)

// exposureIdentity is the immutable generation/binding identity of one sealed
// exposure. Device/Inode/MountID are intentionally excluded: they are re-read
// from the descriptor being exported or verified.
type exposureIdentity struct {
	generationID string
	// sourceTreeDigest is the delta tree-digest string used for internal
	// generation/provenance comparisons. treeDigest is the plain sha256
	// commitment emitted in WorkerExposureEvidence, whose shape validator
	// requires a "sha256:" digest.
	sourceTreeDigest string
	treeDigest       string
	manifestDigest   string
	bindingDigest    string
	provenanceDigest string
	viewIdentity     string
}

// OpenSealedWorkerExposure is the only supported worker-exposure constructor
// for a prepared D06 session. It re-runs ExposeReadOnly, which re-verifies the
// sessionrepo chain/bundle, the durable sessionlaunch_source_binding and
// sessionlaunch_private_git_ready journal records, the private Git boundary,
// and that the requested generation is the durable current selection, before a
// registered read-only view is created. It then computes the exact evidence a
// launcher must re-verify.
//
// A Store without a registered sessionrepo.Binder fails closed with an error
// wrapping sessionrepo.ErrExposureBinderUnavailable; the adapter cannot and
// does not fabricate a read-only view.
func (p *PreparedSession) OpenSealedWorkerExposure(generation *sessionrepo.Generation) (*SealedWorkerExposure, error) {
	if p == nil || generation == nil {
		return nil, ErrWorkerExposureContract
	}
	view, err := p.ExposeReadOnly(generation, sessionrepo.ViewRecreated)
	if err != nil {
		return nil, errors.Join(ErrWorkerExposureContract, err)
	}
	store, journal, profile, provenance, err := p.workerExposureAnchors()
	if err != nil {
		_ = view.Close()
		return nil, err
	}
	exposure := &SealedWorkerExposure{
		session: p, view: view, generation: generation,
		store: store, journal: journal, profile: profile, provenance: provenance,
	}
	identity, err := exposure.computeIdentity()
	if err != nil {
		_ = view.Close()
		return nil, errors.Join(ErrWorkerExposureContract, err)
	}
	exposure.identity = identity
	return exposure, nil
}

// workerExposureAnchors captures the immutable anchors of a live prepared
// session under its lock. It refuses a closing/closed session.
func (p *PreparedSession) workerExposureAnchors() (*sessionrepo.Store, *audit.Journal, SourceProfile, Provenance, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.closing || p.store == nil || p.generation == nil || p.journal == nil || p.git == nil {
		return nil, nil, SourceProfile{}, Provenance{}, ErrWorkerExposureContract
	}
	return p.store, p.journal, p.profile, p.provenance, nil
}

// computeIdentity derives the generation/binding identity from the durable
// provenance and the retained generation. It does not read any descriptor; the
// descriptor-derived fields are added per Export/Verify.
func (e *SealedWorkerExposure) computeIdentity() (exposureIdentity, error) {
	if e == nil || e.generation == nil {
		return exposureIdentity{}, ErrWorkerExposureContract
	}
	provenance := e.provenance
	if !validDigest(provenance.BindingDigest) || provenance.SourceGeneration != e.generation.ID() ||
		provenance.SourceTreeDigest != e.generation.TreeDigest() {
		return exposureIdentity{}, ErrWorkerExposureContract
	}
	manifestDigest, err := canonicalDigest(e.generation.Manifest())
	if err != nil {
		return exposureIdentity{}, err
	}
	provenanceDigest, err := canonicalDigest(provenance)
	if err != nil {
		return exposureIdentity{}, err
	}
	viewIdentity := DigestBytes([]byte("tbound/sessionlaunch/worker-view/v1\x00" +
		provenance.BindingDigest + "\x00" + provenance.SourceTreeDigest + "\x00" + provenance.SourceGeneration))
	return exposureIdentity{
		generationID:     e.generation.ID(),
		sourceTreeDigest: e.generation.TreeDigest(),
		treeDigest:       DigestBytes([]byte("tbound/sessionlaunch/worker-tree/v1\x00" + e.generation.TreeDigest())),
		manifestDigest:   manifestDigest,
		bindingDigest:    provenance.BindingDigest,
		provenanceDigest: provenanceDigest,
		viewIdentity:     viewIdentity,
	}, nil
}

// ExportVerifiedWorkerExposure re-proves the durable D06 provenance and the
// current-generation selection, then returns a fresh O_PATH descriptor for the
// registered read-only view together with evidence whose descriptor identity is
// read from that same descriptor. The returned descriptor is the registered
// exposure view, never Repository.Root, the admin parent, or a Store directory.
func (e *SealedWorkerExposure) ExportVerifiedWorkerExposure(ctx context.Context) (*os.File, piruntime.WorkerExposureEvidence, error) {
	if ctx == nil {
		return nil, piruntime.WorkerExposureEvidence{}, ErrWorkerExposureContract
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.session == nil || e.view == nil || e.generation == nil {
		return nil, piruntime.WorkerExposureEvidence{}, ErrWorkerExposureContract
	}
	if err := e.verifyLocked(); err != nil {
		return nil, piruntime.WorkerExposureEvidence{}, err
	}
	descriptor, observed, err := e.view.Descriptor()
	if err != nil {
		return nil, piruntime.WorkerExposureEvidence{}, errors.Join(ErrWorkerExposureContract, err)
	}
	if observed.ID != e.identity.generationID || observed.TreeDigest != e.identity.sourceTreeDigest {
		_ = descriptor.Close()
		return nil, piruntime.WorkerExposureEvidence{}, ErrWorkerExposureContract
	}
	evidence, err := e.evidenceFor(descriptor)
	if err != nil {
		_ = descriptor.Close()
		return nil, piruntime.WorkerExposureEvidence{}, err
	}
	return descriptor, evidence, nil
}

// VerifyWorkerExposure re-checks that a previously exported evidence record
// still matches this session's durable D06 provenance, current selection, and
// registered view. It is called by the D09 launcher before it creates child
// FD 4; a syntactically valid evidence copy from a fixture does not authorize a
// child.
func (e *SealedWorkerExposure) VerifyWorkerExposure(ctx context.Context, evidence piruntime.WorkerExposureEvidence) error {
	if ctx == nil {
		return ErrWorkerExposureContract
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.session == nil || e.view == nil || e.generation == nil {
		return ErrWorkerExposureContract
	}
	if evidence.GenerationID != e.identity.generationID || evidence.TreeDigest != e.identity.treeDigest ||
		evidence.ManifestDigest != e.identity.manifestDigest || evidence.BindingDigest != e.identity.bindingDigest ||
		evidence.ProvenanceDigest != e.identity.provenanceDigest || evidence.ViewIdentity != e.identity.viewIdentity ||
		evidence.Device == 0 || evidence.Inode == 0 || evidence.MountID == 0 {
		return ErrWorkerExposureContract
	}
	if err := e.verifyLocked(); err != nil {
		return err
	}
	descriptor, _, err := e.view.Descriptor()
	if err != nil {
		return errors.Join(ErrWorkerExposureContract, err)
	}
	defer descriptor.Close()
	stat, err := statNode(descriptor)
	if err != nil || stat.mode&syscall.S_IFMT != syscall.S_IFDIR {
		return errors.Join(ErrWorkerExposureContract, err)
	}
	mountID, err := descriptorMountID(descriptor.Fd())
	if err != nil {
		return errors.Join(ErrWorkerExposureContract, err)
	}
	if stat.device != evidence.Device || stat.inode != evidence.Inode || mountID != evidence.MountID {
		return ErrWorkerExposureContract
	}
	return nil
}

// verifyLocked re-proves the durable anchors of the selected generation. It
// reuses the existing sessionlaunch validators: VerifySourceBinding (Store
// chain/bundle, current generation, D06 journal records, private Git boundary)
// plus the retained generation descriptor rescan that ExposeReadOnly uses.
func (e *SealedWorkerExposure) verifyLocked() error {
	if e.session == nil || e.view == nil || e.generation == nil || e.store == nil || e.journal == nil {
		return ErrWorkerExposureContract
	}
	if err := e.session.VerifySourceBinding(e.store, e.generation, e.journal); err != nil {
		return errors.Join(ErrWorkerExposureContract, err)
	}
	bundle, err := e.store.EvidenceBundle()
	if err != nil {
		return errors.Join(ErrWorkerExposureContract, err)
	}
	source, err := e.generation.ReadOnlyMountSource()
	if err != nil {
		return errors.Join(ErrWorkerExposureContract, err)
	}
	readable, err := openReadableDirAt(source)
	_ = source.Close()
	if err != nil {
		return errors.Join(ErrWorkerExposureContract, err)
	}
	stat, statErr := statNode(readable)
	if statErr != nil {
		_ = readable.Close()
		return errors.Join(ErrWorkerExposureContract, statErr)
	}
	verifyErr := verifyGenerationDescriptor(e.store, e.generation, readable, stat, bundle, e.profile)
	closeErr := readable.Close()
	if verifyErr != nil || closeErr != nil {
		return errors.Join(ErrWorkerExposureContract, verifyErr, closeErr)
	}
	observed, ok := e.view.observedEvidence()
	if !ok || observed.ID != e.identity.generationID || observed.TreeDigest != e.identity.sourceTreeDigest {
		return ErrWorkerExposureContract
	}
	return nil
}

// evidenceFor binds the durable identity to the exact descriptor being exported
// or verified.
func (e *SealedWorkerExposure) evidenceFor(descriptor *os.File) (piruntime.WorkerExposureEvidence, error) {
	stat, err := statNode(descriptor)
	if err != nil || stat.mode&syscall.S_IFMT != syscall.S_IFDIR {
		return piruntime.WorkerExposureEvidence{}, errors.Join(ErrWorkerExposureContract, err)
	}
	mountID, err := descriptorMountID(descriptor.Fd())
	if err != nil {
		return piruntime.WorkerExposureEvidence{}, errors.Join(ErrWorkerExposureContract, err)
	}
	return piruntime.WorkerExposureEvidence{
		GenerationID:     e.identity.generationID,
		TreeDigest:       e.identity.treeDigest,
		ManifestDigest:   e.identity.manifestDigest,
		BindingDigest:    e.identity.bindingDigest,
		ProvenanceDigest: e.identity.provenanceDigest,
		ViewIdentity:     e.identity.viewIdentity,
		Device:           stat.device,
		Inode:            stat.inode,
		MountID:          mountID,
	}, nil
}

// Close withdraws this adapter's registered view. It does not close the
// PreparedSession; the session owner remains responsible for Store/Git teardown.
func (e *SealedWorkerExposure) Close() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	view := e.view
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	e.view = nil
	e.mu.Unlock()
	if view != nil {
		return view.Close()
	}
	return nil
}

// observedEvidence returns a defensive copy of the registered view's selected
// generation evidence while the view is still open.
func (v *WorkerView) observedEvidence() (sessionrepo.GenerationEvidence, bool) {
	if v == nil {
		return sessionrepo.GenerationEvidence{}, false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed || v.source == nil {
		return sessionrepo.GenerationEvidence{}, false
	}
	return cloneGenerationEvidence(v.evidence), true
}

// canonicalDigest is the RFC-8785 (JCS) SHA-256 commitment used for the
// manifest and provenance identity fields.
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
// descriptor. It mirrors the launcher's own descriptor-identity check so the
// evidence this package emits is exactly what the launcher re-verifies.
func descriptorMountID(fd uintptr) (uint64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/self/fdinfo/%d", fd))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "mnt_id:" {
			id, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil || id == 0 {
				return 0, errors.New("descriptor mount ID is invalid")
			}
			return id, nil
		}
	}
	return 0, errors.New("descriptor fdinfo omitted mount ID")
}

// statDevice returns the containing filesystem device of an opened node.
func statDevice(info os.FileInfo) (uint64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(stat.Dev), true
}
