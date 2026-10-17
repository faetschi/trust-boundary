//go:build linux

package sessionrepo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"tbound/supervisor/internal/workspace"
)

var (
	ErrSealedSourceUnavailable = errors.New("verified sealed-generation source is unavailable or unverified")
	ErrSealedSourceOutstanding = errors.New("session Store still owns a D09 sealed-source capability")
	ErrSealedSourceExportOpen  = errors.New("caller-owned D09 source descriptor must close before its source capability")
)

// SealedSourceEvidence describes only the Store-owned host SOURCE descriptor.
// SourceMountID is never the identity of the later D09 consumer mount. The
// consumer proof is produced separately by the trusted D09 entrypoint after it
// has installed and checked the read-only bind inside the container namespace.
type SealedSourceEvidence struct {
	GenerationID          string
	TreeDigest            string
	ManifestDigest        string
	SourceBindingDigest   string
	ProvenanceDigest      string
	StoreAuditHead        string
	StoreAuditRecordCount uint64
	SourceIdentityDigest  string
	SourceDevice          uint64
	SourceInode           uint64
	SourceMountID         uint64
}

// VerifiedSealedSource is an opaque, Store-issued capability for one exact
// retained current generation. Its held O_PATH descriptor is source material
// for a trusted D09 Podman/crun setup only. It is not a read-only consumer view,
// cannot be passed to Pi/Node, and must not be used as a caller-path substitute.
//
// The capability retains the exact Store and Generation objects plus the D06
// source-binding/private-Git-ready records found in that Store's verified
// journal. Its zero value is invalid. Only Generation.ExportSealedSource can
// mint a live capability; exported evidence strings or directory descriptors
// cannot reconstruct one.
type VerifiedSealedSource struct {
	mu         sync.Mutex
	store      *Store
	generation *Generation
	source     *os.File
	evidence   SealedSourceEvidence
	exportedFD *os.File
	exported   bool
	closed     bool
}

// IsForGeneration reports pointer identity against the live Store issuer. It
// does not validate or authorize the source; callers must also call Revalidate
// with Evidence. The check prevents a same-label generation from another Store
// from being substituted at the sessionlaunch boundary.
func (s *VerifiedSealedSource) IsForGeneration(store *Store, generation *Generation) bool {
	if s == nil || store == nil || generation == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && s.store == store && s.generation == generation && generation.store == store
}

// OwnsGeneration reports whether generation is the exact retained object in
// this Store. A generation label/tree digest from another Store is not enough.
func (s *Store) OwnsGeneration(generation *Generation) bool {
	if s == nil || generation == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && generation.store == s && s.containsGenerationLocked(generation)
}

