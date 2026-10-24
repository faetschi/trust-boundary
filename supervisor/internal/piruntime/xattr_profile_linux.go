//go:build linux

package piruntime

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/workspace"
)

const d09XattrVisibilityDomain = "tbound-d09-xattr-visibility/v2\x00"

// D09XattrScanPrivilegeProfile is a closed, measured scanner authority profile:
// the dedicated non-root service UID/GID, zero effective capabilities,
// NoNewPrivs, and the initial host user namespace (complete UID/GID maps).
const D09XattrScanPrivilegeProfile = "service-nonroot-no-extra-groups-empty-caps-nnp-initial-userns/v1"

// D09XattrBoundedAuthority is the only xattr-visibility authority this build
// admits. It is a BOUNDED, root-attested result, not a completeness proof.
//
// What it claims: the trusted host administrator (root; the host-admin trust
// assumption is part of the thesis TCB) attests, through the separately signed
// enumeration calibration, that for the one measured filesystem type, mount
// options, scanner uid/gid and source access profile there are no hidden
// trusted.* attributes on the governed source. The label is carried in the
// signed cell template, the signed calibration, and the managed-root xattr
// evidence, so downstream receipts show this bounded claim.
//
// What it does NOT claim: that the unprivileged scanner enumerates every
// extended attribute. It does not. A non-root scanner cannot list trusted.*
// names at all (the known counterexample is trusted.tbound.probe, hidden from
// uid 995), so the scan's silence about trusted.* is not evidence of absence;
// the trusted.* statement rests on root's attestation, not on the enumeration.
// It makes no claim about other filesystems, other mounts or uids, hosts
// without a trusted administrator, or attributes created after the
// measurement. Complete enumeration is not granted and no authority string for
// it exists in this build.
const D09XattrBoundedAuthority = string(workspace.XattrAuthorityBoundedNoHiddenTrusted)

// D09XattrVisibilityAuthority is the closed signed-template policy value. It
// is the bounded authority; it is the documented default and the only value
// ValidD09XattrVisibilityAuthority accepts. Runtime APIs still verify the
// actual protected profile and source descriptor; a caller-provided boolean or
// digest alone cannot issue the workspace attestation, and workspace
// attestation carries this typed bounded Authority, never a completeness bit.
const D09XattrVisibilityAuthority = D09XattrBoundedAuthority

// ValidD09XattrVisibilityAuthority reports whether authority is the single
// admitted bounded authority. Unknown strings, including the retired
// complete-enumeration label, are refused.
func ValidD09XattrVisibilityAuthority(authority string) bool {
	return authority == D09XattrBoundedAuthority
}

const (
	D09XattrEnumerationCalibrationSchema = "tbound-d09-xattr-enumeration-calibration/v2"
	D09XattrEnumerationProfile           = "fd-relative-listxattr-procfd/v1"
	D09XattrManagedRWSourceProfile       = "managed-live-root-rw-under-recovered-lease/v1"
	D09XattrReadonlySourceProfile        = "readonly-sealed-source/v1"
	d09XattrCalibrationDomain            = "tbound-d09-xattr-enumeration-calibration/v2\x00"
	d09ManagedSourceLeaseEvidenceDomain  = "tbound-d09-managed-source-lease-evidence/v1\x00"
	d09ManagedSnapshotEvidenceDomain     = "tbound-d09-managed-live-snapshot-evidence/v1\x00"
	d09ManagedRootXattrEvidenceDomain    = "tbound-d09-managed-live-xattr-evidence/v2\x00"
)

const (
	D09ManagedSourceRecoveryState = "recovered-no-unresolved-prior-runs/v1"
	D09ManagedSourceTaskState     = "no-task-admitted/v1"
	D09ManagedSourcePermitState   = "no-active-publication-permit/v1"
)

// D09XattrEnumerationCalibration is a separately signed operator qualification
// for one filesystem/mount/scanner/access profile. It records observed probe
// measurements, not a caller-set Complete/Trusted bit. Runtime APIs still scan
// the actual source descriptor and bind that observation to a live lease where
// the source is writable.
type D09XattrEnumerationCalibration struct {
	SchemaVersion           string `json:"schema_version"`
	Authority               string `json:"authority"`
	FilesystemMagic         uint64 `json:"filesystem_magic"`
	MountOptionsDigest      string `json:"mount_options_digest"`
	ScanUID                 int    `json:"scan_uid"`
	ScanGID                 int    `json:"scan_gid"`
	ScanPrivilegeProfile    string `json:"scan_privilege_profile"`
	SourceAccessProfile     string `json:"source_access_profile"`
	EnumerationProfile      string `json:"enumeration_profile"`
	VisibilityProfileDigest string `json:"visibility_profile_digest"`
	ProbeCorpusDigest       string `json:"probe_corpus_digest"`
	ProbeObservationDigest  string `json:"probe_observation_digest"`
	ProbeEntryCount         uint64 `json:"probe_entry_count"`
	ProbeXattrNameCount     uint64 `json:"probe_xattr_name_count"`
	Digest                  string `json:"digest"`
}

func (c D09XattrEnumerationCalibration) Validate() error {
	if c.SchemaVersion != D09XattrEnumerationCalibrationSchema || !ValidD09XattrVisibilityAuthority(c.Authority) || c.FilesystemMagic == 0 ||
		!validDigest(c.MountOptionsDigest) || c.ScanUID <= 0 || c.ScanGID <= 0 ||
		c.ScanPrivilegeProfile != D09XattrScanPrivilegeProfile ||
		(c.SourceAccessProfile != D09XattrManagedRWSourceProfile && c.SourceAccessProfile != D09XattrReadonlySourceProfile) ||
		c.EnumerationProfile != D09XattrEnumerationProfile || !validDigest(c.VisibilityProfileDigest) ||
		!validDigest(c.ProbeCorpusDigest) || !validDigest(c.ProbeObservationDigest) ||
		c.ProbeEntryCount == 0 || c.ProbeXattrNameCount == 0 || !validDigest(c.Digest) {
		return ErrD09TemplateInvalid
	}
	if c.VisibilityProfileDigest != DigestD09XattrVisibilityProfile(c.FilesystemMagic, c.MountOptionsDigest,
		c.ScanUID, c.ScanGID, c.ScanPrivilegeProfile) {
		return ErrD09TemplateInvalid
	}
	return nil
}

func D09XattrEnumerationCalibrationSigningBytes(calibration D09XattrEnumerationCalibration) ([]byte, error) {
	calibration.Digest = ""
	canonical, err := json.Marshal(calibration)
	if err != nil {
		return nil, err
	}
	return append([]byte(d09XattrCalibrationDomain), canonical...), nil
}

func SignD09XattrEnumerationCalibration(calibration D09XattrEnumerationCalibration,
	privateKey ed25519.PrivateKey) ([]byte, []byte, string, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, nil, "", ErrD09TemplateInvalid
	}
	calibration.SchemaVersion = D09XattrEnumerationCalibrationSchema
	message, err := D09XattrEnumerationCalibrationSigningBytes(calibration)
	if err != nil {
		return nil, nil, "", err
	}
	calibration.Digest = DigestBytes(message)
	if err := calibration.Validate(); err != nil {
		return nil, nil, "", err
	}
	message, err = D09XattrEnumerationCalibrationSigningBytes(calibration)
	if err != nil {
		return nil, nil, "", err
	}
	encoded, err := json.Marshal(calibration)
	if err != nil {
		return nil, nil, "", err
	}
	return encoded, ed25519.Sign(privateKey, message), calibration.Digest, nil
}

// ErrD09XattrCalibrationSignature reports that a well-formed xattr enumeration
// calibration did not verify under the supplied public key (wrong key, or a
// signature made over other bytes). It is distinct from a missing or invalid
// cell template, and its message says so. It still unwraps to
// ErrD09TemplateInvalid so existing fail-closed callers keep refusing.
var ErrD09XattrCalibrationSignature error = d09XattrCalibrationSignatureError{}

type d09XattrCalibrationSignatureError struct{}

func (d09XattrCalibrationSignatureError) Error() string {
	return "xattr calibration signature does not verify under the supplied public key (wrong signing key, or signature over different bytes)"
}

func (d09XattrCalibrationSignatureError) Unwrap() error { return ErrD09TemplateInvalid }

func VerifyD09XattrEnumerationCalibration(encoded, signature []byte, publicKey ed25519.PublicKey) (D09XattrEnumerationCalibration, error) {
	var calibration D09XattrEnumerationCalibration
	if len(encoded) == 0 || len(encoded) > 64<<10 || len(signature) != ed25519.SignatureSize ||
		len(publicKey) != ed25519.PublicKeySize || protocol.ValidateStrictJSON(encoded) != nil {
		return calibration, ErrD09TemplateInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&calibration); err != nil {
		return D09XattrEnumerationCalibration{}, errors.Join(ErrD09TemplateInvalid, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return D09XattrEnumerationCalibration{}, ErrD09TemplateInvalid
	}
	message, err := D09XattrEnumerationCalibrationSigningBytes(calibration)
	if err != nil || calibration.Digest != DigestBytes(message) {
		return D09XattrEnumerationCalibration{}, errors.Join(ErrD09TemplateInvalid, err)
	}
	if !ed25519.Verify(publicKey, message, signature) {
		return D09XattrEnumerationCalibration{}, ErrD09XattrCalibrationSignature
	}
	canonical, err := json.Marshal(calibration)
	if err != nil || !bytes.Equal(canonical, encoded) {
		return D09XattrEnumerationCalibration{}, errors.Join(ErrD09TemplateInvalid, err)
	}
	if err := calibration.Validate(); err != nil {
		return D09XattrEnumerationCalibration{}, err
	}
	return calibration, nil
}

// LoadVerifiedD09ManagedRWXattrEnumerationCalibration loads the fixed managed-RW
// calibration. It proves signature, schema, and role; the live-root binding is
// checked by LoadVerifiedD09ManagedRWXattrCalibrationForRegistration.
func LoadVerifiedD09ManagedRWXattrEnumerationCalibration(ctx context.Context) (D09XattrEnumerationCalibration, error) {
	return loadVerifiedD09XattrEnumerationCalibration(ctx, D09ManagedPiXattrCalibrationPath, D09ManagedPiXattrCalibrationSig,
		D09XattrManagedRWSourceProfile)
}

// LoadVerifiedD09ReadonlyXattrEnumerationCalibration loads the fixed read-only
// source calibration on which the cell template's visibility profile is measured.
func LoadVerifiedD09ReadonlyXattrEnumerationCalibration(ctx context.Context) (D09XattrEnumerationCalibration, error) {
	return loadVerifiedD09XattrEnumerationCalibration(ctx, D09ManagedPiROXattrCalibrationPath, D09ManagedPiROXattrCalibrationSig,
		D09XattrReadonlySourceProfile)
}

// LoadVerifiedD09ManagedRWXattrCalibrationForRegistration is the production
// managed-RW binding. The fixed signed calibration must match the signed
// registration's live-root identity; the RO template digest is not a comparator.
func LoadVerifiedD09ManagedRWXattrCalibrationForRegistration(ctx context.Context,
	registration D09ManagedLiveRootRegistration) (D09XattrEnumerationCalibration, error) {
	calibration, err := LoadVerifiedD09ManagedRWXattrEnumerationCalibration(ctx)
	if err != nil {
		return D09XattrEnumerationCalibration{}, err
	}
	if err := verifyD09ManagedRWXattrCalibrationBinding(calibration, registration); err != nil {
		return D09XattrEnumerationCalibration{}, err
	}
	return calibration, nil
}

func loadVerifiedD09XattrEnumerationCalibration(ctx context.Context, path, signaturePath, sourceProfile string) (D09XattrEnumerationCalibration, error) {
	if ctx == nil {
		return D09XattrEnumerationCalibration{}, ErrD09TemplateInvalid
	}
	if err := ctx.Err(); err != nil {
		return D09XattrEnumerationCalibration{}, err
	}
	encoded, err := readRootOwnedProfileFile(path, 64<<10)
	if err != nil {
		return D09XattrEnumerationCalibration{}, errors.Join(ErrD09TemplateInvalid, err)
	}
	signature, err := readRootOwnedProfileFile(signaturePath, ed25519.SignatureSize)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return D09XattrEnumerationCalibration{}, errors.Join(ErrD09TemplateInvalid, err)
	}
	publicKey, err := readProtectedLinuxHostProfilePublicKey()
	if err != nil {
		return D09XattrEnumerationCalibration{}, errors.Join(ErrD09TemplateInvalid, err)
	}
	return verifyD09XattrCalibrationForSource(encoded, signature, ed25519.PublicKey(publicKey), sourceProfile)
}

// verifyD09XattrCalibrationForSource verifies a signed calibration and refuses it
// unless it was measured for the requested source access profile. A managed-RW
// calibration cannot be swapped into the read-only role, or the reverse.
func verifyD09XattrCalibrationForSource(encoded, signature []byte, publicKey ed25519.PublicKey,
	sourceProfile string) (D09XattrEnumerationCalibration, error) {
	calibration, err := VerifyD09XattrEnumerationCalibration(encoded, signature, publicKey)
	if err != nil {
		return D09XattrEnumerationCalibration{}, err
	}
	if calibration.SourceAccessProfile != sourceProfile {
		return D09XattrEnumerationCalibration{}, ErrD09TemplateInvalid
	}
	return calibration, nil
}

// verifyD09ManagedRWXattrCalibrationBinding binds the managed-RW calibration to
// the signed registration. The expected visibility digest is recomputed from the
// live-root filesystem magic, mount options, service UID/GID and the closed
// scanner profile. It is not compared with the RO template digest: that digest
// measures the read-only runtime mount, and a different mount has different
// options, so the two digests are not expected to match.
func verifyD09ManagedRWXattrCalibrationBinding(calibration D09XattrEnumerationCalibration,
	registration D09ManagedLiveRootRegistration) error {
	expected := DigestD09XattrVisibilityProfile(registration.RootFilesystemMagic, registration.RootMountOptionsDigest,
		registration.ServiceUID, registration.ServiceGID, D09XattrScanPrivilegeProfile)
	if calibration.Validate() != nil || calibration.Digest != registration.ManagedRootXattrCalibrationDigest ||
		calibration.SourceAccessProfile != D09XattrManagedRWSourceProfile ||
		calibration.FilesystemMagic != registration.RootFilesystemMagic ||
		calibration.MountOptionsDigest != registration.RootMountOptionsDigest ||
		calibration.ScanUID != registration.ServiceUID || calibration.ScanGID != registration.ServiceGID ||
		calibration.ScanPrivilegeProfile != D09XattrScanPrivilegeProfile ||
		calibration.VisibilityProfileDigest != expected {
		return ErrD09TemplateInvalid
	}
	return nil
}