// ExportSealedSource issues an opaque source capability for g only when g is
// the Store's exact, fully verified current tip and the Store audit journal
// contains the durable sessionlaunch source-binding and private-Git-ready
// records for this run. It rescans and compares the sealed tree/manifest, opens
// a fresh O_PATH descriptor relative to the Store's generations directory,
// captures fstat/fdinfo identity, then re-verifies the Store and D06 journal
// before returning. No Binder, mount, caller path, or caller trust boolean is
// involved.
//
// The descriptor retained by the returned capability remains private to Go.
// DuplicateForD09 can return one caller-owned O_PATH descriptor for trusted
// Podman/crun staging; callers must close that descriptor. The D09 entrypoint
// must bind it read-only into its private namespace, prove the distinct consumer
// mount, replace/close the raw source FD before Node, and withhold readiness if
// that proof is absent.
func (g *Generation) ExportSealedSource(ctx context.Context) (*VerifiedSealedSource, error) {
	if ctx == nil || g == nil || g.store == nil {
		return nil, ErrSealedSourceUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(ErrSealedSourceUnavailable, err)
	}
	s := g.store
	chain, bundle, d06, err := verifySealedSourceAnchors(ctx, s, g, true)
	if err != nil {
		return nil, err
	}
	_ = chain
	_ = d06
	source, err := g.ReadOnlyMountSource()
	if err != nil {
		return nil, fmt.Errorf("open held sealed source descriptor: %w", errors.Join(ErrSealedSourceUnavailable, err))
	}
	stat, err := statSealedSource(source)
	if err != nil || stat.fileType != syscall.S_IFDIR {
		_ = source.Close()
		return nil, errors.Join(ErrSealedSourceUnavailable, err)
	}
	if err := verifyGenerationSourceManifest(g, source, bundle); err != nil {
		_ = source.Close()
		return nil, err
	}
	mountID, err := sealedSourceMountID(source.Fd())
	if err != nil {
		_ = source.Close()
		return nil, errors.Join(ErrSealedSourceUnavailable, err)
	}
	evidence, err := makeSealedSourceEvidence(g, bundle, d06, stat, mountID)
	if err != nil {
		_ = source.Close()
		return nil, err
	}
	// Close the verification window: both the exact Store tip and the pinned
	// source descriptor must still match after the evidence has been assembled.
	_, after, afterD06, err := verifySealedSourceAnchors(ctx, s, g, true)
	if err != nil || !sameSealedSourceAnchors(bundle, after, evidence, afterD06) {
		_ = source.Close()
		return nil, errors.Join(ErrSealedSourceUnavailable, err)
	}
	if err := verifySealedSourceDescriptor(source, evidence); err != nil {
		_ = source.Close()
		return nil, err
	}
	capability := &VerifiedSealedSource{store: s, generation: g, source: source, evidence: evidence}
	s.mu.Lock()
	if s.closed || !s.containsGenerationLocked(g) || len(s.generations) == 0 || s.generations[len(s.generations)-1] != g {
		s.mu.Unlock()
		_ = source.Close()
		return nil, ErrStaleGeneration
	}
	if s.sealedSources == nil {
		s.sealedSources = make(map[*VerifiedSealedSource]struct{})
	}
	s.sealedSources[capability] = struct{}{}
	s.mu.Unlock()
	if err := capability.Revalidate(ctx, evidence); err != nil {
		_ = capability.Close()
		return nil, err
	}
	return capability, nil
}