// D09ManagedSourceLeaseEvidence is a non-boolean C lease snapshot. Evidence is
// useful only when issued and revalidated by the same opaque held-epoch lease;
// a registration signature or caller-set quiescence flag cannot construct an
// admitted snapshot.
type D09ManagedSourceLeaseEvidence struct {
	SchemaVersion           string `json:"schema_version"`
	RunID                   string `json:"run_id"`
	WorkflowID              string `json:"workflow_id"`
	OwnerEpoch              uint64 `json:"owner_epoch"`
	RegistrationDigest      string `json:"registration_digest"`
	RootDevice              uint64 `json:"root_device"`
	RootInode               uint64 `json:"root_inode"`
	RootMountID             uint64 `json:"root_mount_id"`
	ServiceCgroupPath       string `json:"service_cgroup_path"`
	ServiceCgroupDevice     uint64 `json:"service_cgroup_device"`
	ServiceCgroupInode      uint64 `json:"service_cgroup_inode"`
	ServiceCgroupMountID    uint64 `json:"service_cgroup_mount_id"`
	RecoveryState           string `json:"recovery_state"`
	RecoveryRecordSequence  uint64 `json:"recovery_record_sequence"`
	RecoveryRecordHash      string `json:"recovery_record_hash"`
	LeaseJournalRecordCount uint64 `json:"lease_journal_record_count"`
	LeaseJournalHead        string `json:"lease_journal_head"`
	AdmissionSequence       uint64 `json:"admission_sequence"`
	AdmissionRecordHash     string `json:"admission_record_hash"`
	TaskState               string `json:"task_state"`
	TaskStateDigest         string `json:"task_state_digest"`
	PermitState             string `json:"permit_state"`
	PermitStateDigest       string `json:"permit_state_digest"`
	EvidenceDigest          string `json:"evidence_digest"`
}

// D09ManagedSourceLease is C's opaque recovered writer-epoch capability. Its
// evidence methods read/revalidate durable lease state and reject stale epochs.
type D09ManagedSourceLease interface {
	D09ManagedSourceLeaseEvidence(context.Context, *VerifiedD09ManagedLiveRoot) (D09ManagedSourceLeaseEvidence, error)
	VerifyD09ManagedSourceLease(context.Context, *VerifiedD09ManagedLiveRoot, D09ManagedSourceLeaseEvidence) error
}

func NewD09ManagedSourceLeaseEvidence(evidence D09ManagedSourceLeaseEvidence) (D09ManagedSourceLeaseEvidence, error) {
	evidence.SchemaVersion = "tbound-d09-managed-source-lease-evidence/v1"
	evidence.EvidenceDigest = ""
	message, err := d09ManagedSourceLeaseEvidenceBytes(evidence)
	if err != nil {
		return D09ManagedSourceLeaseEvidence{}, err
	}
	evidence.EvidenceDigest = DigestBytes(message)
	if err := evidence.Validate(); err != nil {
		return D09ManagedSourceLeaseEvidence{}, err
	}
	return evidence, nil
}

func (e D09ManagedSourceLeaseEvidence) Validate() error {
	if e.SchemaVersion != "tbound-d09-managed-source-lease-evidence/v1" ||
		!validIdentity(e.RunID) || !validIdentity(e.WorkflowID) || e.OwnerEpoch == 0 ||
		!validDigest(e.RegistrationDigest) || e.RootDevice == 0 || e.RootInode == 0 || e.RootMountID == 0 ||
		!canonicalAbsolutePath(e.ServiceCgroupPath) || e.ServiceCgroupDevice == 0 ||
		e.ServiceCgroupInode == 0 || e.ServiceCgroupMountID == 0 ||
		e.RecoveryState != D09ManagedSourceRecoveryState || e.RecoveryRecordSequence == 0 ||
		!validDigest(e.RecoveryRecordHash) || e.LeaseJournalRecordCount == 0 || !validDigest(e.LeaseJournalHead) ||
		e.AdmissionSequence == 0 || !validDigest(e.AdmissionRecordHash) || e.TaskState != D09ManagedSourceTaskState ||
		!validDigest(e.TaskStateDigest) || e.PermitState != D09ManagedSourcePermitState ||
		!validDigest(e.PermitStateDigest) || !validDigest(e.EvidenceDigest) {
		return ErrD09ManagedLiveRootRefused
	}
	message, err := d09ManagedSourceLeaseEvidenceBytes(e)
	if err != nil || e.EvidenceDigest != DigestBytes(message) {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	return nil
}

func d09ManagedSourceLeaseEvidenceBytes(evidence D09ManagedSourceLeaseEvidence) ([]byte, error) {
	evidence.EvidenceDigest = ""
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return nil, err
	}
	return append([]byte(d09ManagedSourceLeaseEvidenceDomain), encoded...), nil
}

// D09ManagedLiveRootSnapshotEvidence is source-only evidence for a fresh
// filtered Store seed; it is never a worker exposure or inherited descriptor.
type D09ManagedLiveRootSnapshotEvidence struct {
	SchemaVersion       string `json:"schema_version"`
	RunID               string `json:"run_id"`
	OwnerEpoch          uint64 `json:"owner_epoch"`
	RegistrationDigest  string `json:"registration_digest"`
	RootDevice          uint64 `json:"root_device"`
	RootInode           uint64 `json:"root_inode"`
	RootMountID         uint64 `json:"root_mount_id"`
	LeaseEvidenceDigest string `json:"lease_evidence_digest"`
	EntryCount          uint64 `json:"entry_count"`
	DirectoryCount      uint64 `json:"directory_count"`
	XattrNameCount      uint64 `json:"xattr_name_count"`
	ObservationDigest   string `json:"observation_digest"`
	EvidenceDigest      string `json:"evidence_digest"`
}

// D09ManagedRootXattrEvidence is measured visibility evidence for the actual
// writable root. It contains no Complete/Quiescent boolean.
type D09ManagedRootXattrEvidence struct {
	SchemaVersion        string `json:"schema_version"`
	Authority            string `json:"authority"`
	RunID                string `json:"run_id"`
	OwnerEpoch           uint64 `json:"owner_epoch"`
	RegistrationDigest   string `json:"registration_digest"`
	CalibrationDigest    string `json:"calibration_digest"`
	LeaseEvidenceDigest  string `json:"lease_evidence_digest"`
	RootDevice           uint64 `json:"root_device"`
	RootInode            uint64 `json:"root_inode"`
	RootMountID          uint64 `json:"root_mount_id"`
	FilesystemMagic      uint64 `json:"filesystem_magic"`
	MountOptionsDigest   string `json:"mount_options_digest"`
	ScanUID              int    `json:"scan_uid"`
	ScanGID              int    `json:"scan_gid"`
	ScanPrivilegeProfile string `json:"scan_privilege_profile"`
	EntryCount           uint64 `json:"entry_count"`
	DirectoryCount       uint64 `json:"directory_count"`
	XattrNameCount       uint64 `json:"xattr_name_count"`
	ObservationDigest    string `json:"observation_digest"`
	EvidenceDigest       string `json:"evidence_digest"`
}