// Evidence returns the exact immutable source evidence minted with this cap.
// A zero/closed capability has no evidence.
func (s *VerifiedSealedSource) Evidence() (SealedSourceEvidence, error) {
	if s == nil {
		return SealedSourceEvidence{}, ErrSealedSourceUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.source == nil {
		return SealedSourceEvidence{}, ErrSealedSourceUnavailable
	}
	return s.evidence, nil
}

// DuplicateForD09 returns one CLOEXEC O_PATH duplicate for the trusted D09
// source-bind setup. The duplicate belongs to the caller and must be closed by
// that caller on all success/failure/teardown paths. The capability itself
// retains a separate source descriptor for revalidation. The export is
// deliberately one-use so a source capability cannot be fanned out into
// independent mounts/sessions.
func (s *VerifiedSealedSource) DuplicateForD09(ctx context.Context) (*os.File, SealedSourceEvidence, error) {
	if ctx == nil {
		return nil, SealedSourceEvidence{}, ErrSealedSourceUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.source == nil || s.exported {
		return nil, SealedSourceEvidence{}, ErrSealedSourceUnavailable
	}
	if err := s.revalidateLocked(ctx, s.evidence); err != nil {
		return nil, SealedSourceEvidence{}, err
	}
	fd, err := syscall.Openat(int(s.source.Fd()), ".", linuxOPath|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, SealedSourceEvidence{}, errors.Join(ErrSealedSourceUnavailable, err)
	}
	file := os.NewFile(uintptr(fd), "d09-trusted-source-staging")
	if file == nil {
		_ = syscall.Close(fd)
		return nil, SealedSourceEvidence{}, ErrSealedSourceUnavailable
	}
	if err := verifySealedSourceDescriptor(file, s.evidence); err != nil {
		_ = file.Close()
		return nil, SealedSourceEvidence{}, err
	}
	if err := ctx.Err(); err != nil {
		_ = file.Close()
		return nil, SealedSourceEvidence{}, errors.Join(ErrSealedSourceUnavailable, err)
	}
	s.exported = true
	s.exportedFD = file
	return file, s.evidence, nil
}

// Revalidate confirms the concrete live Store/Generation issuer, current
// selected tip, chain, D06 source-binding/private-Git-ready record pair, full
// manifest/content, and held source descriptor. It accepts typed evidence only;
// no function callback, path, or bool can turn an arbitrary directory into this
// capability. The evidence remains SOURCE evidence, not consumer mount proof.
func (s *VerifiedSealedSource) Revalidate(ctx context.Context, evidence SealedSourceEvidence) error {
	if ctx == nil {
		return ErrSealedSourceUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revalidateLocked(ctx, evidence)
}

func (s *VerifiedSealedSource) revalidateLocked(ctx context.Context, evidence SealedSourceEvidence) error {
	if s.closed || s.store == nil || s.generation == nil || s.source == nil || evidence != s.evidence {
		return ErrSealedSourceUnavailable
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrSealedSourceUnavailable, err)
	}
	_, bundle, d06, err := verifySealedSourceAnchors(ctx, s.store, s.generation, false)
	if err != nil {
		return err
	}
	if !sealedSourceAuditPrefixMatches(bundle, s.evidence) {
		return ErrSealedSourceUnavailable
	}
	stat, err := statSealedSource(s.source)
	if err != nil || stat.fileType != syscall.S_IFDIR {
		return errors.Join(ErrSealedSourceUnavailable, err)
	}
	mountID, err := sealedSourceMountID(s.source.Fd())
	if err != nil {
		return errors.Join(ErrSealedSourceUnavailable, err)
	}
	expected, err := makeSealedSourceEvidence(s.generation, bundle, d06, stat, mountID)
	if err != nil {
		return errors.Join(ErrSealedSourceUnavailable, err)
	}
	// The capability is issued at one current tip and binds the journal prefix
	// present then. Later successful effects may append new generations/records;
	// the original generation remains valid if it is still a retained immutable
	// ancestor and its original prefix is byte/hash-chain intact.
	expected.StoreAuditHead = s.evidence.StoreAuditHead
	expected.StoreAuditRecordCount = s.evidence.StoreAuditRecordCount
	expected.SourceIdentityDigest, err = sealedSourceIdentityDigest(expected)
	if err != nil || expected != s.evidence {
		return errors.Join(ErrSealedSourceUnavailable, err)
	}
	if err := verifyGenerationSourceManifest(s.generation, s.source, bundle); err != nil {
		return err
	}
	return verifySealedSourceDescriptor(s.source, s.evidence)
}

// Close releases only the capability's private held source descriptor. It does
// not close a descriptor previously returned by DuplicateForD09; that exported
// staging descriptor is caller-owned and has its own explicit Close obligation.
func (s *VerifiedSealedSource) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	if s.exportedFD != nil && s.exportedFD.Fd() != ^uintptr(0) {
		return ErrSealedSourceExportOpen
	}
	s.closed = true
	var errs []error
	if s.source != nil {
		errs = append(errs, s.source.Close())
	}
	s.source = nil
	store := s.store
	s.store, s.generation = nil, nil
	if store != nil {
		store.mu.Lock()
		delete(store.sealedSources, s)
		store.mu.Unlock()
	}
	return errors.Join(errs...)
}

type d06SourceProvenance struct {
	BindingDigest        string          `json:"binding_digest"`
	SourceGeneration     string          `json:"source_generation"`
	SourceTreeDigest     string          `json:"source_tree_digest"`
	MetadataPolicyDigest string          `json:"metadata_policy_digest"`
	XattrProfileDigest   string          `json:"xattr_profile_digest"`
	XattrAuthority       string          `json:"xattr_authority"`
	PrivateGitDigest     string          `json:"private_git_digest"`
	SourceRecord         uint64          `json:"source_record_sequence"`
	PrivateGitRecord     uint64          `json:"private_git_record_sequence"`
	PrivateGit           json.RawMessage `json:"private_git"`
	ProvenanceDigest     string          `json:"-"`
}

type d06SourceEvent struct {
	Provenance d06SourceProvenance `json:"provenance"`
	Manifest   delta.TreeManifest  `json:"source_manifest"`
}

type d06PrivateGitEvent struct {
	SchemaVersion string              `json:"schema_version"`
	Provenance    d06SourceProvenance `json:"provenance"`
	Snapshot      struct {
		Manifest   delta.TreeManifest
		TreeDigest string
	} `json:"snapshot"`
}

func verifySealedSourceAnchors(ctx context.Context, store *Store, generation *Generation, requireCurrentTip bool) (delta.Result, EvidenceBundle, d06SourceProvenance, error) {
	if ctx == nil || store == nil || generation == nil || generation.store != store {
		return delta.Result{}, EvidenceBundle{}, d06SourceProvenance{}, ErrSealedSourceUnavailable
	}
	if err := ctx.Err(); err != nil {
		return delta.Result{}, EvidenceBundle{}, d06SourceProvenance{}, errors.Join(ErrSealedSourceUnavailable, err)
	}
	chain, err := store.Verify()
	if err != nil {
		return delta.Result{}, EvidenceBundle{}, d06SourceProvenance{}, errors.Join(ErrSealedSourceUnavailable, err)
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		return delta.Result{}, EvidenceBundle{}, d06SourceProvenance{}, errors.Join(ErrSealedSourceUnavailable, err)
	}
	if len(bundle.Generations) == 0 || !reflect.DeepEqual(chain, bundle.ValidatedChain) ||
		!bundleContainsGeneration(bundle, generation) || containsReservedGitMetadata(generation.Manifest()) {
		return delta.Result{}, EvidenceBundle{}, d06SourceProvenance{}, ErrStaleGeneration
	}
	if requireCurrentTip {
		if chain.SealedGeneration != generation.ID() || chain.SealedTreeDigest != generation.TreeDigest() {
			return delta.Result{}, EvidenceBundle{}, d06SourceProvenance{}, ErrStaleGeneration
		}
		last := bundle.Generations[len(bundle.Generations)-1]
		if last.ID != generation.ID() || last.TreeDigest != generation.TreeDigest() || !reflect.DeepEqual(last.Manifest, generation.Manifest()) {
			return delta.Result{}, EvidenceBundle{}, d06SourceProvenance{}, ErrStaleGeneration
		}
	}
	d06, err := verifyD06SourceRecords(bundle)
	if err != nil {
		return delta.Result{}, EvidenceBundle{}, d06SourceProvenance{}, err
	}
	return chain, bundle, d06, nil
}

func verifyD06SourceRecords(bundle EvidenceBundle) (d06SourceProvenance, error) {
	trace, err := audit.Verify(bytes.NewReader(bundle.AuditJournal))
	if err != nil || uint64(len(trace.Records)) != bundle.AuditRecordCount || auditHeadHash(trace.Records) != bundle.AuditHeadHash {
		return d06SourceProvenance{}, errors.Join(ErrSealedSourceUnavailable, err)
	}
	var source *audit.Record
	var ready *audit.Record
	for i := range trace.Records {
		record := &trace.Records[i]
		switch record.Event.Kind {
		case "sessionlaunch_source_binding":
			if source != nil {
				return d06SourceProvenance{}, fmt.Errorf("%w: duplicate D06 source binding records", ErrSealedSourceUnavailable)
			}
			source = record
		case "sessionlaunch_private_git_ready":
			if ready != nil {
				return d06SourceProvenance{}, fmt.Errorf("%w: duplicate D06 private-Git-ready records", ErrSealedSourceUnavailable)
			}
			ready = record
		}
	}
	if source == nil || ready == nil || source.Sequence == 0 || ready.Sequence <= source.Sequence {
		return d06SourceProvenance{}, fmt.Errorf("%w: durable D06 source/private-Git provenance is missing", ErrSealedSourceUnavailable)
	}
	var sourceEvent d06SourceEvent
	var readyEvent d06PrivateGitEvent
	if json.Unmarshal(source.Event.Data, &sourceEvent) != nil || json.Unmarshal(ready.Event.Data, &readyEvent) != nil ||
		readyEvent.SchemaVersion != "tbound-sessionlaunch-provenance/v1" {
		return d06SourceProvenance{}, fmt.Errorf("%w: D06 provenance record schema is invalid", ErrSealedSourceUnavailable)
	}
	sp, rp := sourceEvent.Provenance, readyEvent.Provenance
	if sp.BindingDigest == "" || !validSHA256(sp.BindingDigest) ||
		rkD06ProvenanceMismatch(sp, rp, source.Sequence, ready.Sequence) ||
		sourceEvent.Manifest.Generation != sp.SourceGeneration ||
		!reflect.DeepEqual(sourceEvent.Manifest, readyEvent.Snapshot.Manifest) ||
		readyEvent.Snapshot.TreeDigest != sp.SourceTreeDigest ||
		sourceEvent.Manifest.Generation != readyEvent.Snapshot.Manifest.Generation {
		return d06SourceProvenance{}, fmt.Errorf("%w: D06 source and private-Git records do not agree", ErrSealedSourceUnavailable)
	}
	var privateGit struct {
		SourceGeneration     string             `json:"source_generation"`
		SourceTreeDigest     string             `json:"source_tree_digest"`
		SourceManifest       delta.TreeManifest `json:"source_manifest"`
		MetadataPolicyDigest string             `json:"metadata_policy_digest"`
		SourceBindingDigest  string             `json:"source_binding_digest"`
		XattrProfileDigest   string             `json:"xattr_profile_digest"`
		XattrAuthority       string             `json:"xattr_authority"`
	}
	privateGitDigest := sha256.Sum256(rp.PrivateGit)
	if json.Unmarshal(rp.PrivateGit, &privateGit) != nil || !reflect.DeepEqual(privateGit.SourceManifest, sourceEvent.Manifest) ||
		privateGit.SourceGeneration != rp.SourceGeneration || privateGit.SourceTreeDigest != rp.SourceTreeDigest ||
		privateGit.MetadataPolicyDigest != sourceEvent.Manifest.MetadataPolicyDigest ||
		privateGit.SourceBindingDigest != rp.BindingDigest || privateGit.XattrProfileDigest != rp.XattrProfileDigest ||
		privateGit.XattrAuthority != rp.XattrAuthority ||
		"sha256:"+hex.EncodeToString(privateGitDigest[:]) != rp.PrivateGitDigest {
		return d06SourceProvenance{}, fmt.Errorf("%w: private-Git provenance does not bind the exact D06 source manifest", ErrSealedSourceUnavailable)
	}
	treeDigest, err := delta.ComputeTreeDigest(sourceEvent.Manifest)
	if err != nil || treeDigest != sp.SourceTreeDigest || !validTreeDigest(sp.SourceTreeDigest) || !validSHA256(rp.PrivateGitDigest) {
		return d06SourceProvenance{}, errors.Join(fmt.Errorf("%w: D06 source manifest/tree digest mismatch (computed=%q recorded=%q valid=%t private_git=%t)",
			ErrSealedSourceUnavailable, treeDigest, sp.SourceTreeDigest, validTreeDigest(sp.SourceTreeDigest), validSHA256(sp.PrivateGitDigest)), err)
	}
	var readyEnvelope map[string]json.RawMessage
	if json.Unmarshal(ready.Event.Data, &readyEnvelope) != nil {
		return d06SourceProvenance{}, ErrSealedSourceUnavailable
	}
	rawProvenance := readyEnvelope["provenance"]
	canonical, err := jcs.Transform(rawProvenance)
	if err != nil || len(rawProvenance) == 0 {
		return d06SourceProvenance{}, errors.Join(ErrSealedSourceUnavailable, err)
	}
	digest := sha256.Sum256(canonical)
	rp.ProvenanceDigest = "sha256:" + hex.EncodeToString(digest[:])
	sp.PrivateGit = append(json.RawMessage(nil), rp.PrivateGit...)
	sp.PrivateGitDigest = rp.PrivateGitDigest
	sp.PrivateGitRecord = rp.PrivateGitRecord
	sp.ProvenanceDigest = rp.ProvenanceDigest
	return sp, nil
}

func rkD06ProvenanceMismatch(source, ready d06SourceProvenance, sourceSequence, readySequence uint64) bool {
	return source.SourceGeneration == "" || source.SourceTreeDigest == "" ||
		source.SourceGeneration != ready.SourceGeneration || source.SourceTreeDigest != ready.SourceTreeDigest ||
		source.BindingDigest != ready.BindingDigest ||
		source.XattrAuthority != string(workspace.XattrAuthorityBoundedNoHiddenTrusted) || source.XattrAuthority != ready.XattrAuthority || source.SourceRecord != sourceSequence ||
		source.PrivateGitRecord != 0 || source.PrivateGitDigest != "" ||
		ready.SourceRecord != sourceSequence || ready.PrivateGitRecord != readySequence ||
		ready.PrivateGitDigest == "" || len(ready.PrivateGit) == 0
}

func makeSealedSourceEvidence(generation *Generation, bundle EvidenceBundle, d06 d06SourceProvenance, stat sealedSourceStat, mountID uint64) (SealedSourceEvidence, error) {
	manifestDigest, err := digestJCS(generation.Manifest())
	if err != nil {
		return SealedSourceEvidence{}, errors.Join(ErrSealedSourceUnavailable, err)
	}
	provenanceDigest := d06.ProvenanceDigest
	if !validSHA256(provenanceDigest) {
		return SealedSourceEvidence{}, ErrSealedSourceUnavailable
	}
	evidence := SealedSourceEvidence{
		GenerationID: generation.ID(), TreeDigest: digestSHA256([]byte(generation.TreeDigest())),
		ManifestDigest: manifestDigest, SourceBindingDigest: d06.BindingDigest,
		ProvenanceDigest: provenanceDigest, StoreAuditHead: bundle.AuditHeadHash,
		StoreAuditRecordCount: bundle.AuditRecordCount,
		SourceDevice:          stat.device, SourceInode: stat.inode, SourceMountID: mountID,
	}
	identityDigest, err := sealedSourceIdentityDigest(evidence)
	if err != nil {
		return SealedSourceEvidence{}, errors.Join(ErrSealedSourceUnavailable, err)
	}
	evidence.SourceIdentityDigest = identityDigest
	return evidence, nil
}

func sealedSourceIdentityDigest(evidence SealedSourceEvidence) (string, error) {
	identity := struct {
		Domain     string `json:"domain"`
		Generation string `json:"generation"`
		Tree       string `json:"tree_digest"`
		Manifest   string `json:"manifest_digest"`
		Binding    string `json:"source_binding_digest"`
		Provenance string `json:"provenance_digest"`
		AuditHead  string `json:"audit_head"`
		AuditCount uint64 `json:"audit_record_count"`
		Device     uint64 `json:"source_device"`
		Inode      uint64 `json:"source_inode"`
		MountID    uint64 `json:"source_mount_id"`
	}{"tbound/sessionrepo/sealed-source/v1", evidence.GenerationID, evidence.TreeDigest,
		evidence.ManifestDigest, evidence.SourceBindingDigest, evidence.ProvenanceDigest,
		evidence.StoreAuditHead, evidence.StoreAuditRecordCount, evidence.SourceDevice,
		evidence.SourceInode, evidence.SourceMountID}
	return digestJCS(identity)
}

func sameSealedSourceAnchors(before, after EvidenceBundle, evidence SealedSourceEvidence, d06 d06SourceProvenance) bool {
	return validSHA256(d06.ProvenanceDigest) && d06.BindingDigest == evidence.SourceBindingDigest &&
		d06.ProvenanceDigest == evidence.ProvenanceDigest && before.AuditHeadHash == after.AuditHeadHash &&
		before.AuditRecordCount == after.AuditRecordCount &&
		before.ValidatedChain.SealedGeneration == after.ValidatedChain.SealedGeneration &&
		before.ValidatedChain.SealedTreeDigest == after.ValidatedChain.SealedTreeDigest
}

func sealedSourceAuditPrefixMatches(bundle EvidenceBundle, evidence SealedSourceEvidence) bool {
	if evidence.StoreAuditRecordCount == 0 || bundle.AuditRecordCount < evidence.StoreAuditRecordCount {
		return false
	}
	trace, err := audit.Verify(bytes.NewReader(bundle.AuditJournal))
	if err != nil || uint64(len(trace.Records)) != bundle.AuditRecordCount ||
		uint64(len(trace.Records)) < evidence.StoreAuditRecordCount {
		return false
	}
	prefixCount := int(evidence.StoreAuditRecordCount)
	return auditHeadHash(trace.Records[:prefixCount]) == evidence.StoreAuditHead
}

type sealedSourceStat struct {
	device   uint64
	inode    uint64
	mode     uint32
	fileType uint32
}

func statSealedSource(file *os.File) (sealedSourceStat, error) {
	if file == nil {
		return sealedSourceStat{}, ErrSealedSourceUnavailable
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &stat); err != nil {
		return sealedSourceStat{}, err
	}
	return sealedSourceStat{device: uint64(stat.Dev), inode: stat.Ino, mode: stat.Mode, fileType: stat.Mode & syscall.S_IFMT}, nil
}

func verifyGenerationSourceManifest(generation *Generation, source *os.File, bundle EvidenceBundle) error {
	if generation == nil || source == nil {
		return ErrSealedSourceUnavailable
	}
	opened, err := generation.ReadOnlyMountSource()
	if err != nil {
		return errors.Join(ErrSealedSourceUnavailable, err)
	}
	defer opened.Close()
	left, err := statSealedSource(opened)
	if err != nil {
		return errors.Join(ErrSealedSourceUnavailable, err)
	}
	right, err := statSealedSource(source)
	if err != nil || left.device != right.device || left.inode != right.inode || left.fileType != syscall.S_IFDIR || right.fileType != syscall.S_IFDIR {
		return errors.Join(ErrStaleGeneration, err)
	}
	if !bundleContainsGeneration(bundle, generation) || containsReservedGitMetadata(generation.Manifest()) {
		return ErrStaleGeneration
	}
	return nil
}

func verifySealedSourceDescriptor(file *os.File, evidence SealedSourceEvidence) error {
	stat, err := statSealedSource(file)
	if err != nil || stat.fileType != syscall.S_IFDIR || stat.device != evidence.SourceDevice || stat.inode != evidence.SourceInode {
		return errors.Join(ErrSealedSourceUnavailable, err)
	}
	mountID, err := sealedSourceMountID(file.Fd())
	if err != nil || mountID != evidence.SourceMountID {
		return errors.Join(ErrSealedSourceUnavailable, err)
	}
	return nil
}

func sealedSourceMountID(fd uintptr) (uint64, error) {
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
			return 0, errors.New("source descriptor mount ID is invalid")
		}
	}
	return 0, errors.New("source descriptor fdinfo omitted mount ID")
}

func auditHeadHash(records []audit.Record) string {
	if len(records) == 0 {
		return ""
	}
	return records[len(records)-1].Hash
}

func digestJCS(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	canonical, err := jcs.Transform(encoded)
	if err != nil {
		return "", err
	}
	return digestSHA256(canonical), nil
}

func digestSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func bundleContainsGeneration(bundle EvidenceBundle, generation *Generation) bool {
	if generation == nil {
		return false
	}
	for _, item := range bundle.Generations {
		if item.ID == generation.ID() && item.TreeDigest == generation.TreeDigest() &&
			reflect.DeepEqual(item.Manifest, generation.Manifest()) {
			return true
		}
	}
	return false
}

func containsReservedGitMetadata(manifest delta.TreeManifest) bool {
	for _, object := range manifest.Objects {
		for _, component := range strings.Split(object.Path, "/") {
			if strings.EqualFold(component, ".git") {
				return true
			}
		}
	}
	return false
}