func (e D09ManagedLiveRootSnapshotEvidence) Validate() error {
	if e.SchemaVersion != "tbound-d09-managed-live-snapshot-evidence/v1" || !validIdentity(e.RunID) ||
		e.OwnerEpoch == 0 || !validDigest(e.RegistrationDigest) || e.RootDevice == 0 || e.RootInode == 0 ||
		e.RootMountID == 0 || !validDigest(e.LeaseEvidenceDigest) || e.EntryCount == 0 ||
		e.DirectoryCount == 0 || !validDigest(e.ObservationDigest) || !validDigest(e.EvidenceDigest) {
		return ErrD09ManagedLiveRootRefused
	}
	message, err := d09ManagedSnapshotEvidenceBytes(e)
	if err != nil || e.EvidenceDigest != DigestBytes(message) {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	return nil
}

func d09ManagedSnapshotEvidenceBytes(e D09ManagedLiveRootSnapshotEvidence) ([]byte, error) {
	e.EvidenceDigest = ""
	encoded, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	return append([]byte(d09ManagedSnapshotEvidenceDomain), encoded...), nil
}

func (e D09ManagedRootXattrEvidence) Validate() error {
	if e.SchemaVersion != "tbound-d09-managed-live-xattr-evidence/v2" || !ValidD09XattrVisibilityAuthority(e.Authority) || !validIdentity(e.RunID) ||
		e.OwnerEpoch == 0 || !validDigest(e.RegistrationDigest) || !validDigest(e.CalibrationDigest) ||
		!validDigest(e.LeaseEvidenceDigest) || e.RootDevice == 0 || e.RootInode == 0 || e.RootMountID == 0 ||
		e.FilesystemMagic == 0 || !validDigest(e.MountOptionsDigest) || e.ScanUID <= 0 || e.ScanGID <= 0 ||
		e.ScanPrivilegeProfile != D09XattrScanPrivilegeProfile || e.EntryCount == 0 || e.DirectoryCount == 0 ||
		!validDigest(e.ObservationDigest) || !validDigest(e.EvidenceDigest) {
		return ErrD09ManagedLiveRootRefused
	}
	message, err := d09ManagedRootXattrEvidenceBytes(e)
	if err != nil || e.EvidenceDigest != DigestBytes(message) {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	return nil
}

func d09ManagedRootXattrEvidenceBytes(e D09ManagedRootXattrEvidence) ([]byte, error) {
	e.EvidenceDigest = ""
	encoded, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	return append([]byte(d09ManagedRootXattrEvidenceDomain), encoded...), nil
}

type rootSnapshotQualificationContract interface {
	RevalidateRootSnapshot(context.Context, *os.File) error
	SnapshotEvidenceDigest() string
}

var _ rootSnapshotQualificationContract = (*D09ManagedLiveRootSnapshotQualification)(nil)

// D09ManagedLiveRootSnapshotQualification is an opaque A-issued capability for
// one measured source descriptor under one recovered C lease. It retains only
// identities/evidence and revalidators; it never exports a Pi descriptor.
type D09ManagedLiveRootSnapshotQualification struct {
	verified      *VerifiedD09ManagedLiveRoot
	lease         D09ManagedSourceLease
	profile       HostProfile
	template      D09CellTemplate
	snapshot      D09ManagedLiveRootSnapshotEvidence
	xattr         D09ManagedRootXattrEvidence
	leaseEvidence D09ManagedSourceLeaseEvidence
	sourceDevice  uint64
	sourceInode   uint64
	sourceMountID uint64
	digest        string
}

func D09ManagedLiveRootSnapshotEvidenceForSeed(ctx context.Context, verified *VerifiedD09ManagedLiveRoot,
	candidate *os.File, profile HostProfile, template D09CellTemplate,
	lease D09ManagedSourceLease) (*D09ManagedLiveRootSnapshotQualification, error) {
	return QualifyD09ManagedLiveRootSeedSource(ctx, verified, candidate, profile, template, lease)
}

// QualifyD09ManagedLiveRootSeedSource is the combined typed source gate for C's
// fresh filtered Store seed. It requires the current recovered lease, the
// registered managed RW-root calibration, the actual scanner identity and a
// descriptor-rooted enumeration. No RootAttestation.Quiescent or xattr
// Complete bit is constructed here.
func QualifyD09ManagedLiveRootSeedSource(ctx context.Context, verified *VerifiedD09ManagedLiveRoot,
	candidate *os.File, profile HostProfile, template D09CellTemplate,
	lease D09ManagedSourceLease) (*D09ManagedLiveRootSnapshotQualification, error) {
	if ctx == nil || verified == nil || candidate == nil || lease == nil {
		return nil, ErrD09ManagedLiveRootRefused
	}
	registration, _, leaseEvidence, err := verifyD09ManagedLeaseEvidence(ctx, verified, lease)
	if err != nil {
		return nil, err
	}
	xattrEvidence, err := MeasureD09ManagedLiveRootXattrs(ctx, verified, candidate, profile, template, lease)
	if err != nil || xattrEvidence.LeaseEvidenceDigest != leaseEvidence.EvidenceDigest {
		return nil, errors.Join(ErrD09ManagedLiveRootRefused, err,
			errors.New("managed-root xattr measurement used a different lease evidence snapshot"))
	}
	stat, err := statD09QualificationSource(candidate)
	if err != nil || stat.device != registration.RootDevice || stat.inode != registration.RootInode ||
		stat.mountID != leaseEvidence.RootMountID {
		return nil, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	snapshot := D09ManagedLiveRootSnapshotEvidence{
		SchemaVersion: "tbound-d09-managed-live-snapshot-evidence/v1", RunID: leaseEvidence.RunID,
		OwnerEpoch: leaseEvidence.OwnerEpoch, RegistrationDigest: registration.Digest,
		RootDevice: registration.RootDevice, RootInode: registration.RootInode, RootMountID: leaseEvidence.RootMountID,
		LeaseEvidenceDigest: leaseEvidence.EvidenceDigest, EntryCount: xattrEvidence.EntryCount,
		DirectoryCount: xattrEvidence.DirectoryCount, XattrNameCount: xattrEvidence.XattrNameCount,
		ObservationDigest: xattrEvidence.ObservationDigest,
	}
	snapshotBytes, err := d09ManagedSnapshotEvidenceBytes(snapshot)
	if err != nil {
		return nil, err
	}
	snapshot.EvidenceDigest = DigestBytes(snapshotBytes)
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	combined := struct {
		Domain              string `json:"domain"`
		LeaseEvidenceDigest string `json:"lease_evidence_digest"`
		SnapshotEvidence    string `json:"snapshot_evidence_digest"`
		XattrEvidence       string `json:"xattr_evidence_digest"`
		SourceDevice        uint64 `json:"source_device"`
		SourceInode         uint64 `json:"source_inode"`
		SourceMountID       uint64 `json:"source_mount_id"`
	}{"tbound-d09-managed-live-snapshot-qualification/v1", leaseEvidence.EvidenceDigest,
		snapshot.EvidenceDigest, xattrEvidence.EvidenceDigest, stat.device, stat.inode, stat.mountID}
	encoded, err := json.Marshal(combined)
	if err != nil {
		return nil, err
	}
	return &D09ManagedLiveRootSnapshotQualification{
		verified: verified, lease: lease, profile: profile, template: template,
		snapshot: snapshot, xattr: xattrEvidence, leaseEvidence: leaseEvidence,
		sourceDevice: stat.device, sourceInode: stat.inode, sourceMountID: stat.mountID,
		digest: DigestBytes(encoded),
	}, nil
}

func (q *D09ManagedLiveRootSnapshotQualification) SnapshotEvidenceDigest() string {
	if q == nil || !validDigest(q.digest) {
		return ""
	}
	return q.digest
}

func (q *D09ManagedLiveRootSnapshotQualification) SnapshotEvidence() (D09ManagedLiveRootSnapshotEvidence, error) {
	if q == nil || q.snapshot.Validate() != nil || !validDigest(q.digest) {
		return D09ManagedLiveRootSnapshotEvidence{}, ErrD09ManagedLiveRootRefused
	}
	return q.snapshot, nil
}

func (q *D09ManagedLiveRootSnapshotQualification) XattrEvidence() (D09ManagedRootXattrEvidence, error) {
	if q == nil || q.xattr.Validate() != nil || !validDigest(q.digest) {
		return D09ManagedRootXattrEvidence{}, ErrD09ManagedLiveRootRefused
	}
	return q.xattr, nil
}

// XattrVisibilityAttestation converts an opaque managed-root qualification into
// the workspace import attestation, carrying the evidence's bounded Authority.
// Callers cannot synthesize it: the
// qualification is issued only after the signed RW calibration and current
// recovered source lease have both been checked.
func (q *D09ManagedLiveRootSnapshotQualification) XattrVisibilityAttestation() (workspace.XattrVisibilityAttestation, error) {
	evidence, err := q.XattrEvidence()
	if err != nil {
		return workspace.XattrVisibilityAttestation{}, err
	}
	return WorkspaceXattrAttestation(evidence.CalibrationDigest, evidence.Authority)
}

// WorkspaceXattrAttestation is the single place a piruntime xattr result becomes
// a workspace attestation. It forwards the recorded authority label and admits
// only the bounded authority (ValidD09XattrVisibilityAuthority); an empty or
// unknown label, or a malformed profile digest, yields no attestation. It never
// asserts complete enumeration.
func WorkspaceXattrAttestation(profileDigest, authority string) (workspace.XattrVisibilityAttestation, error) {
	if !validDigest(profileDigest) || !ValidD09XattrVisibilityAuthority(authority) {
		return workspace.XattrVisibilityAttestation{}, errors.New("xattr visibility result does not carry the bounded root-attested authority")
	}
	return workspace.XattrVisibilityAttestation{ProfileDigest: profileDigest, Authority: workspace.XattrVisibilityAuthority(authority)}, nil
}

// RevalidateRootSnapshot implements sessionrepo's narrow structural bridge.
// B calls it before its descriptor-rooted source scan and after workspace.Import;
// C keeps the same writer lease active across both calls.
func (q *D09ManagedLiveRootSnapshotQualification) RevalidateRootSnapshot(ctx context.Context, source *os.File) error {
	if q == nil || ctx == nil || source == nil || q.verified == nil || q.lease == nil ||
		q.snapshot.Validate() != nil || q.xattr.Validate() != nil || !validDigest(q.digest) {
		return ErrD09ManagedLiveRootRefused
	}
	stat, err := statD09QualificationSource(source)
	if err != nil || stat.device != q.sourceDevice || stat.inode != q.sourceInode || stat.mountID != q.sourceMountID {
		return errors.Join(ErrD09ManagedLiveRootRefused, err,
			errors.New("seed source descriptor differs from the qualified managed-root identity"))
	}
	if err := q.verified.Revalidate(ctx, source); err != nil {
		return err
	}
	observed, err := MeasureD09ManagedLiveRootXattrs(ctx, q.verified, source, q.profile, q.template, q.lease)
	if err != nil || observed != q.xattr || observed.LeaseEvidenceDigest != q.leaseEvidence.EvidenceDigest {
		return errors.Join(ErrD09ManagedLiveRootRefused, err,
			errors.New("managed-root source, lease, calibration, or enumeration changed after qualification"))
	}
	return nil
}

type d09QualificationSourceIdentity struct {
	device  uint64
	inode   uint64
	mountID uint64
}

func statD09QualificationSource(source *os.File) (d09QualificationSourceIdentity, error) {
	var identity d09QualificationSourceIdentity
	if source == nil {
		return identity, ErrD09ManagedLiveRootRefused
	}
	info, err := source.Stat()
	if err != nil || !info.IsDir() {
		return identity, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return identity, ErrD09ManagedLiveRootRefused
	}
	mountID, err := descriptorMountID(source.Fd())
	if err != nil {
		return identity, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	identity = d09QualificationSourceIdentity{device: uint64(stat.Dev), inode: stat.Ino, mountID: mountID}
	return identity, nil
}

// ValidD09Digest reports whether value is a canonical lowercase SHA-256 digest.
func ValidD09Digest(value string) bool { return validDigest(value) }

// D09XattrVisibilityMeasurements is the observed filesystem/mount/scanner
// identity of a protected source descriptor. It is a measurement, not an
// attestation by itself.
type D09XattrVisibilityMeasurements struct {
	FilesystemMagic       uint64 `json:"filesystem_magic"`
	MountOptionsDigest    string `json:"mount_options_digest"`
	ScanUID               int    `json:"scan_uid"`
	ScanGID               int    `json:"scan_gid"`
	ScanPrivilegeProfile  string `json:"scan_privilege_profile"`
	EffectiveCapabilities string `json:"effective_capabilities"`
	NoNewPrivs            bool   `json:"no_new_privs"`
	InitialUserNamespace  bool   `json:"initial_user_namespace"`
	ReadOnly              bool   `json:"read_only"`
	RootOwned             bool   `json:"root_owned"`
}

// D09XattrEnumerationObservation is a read-only observation of a descriptor-
// rooted tree walk. It is not an attestation and contains no completeness bit.
type D09XattrEnumerationObservation struct {
	EntryCount        uint64 `json:"entry_count"`
	DirectoryCount    uint64 `json:"directory_count"`
	XattrNameCount    uint64 `json:"xattr_name_count"`
	ObservationDigest string `json:"observation_digest"`
}

type d09XattrPendingNode struct {
	file  *os.File
	rel   string
	depth int
}

// MeasureD09XattrEnumeration observes entries and xattr names from one held
// directory descriptor. It never produces workspace authorization; callers
// need the separately signed calibration plus live source/lease validation.
func MeasureD09XattrEnumeration(source *os.File) (D09XattrEnumerationObservation, error) {
	var observation D09XattrEnumerationObservation
	if source == nil {
		return observation, ErrD09TemplateInvalid
	}
	rootFD, err := syscall.Openat(int(source.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return observation, err
	}
	root := os.NewFile(uintptr(rootFD), "d09-xattr-enumeration-root")
	defer root.Close()
	queue := []d09XattrPendingNode{{file: root, rel: ".", depth: 0}}
	defer func() {
		for _, pending := range queue {
			if pending.file != nil && pending.file != root {
				_ = pending.file.Close()
			}
		}
	}()
	var transcript bytes.Buffer
	const maxEntries = 1 << 20
	const maxDepth = 4096
	for len(queue) > 0 {
		item := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		info, statErr := item.file.Stat()
		if statErr != nil {
			if item.file != root {
				_ = item.file.Close()
			}
			return D09XattrEnumerationObservation{}, errors.Join(ErrD09TemplateInvalid, statErr)
		}
		mode := info.Mode()
		if !mode.IsDir() && !mode.IsRegular() {
			if item.file != root {
				_ = item.file.Close()
			}
			return D09XattrEnumerationObservation{}, fmt.Errorf("unsupported node type in D09 xattr enumeration at %q", item.rel)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (mode.IsRegular() && stat.Nlink != 1) {
			if item.file != root {
				_ = item.file.Close()
			}
			return D09XattrEnumerationObservation{}, fmt.Errorf("unmeasured or multiply-linked node in D09 xattr enumeration at %q", item.rel)
		}
		attributes, listErr := listD09DescriptorXattrs(item.file)
		if listErr != nil {
			if item.file != root {
				_ = item.file.Close()
			}
			return D09XattrEnumerationObservation{}, errors.Join(ErrD09TemplateInvalid, listErr)
		}
		observation.EntryCount++
		if mode.IsDir() {
			observation.DirectoryCount++
		}
		observation.XattrNameCount += uint64(len(attributes))
		fmt.Fprintf(&transcript, "%s\x00%d\x00%d\x00%d\x00%o\x00%d\x00", item.rel,
			uint64(stat.Dev), stat.Ino, uint64(mode), stat.Mode&0o7777, stat.Nlink)
		for _, name := range attributes {
			transcript.WriteString(name)
			transcript.WriteByte(0)
		}
		if mode.IsDir() {
			if item.depth >= maxDepth {
				if item.file != root {
					_ = item.file.Close()
				}
				return D09XattrEnumerationObservation{}, fmt.Errorf("D09 xattr enumeration exceeds maximum tree depth")
			}
			entries, readErr := item.file.ReadDir(-1)
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				if item.file != root {
					_ = item.file.Close()
				}
				return D09XattrEnumerationObservation{}, errors.Join(ErrD09TemplateInvalid, readErr)
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
			for index := len(entries) - 1; index >= 0; index-- {
				name := entries[index].Name()
				if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
					if item.file != root {
						_ = item.file.Close()
					}
					return D09XattrEnumerationObservation{}, fmt.Errorf("invalid directory entry in D09 xattr enumeration")
				}
				fd, openErr := syscall.Openat(int(item.file.Fd()), name,
					syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
				if openErr != nil {
					if item.file != root {
						_ = item.file.Close()
					}
					return D09XattrEnumerationObservation{}, errors.Join(ErrD09TemplateInvalid, openErr)
				}
				child := os.NewFile(uintptr(fd), name)
				queue = append(queue, d09XattrPendingNode{file: child, rel: filepath.Join(item.rel, name), depth: item.depth + 1})
				if len(queue)+int(observation.EntryCount) > maxEntries {
					_ = child.Close()
					if item.file != root {
						_ = item.file.Close()
					}
					return D09XattrEnumerationObservation{}, fmt.Errorf("D09 xattr enumeration exceeds maximum entry count")
				}
			}
		}
		if item.file != root {
			_ = item.file.Close()
		}
	}
	if observation.EntryCount == 0 || observation.DirectoryCount == 0 {
		return D09XattrEnumerationObservation{}, ErrD09TemplateInvalid
	}
	observation.ObservationDigest = DigestBytes(transcript.Bytes())
	return observation, nil
}

func listD09DescriptorXattrs(file *os.File) ([]string, error) {
	if file == nil {
		return nil, ErrD09TemplateInvalid
	}
	path := fmt.Sprintf("/proc/self/fd/%d", file.Fd())
	size, err := syscall.Listxattr(path, nil)
	if err != nil || size < 0 || size > 1<<20 {
		return nil, errors.Join(ErrD09TemplateInvalid, err)
	}
	if size == 0 {
		return []string{}, nil
	}
	buffer := make([]byte, size)
	observed, err := syscall.Listxattr(path, buffer)
	if err != nil || observed != size || buffer[size-1] != 0 {
		return nil, errors.Join(ErrD09TemplateInvalid, err)
	}
	attributes := strings.Split(string(buffer[:observed-1]), "\x00")
	sort.Strings(attributes)
	for index, attribute := range attributes {
		if attribute == "" || (index > 0 && attributes[index-1] == attribute) {
			return nil, ErrD09TemplateInvalid
		}
	}
	return attributes, nil
}

func verifyD09ManagedLeaseEvidence(ctx context.Context, verified *VerifiedD09ManagedLiveRoot,
	lease D09ManagedSourceLease) (D09ManagedLiveRootRegistration, D09ManagedLiveRootCgroupIdentity,
	D09ManagedSourceLeaseEvidence, error) {
	if ctx == nil || verified == nil || lease == nil {
		return D09ManagedLiveRootRegistration{}, D09ManagedLiveRootCgroupIdentity{},
			D09ManagedSourceLeaseEvidence{}, ErrD09ManagedLiveRootRefused
	}
	registration := verified.Registration()
	cgroup, ok := verified.CgroupIdentity()
	if !ok || registration.Digest == "" || cgroup.Inode == 0 || cgroup.MountID == 0 {
		return D09ManagedLiveRootRegistration{}, D09ManagedLiveRootCgroupIdentity{},
			D09ManagedSourceLeaseEvidence{}, ErrD09ManagedLiveRootRefused
	}
	rootMountID, mountErr := verified.rootMountID()
	if mountErr != nil {
		return D09ManagedLiveRootRegistration{}, D09ManagedLiveRootCgroupIdentity{},
			D09ManagedSourceLeaseEvidence{}, errors.Join(ErrD09ManagedLiveRootRefused, mountErr)
	}
	evidence, err := lease.D09ManagedSourceLeaseEvidence(ctx, verified)
	if err != nil || evidence.Validate() != nil || evidence.RegistrationDigest != registration.Digest ||
		evidence.RootDevice != registration.RootDevice || evidence.RootInode != registration.RootInode ||
		evidence.RootMountID != rootMountID || evidence.ServiceCgroupPath != cgroup.Path ||
		evidence.ServiceCgroupDevice != cgroup.Device || evidence.ServiceCgroupInode != cgroup.Inode ||
		evidence.ServiceCgroupMountID != cgroup.MountID {
		return D09ManagedLiveRootRegistration{}, D09ManagedLiveRootCgroupIdentity{},
			D09ManagedSourceLeaseEvidence{}, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	if err := lease.VerifyD09ManagedSourceLease(ctx, verified, evidence); err != nil {
		return D09ManagedLiveRootRegistration{}, D09ManagedLiveRootCgroupIdentity{},
			D09ManagedSourceLeaseEvidence{}, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	return registration, cgroup, evidence, nil
}

// QualifyD09ManagedLiveRootSnapshot proves only that this exact current managed
// root was stable under C's held/recovered lease during the descriptor-rooted
// inventory. It creates neither a Store generation nor a Pi-facing descriptor.
func QualifyD09ManagedLiveRootSnapshot(ctx context.Context, verified *VerifiedD09ManagedLiveRoot,
	candidate *os.File, lease D09ManagedSourceLease) (D09ManagedLiveRootSnapshotEvidence, error) {
	if ctx == nil || verified == nil || candidate == nil || lease == nil {
		return D09ManagedLiveRootSnapshotEvidence{}, ErrD09ManagedLiveRootRefused
	}
	if err := verified.Revalidate(ctx, candidate); err != nil {
		return D09ManagedLiveRootSnapshotEvidence{}, err
	}
	registration, _, leaseEvidence, err := verifyD09ManagedLeaseEvidence(ctx, verified, lease)
	if err != nil {
		return D09ManagedLiveRootSnapshotEvidence{}, err
	}
	observation, err := MeasureD09XattrEnumeration(candidate)
	if err != nil {
		return D09ManagedLiveRootSnapshotEvidence{}, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	if err := lease.VerifyD09ManagedSourceLease(ctx, verified, leaseEvidence); err != nil {
		return D09ManagedLiveRootSnapshotEvidence{}, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	if err := verified.Revalidate(ctx, candidate); err != nil {
		return D09ManagedLiveRootSnapshotEvidence{}, err
	}
	evidence := D09ManagedLiveRootSnapshotEvidence{
		SchemaVersion: "tbound-d09-managed-live-snapshot-evidence/v1", RunID: leaseEvidence.RunID,
		OwnerEpoch: leaseEvidence.OwnerEpoch, RegistrationDigest: registration.Digest,
		RootDevice: registration.RootDevice, RootInode: registration.RootInode, RootMountID: leaseEvidence.RootMountID,
		LeaseEvidenceDigest: leaseEvidence.EvidenceDigest, EntryCount: observation.EntryCount,
		DirectoryCount: observation.DirectoryCount, XattrNameCount: observation.XattrNameCount,
		ObservationDigest: observation.ObservationDigest,
	}
	message, err := d09ManagedSnapshotEvidenceBytes(evidence)
	if err != nil {
		return D09ManagedLiveRootSnapshotEvidence{}, err
	}
	evidence.EvidenceDigest = DigestBytes(message)
	if err := evidence.Validate(); err != nil {
		return D09ManagedLiveRootSnapshotEvidence{}, err
	}
	return evidence, nil
}

// VerifyD09ManagedLiveRootSnapshot remeasures the current source and requires
// the same lease epoch/head/root inventory, allowing C to bracket its filtered
// Store seed without converting evidence into a Quiescent boolean.
func VerifyD09ManagedLiveRootSnapshot(ctx context.Context, verified *VerifiedD09ManagedLiveRoot,
	candidate *os.File, lease D09ManagedSourceLease, expected D09ManagedLiveRootSnapshotEvidence) error {
	if err := expected.Validate(); err != nil {
		return err
	}
	observed, err := QualifyD09ManagedLiveRootSnapshot(ctx, verified, candidate, lease)
	if err != nil || observed != expected {
		return errors.Join(ErrD09ManagedLiveRootRefused, err, errors.New("managed live-root snapshot evidence changed"))
	}
	return nil
}

// MeasureD09ManagedLiveRootXattrs performs the managed-RW measurement only
// under a current verified registration and C's recovered opaque writer lease.
// It never returns workspace Quiescent/authority flags.
func MeasureD09ManagedLiveRootXattrs(ctx context.Context, verified *VerifiedD09ManagedLiveRoot,
	candidate *os.File, profile HostProfile, template D09CellTemplate,
	lease D09ManagedSourceLease) (D09ManagedRootXattrEvidence, error) {
	if ctx == nil || candidate == nil || verified == nil || lease == nil ||
		profile.Mode != ModeProduction || profile.ContainmentProfile != D09ContainmentProfile || !template.registered {
		return D09ManagedRootXattrEvidence{}, ErrD09ManagedLiveRootRefused
	}
	trustedProfile, err := LoadLinuxSignedHostProfile("")
	if err != nil || !reflect.DeepEqual(profile, trustedProfile) {
		return D09ManagedRootXattrEvidence{}, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	trustedTemplate, _, err := LoadD09CellTemplate(ctx)
	if err != nil || !reflect.DeepEqual(template, trustedTemplate) {
		return D09ManagedRootXattrEvidence{}, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	templateDigest, err := template.Digest()
	if err != nil || templateDigest != profile.ContainmentProfileDigest {
		return D09ManagedRootXattrEvidence{}, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	if err := verified.Revalidate(ctx, candidate); err != nil {
		return D09ManagedRootXattrEvidence{}, err
	}
	registration, _, leaseEvidence, err := verifyD09ManagedLeaseEvidence(ctx, verified, lease)
	if err != nil {
		return D09ManagedRootXattrEvidence{}, err
	}
	calibration, err := LoadVerifiedD09ManagedRWXattrCalibrationForRegistration(ctx, registration)
	if err != nil {
		return D09ManagedRootXattrEvidence{}, errors.Join(ErrD09ManagedLiveRootRefused, err,
			errors.New("managed RW source lacks a matching separately signed enumeration calibration"))
	}
	measurements, err := MeasureD09XattrScannerIdentity(candidate)
	if err != nil || measurements.ReadOnly || measurements.FilesystemMagic != calibration.FilesystemMagic ||
		measurements.MountOptionsDigest != calibration.MountOptionsDigest ||
		measurements.ScanUID != calibration.ScanUID || measurements.ScanGID != calibration.ScanGID ||
		measurements.ScanPrivilegeProfile != calibration.ScanPrivilegeProfile ||
		measurements.EffectiveCapabilities != d09EmptyCapabilitySet || !measurements.NoNewPrivs || !measurements.InitialUserNamespace {
		return D09ManagedRootXattrEvidence{}, errors.Join(ErrD09ManagedLiveRootRefused, err,
			errors.New("actual managed RW scanner identity differs from signed enumeration calibration"))
	}
	observation, err := MeasureD09XattrEnumeration(candidate)
	if err != nil {
		return D09ManagedRootXattrEvidence{}, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	if err := lease.VerifyD09ManagedSourceLease(ctx, verified, leaseEvidence); err != nil {
		return D09ManagedRootXattrEvidence{}, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	if err := verified.Revalidate(ctx, candidate); err != nil {
		return D09ManagedRootXattrEvidence{}, err
	}
	evidence := D09ManagedRootXattrEvidence{
		SchemaVersion: "tbound-d09-managed-live-xattr-evidence/v2", Authority: calibration.Authority, RunID: leaseEvidence.RunID,
		OwnerEpoch: leaseEvidence.OwnerEpoch, RegistrationDigest: registration.Digest,
		CalibrationDigest: calibration.Digest, LeaseEvidenceDigest: leaseEvidence.EvidenceDigest,
		RootDevice: registration.RootDevice, RootInode: registration.RootInode, RootMountID: leaseEvidence.RootMountID,
		FilesystemMagic: measurements.FilesystemMagic, MountOptionsDigest: measurements.MountOptionsDigest,
		ScanUID: measurements.ScanUID, ScanGID: measurements.ScanGID,
		ScanPrivilegeProfile: measurements.ScanPrivilegeProfile,
		EntryCount:           observation.EntryCount, DirectoryCount: observation.DirectoryCount,
		XattrNameCount: observation.XattrNameCount, ObservationDigest: observation.ObservationDigest,
	}
	message, err := d09ManagedRootXattrEvidenceBytes(evidence)
	if err != nil {
		return D09ManagedRootXattrEvidence{}, err
	}
	evidence.EvidenceDigest = DigestBytes(message)
	if err := evidence.Validate(); err != nil {
		return D09ManagedRootXattrEvidence{}, err
	}
	return evidence, nil
}

// ValidD09Digest reports whether value is a canonical lowercase SHA-256 digest.

// MeasureD09XattrVisibility records the descriptor's filesystem type, actual
// read-only mount options and effective scanner uid/gid. It is observation only
// and cannot itself produce a workspace attestation.
func MeasureD09XattrVisibility(source *os.File) (D09XattrVisibilityMeasurements, error) {
	return MeasureD09XattrScannerIdentity(source)
}

// MeasureD09XattrScannerIdentity observes filesystem/mount/scanner properties
// for either RO or RW roots. It never claims that xattr names were completely
// visible; callers must also use a signed enumeration calibration and scan the
// actual tree under the required live lease.
func MeasureD09XattrScannerIdentity(source *os.File) (D09XattrVisibilityMeasurements, error) {
	if source == nil {
		return D09XattrVisibilityMeasurements{}, ErrD09TemplateInvalid
	}
	info, err := source.Stat()
	if err != nil || !info.IsDir() {
		return D09XattrVisibilityMeasurements{}, errors.Join(ErrD09TemplateInvalid, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return D09XattrVisibilityMeasurements{}, ErrD09TemplateInvalid
	}
	var fs syscall.Statfs_t
	if err := syscall.Fstatfs(int(source.Fd()), &fs); err != nil {
		return D09XattrVisibilityMeasurements{}, err
	}
	mountID, err := descriptorMountID(source.Fd())
	if err != nil {
		return D09XattrVisibilityMeasurements{}, err
	}
	mountOptions, readOnly, err := mountOptionsForID(mountID)
	if err != nil {
		return D09XattrVisibilityMeasurements{}, err
	}
	privilegeProfile, capEff, noNewPrivs, initialUserNS, err := measureXattrScannerPrivilege()
	if err != nil {
		return D09XattrVisibilityMeasurements{}, err
	}
	return D09XattrVisibilityMeasurements{
		FilesystemMagic: uint64(fs.Type), MountOptionsDigest: DigestBytes([]byte(mountOptions)),
		ScanUID: os.Geteuid(), ScanGID: os.Getegid(), ScanPrivilegeProfile: privilegeProfile,
		EffectiveCapabilities: capEff, NoNewPrivs: noNewPrivs, InitialUserNamespace: initialUserNS, ReadOnly: readOnly,
		RootOwned: stat.Uid == 0 && info.Mode().Perm()&0o222 == 0,
	}, nil
}

// MeasureD09XattrEnumerationCalibration observes an explicitly named calibration
// corpus on the target filesystem/scanner/access profile. It returns unsigned
// measurements only; an authorized offline signer must review/sign the artifact.
func MeasureD09XattrEnumerationCalibration(source *os.File, accessProfile, probeCorpusDigest string) (D09XattrEnumerationCalibration, error) {
	if accessProfile != D09XattrManagedRWSourceProfile && accessProfile != D09XattrReadonlySourceProfile ||
		!validDigest(probeCorpusDigest) {
		return D09XattrEnumerationCalibration{}, ErrD09TemplateInvalid
	}
	measurements, err := MeasureD09XattrScannerIdentity(source)
	if err != nil {
		return D09XattrEnumerationCalibration{}, err
	}
	if accessProfile == D09XattrManagedRWSourceProfile && measurements.ReadOnly ||
		accessProfile == D09XattrReadonlySourceProfile && (!measurements.ReadOnly || !measurements.RootOwned) {
		return D09XattrEnumerationCalibration{}, errors.New("calibration source access mode differs from requested profile")
	}
	observation, err := MeasureD09XattrEnumeration(source)
	if err != nil || observation.EntryCount == 0 || observation.XattrNameCount == 0 {
		return D09XattrEnumerationCalibration{}, errors.Join(
			errors.New("calibration corpus did not expose the required xattr enumeration probe"), err)
	}
	return D09XattrEnumerationCalibration{
		SchemaVersion: D09XattrEnumerationCalibrationSchema, Authority: D09XattrVisibilityAuthority,
		FilesystemMagic: measurements.FilesystemMagic, MountOptionsDigest: measurements.MountOptionsDigest,
		ScanUID: measurements.ScanUID, ScanGID: measurements.ScanGID,
		ScanPrivilegeProfile: measurements.ScanPrivilegeProfile, SourceAccessProfile: accessProfile,
		EnumerationProfile: D09XattrEnumerationProfile,
		VisibilityProfileDigest: DigestD09XattrVisibilityProfile(measurements.FilesystemMagic,
			measurements.MountOptionsDigest, measurements.ScanUID, measurements.ScanGID,
			measurements.ScanPrivilegeProfile),
		ProbeCorpusDigest: probeCorpusDigest, ProbeObservationDigest: observation.ObservationDigest,
		ProbeEntryCount: observation.EntryCount, ProbeXattrNameCount: observation.XattrNameCount,
	}, nil
}

func measureXattrScannerPrivilege() (profile, capEff string, noNewPrivs, initialUserNS bool, err error) {
	if os.Geteuid() <= 0 || os.Getegid() <= 0 {
		return "", "", false, false, errors.New("xattr scanner must run as the dedicated non-root service UID/GID")
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return "", "", false, false, err
	}
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch fields[0] {
		case "CapEff:":
			capEff = strings.ToLower(fields[1])
		case "NoNewPrivs:":
			noNewPrivs = fields[1] == "1"
		}
	}
	if capEff != d09EmptyCapabilitySet || !noNewPrivs {
		return "", capEff, noNewPrivs, false, errors.New("xattr scanner requires zero effective capabilities and NoNewPrivs=1")
	}
	groups, err := os.Getgroups()
	if err != nil || len(groups) > 1 || (len(groups) == 1 && groups[0] != os.Getegid()) {
		return "", capEff, noNewPrivs, false, errors.Join(errors.New("xattr scanner has unregistered supplementary groups"), err)
	}
	uidMap, err := os.ReadFile("/proc/self/uid_map")
	if err != nil {
		return "", capEff, noNewPrivs, false, err
	}
	gidMap, err := os.ReadFile("/proc/self/gid_map")
	if err != nil {
		return "", capEff, noNewPrivs, false, err
	}
	initialUserNS = exactInitialIDMap(uidMap) && exactInitialIDMap(gidMap)
	if !initialUserNS {
		return "", capEff, noNewPrivs, false, errors.New("xattr scanner is not in the initial host user namespace")
	}
	return D09XattrScanPrivilegeProfile, capEff, noNewPrivs, true, nil
}

func exactInitialIDMap(data []byte) bool {
	fields := strings.Fields(string(data))
	if len(fields) != 3 {
		return false
	}
	return fields[0] == "0" && fields[1] == "0" && fields[2] == "4294967295"
}

// DigestD09XattrVisibilityProfile deterministically binds the registered
// filesystem type, mount options and scanner identity into the digest stored in
// D09CellTemplate.XattrVisibilityProfileDigest.
func DigestD09XattrVisibilityProfile(filesystemMagic uint64, mountOptionsDigest string, scanUID, scanGID int, privilegeProfile string) string {
	identity := fmt.Sprintf("%s%d\x00%s\x00%d\x00%d\x00%s", d09XattrVisibilityDomain, filesystemMagic, mountOptionsDigest, scanUID, scanGID, privilegeProfile)
	return DigestBytes([]byte(identity))
}

// VerifyD09XattrVisibility checks a current source descriptor against the
// measured, registered, signed-template filesystem/privilege profile. It
// returns the workspace attestation only when the actual descriptor, mount
// options, read-only state, and scanning identity all match. The template's
// completeness claim remains an operator-measured profile fact; this runtime
// check prevents silently reusing it on a different filesystem/UID/mount.
func VerifyD09XattrVisibility(source *os.File, profile HostProfile, template D09CellTemplate) (workspace.XattrVisibilityAttestation, error) {
	if !template.registered || profile.Mode != ModeProduction || profile.ContainmentProfile != D09ContainmentProfile {
		return workspace.XattrVisibilityAttestation{}, errors.New("D09 xattr visibility requires the root-owned registered template")
	}
	trustedProfile, profileErr := LoadLinuxSignedHostProfile("")
	if profileErr != nil || !reflect.DeepEqual(profile, trustedProfile) {
		return workspace.XattrVisibilityAttestation{}, errors.Join(errors.New("xattr evidence requires the fixed-anchor verified production profile"), profileErr)
	}
	if err := validateD09CellTemplate(template); err != nil {
		return workspace.XattrVisibilityAttestation{}, err
	}
	templateDigest, err := template.Digest()
	if err != nil || profile.ContainmentProfileDigest != templateDigest {
		return workspace.XattrVisibilityAttestation{}, errors.Join(errors.New("xattr template digest is not bound to the signed production profile"), err)
	}
	observed, err := MeasureD09XattrVisibility(source)
	if err != nil {
		return workspace.XattrVisibilityAttestation{}, errors.Join(ErrD09TemplateInvalid, err)
	}
	if !observed.ReadOnly || !observed.RootOwned || !observed.NoNewPrivs || !observed.InitialUserNamespace ||
		observed.EffectiveCapabilities != d09EmptyCapabilitySet || observed.ScanPrivilegeProfile != D09XattrScanPrivilegeProfile ||
		observed.FilesystemMagic != template.XattrFilesystemMagic ||
		observed.MountOptionsDigest != template.XattrMountOptionsDigest ||
		observed.ScanUID != template.XattrScanUID || observed.ScanGID != template.XattrScanGID ||
		observed.ScanPrivilegeProfile != template.XattrScanPrivilegeProfile {
		return workspace.XattrVisibilityAttestation{}, errors.New("source filesystem/mount/scanner identity differs from registered xattr visibility profile")
	}
	expected := DigestD09XattrVisibilityProfile(template.XattrFilesystemMagic, template.XattrMountOptionsDigest,
		template.XattrScanUID, template.XattrScanGID, template.XattrScanPrivilegeProfile)
	if expected != template.XattrVisibilityProfileDigest {
		return workspace.XattrVisibilityAttestation{}, errors.New("registered xattr visibility profile digest is inconsistent")
	}
	calibration, err := LoadVerifiedD09ReadonlyXattrEnumerationCalibration(context.Background())
	if err != nil || calibration.SourceAccessProfile != D09XattrReadonlySourceProfile ||
		calibration.Authority != template.XattrVisibilityAuthority ||
		calibration.VisibilityProfileDigest != expected || calibration.FilesystemMagic != observed.FilesystemMagic ||
		calibration.MountOptionsDigest != observed.MountOptionsDigest || calibration.ScanUID != observed.ScanUID ||
		calibration.ScanGID != observed.ScanGID || calibration.ScanPrivilegeProfile != observed.ScanPrivilegeProfile {
		return workspace.XattrVisibilityAttestation{}, errors.Join(
			errors.New("source has no separately signed matching xattr enumeration calibration"), err)
	}
	enumeration, err := MeasureD09XattrEnumeration(source)
	if err != nil || enumeration.EntryCount == 0 || enumeration.DirectoryCount == 0 ||
		!validDigest(enumeration.ObservationDigest) {
		return workspace.XattrVisibilityAttestation{}, errors.Join(
			errors.New("actual source xattr enumeration did not complete under the calibrated scanner"), err)
	}
	return WorkspaceXattrAttestation(calibration.Digest, calibration.Authority)
}

func mountOptionsForID(mountID uint64) (options string, readOnly bool, err error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", false, err
	}
	record, err := parseMountRecord(string(data), mountID)
	if err != nil {
		return "", false, err
	}
	return record.options(), record.readOnly(), nil
}
