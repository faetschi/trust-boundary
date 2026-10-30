//go:build linux

package piruntime

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"tbound/supervisor/internal/workspace"
)

func TestD09XattrCalibrationRolesAreClosedAndManagedRWBindsRegistration(t *testing.T) {
	seed := [ed25519.SeedSize]byte{21, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
		17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	publicKey := privateKey.Public().(ed25519.PublicKey)
	registration := validManagedLiveRootRegistration()
	uid, gid := registration.ServiceUID, registration.ServiceGID
	// The read-only runtime source and the managed read-write live root are
	// distinct mounts with distinct mount-options digests.
	roOptions := DigestBytes([]byte("ro,nosuid,nodev,relatime\x00ext4\x00/dev/test"))
	rwOptions := registration.RootMountOptionsDigest
	templateROVisibility := DigestD09XattrVisibilityProfile(0xef53, roOptions, uid, gid, D09XattrScanPrivilegeProfile)
	managed := D09XattrEnumerationCalibration{
		Authority:       D09XattrVisibilityAuthority,
		FilesystemMagic: 0xef53, MountOptionsDigest: rwOptions, ScanUID: uid, ScanGID: gid,
		ScanPrivilegeProfile: D09XattrScanPrivilegeProfile, SourceAccessProfile: D09XattrManagedRWSourceProfile,
		EnumerationProfile:      D09XattrEnumerationProfile,
		VisibilityProfileDigest: DigestD09XattrVisibilityProfile(0xef53, rwOptions, uid, gid, D09XattrScanPrivilegeProfile),
		ProbeCorpusDigest:       DigestBytes([]byte("managed probe corpus")),
		ProbeObservationDigest:  DigestBytes([]byte("managed observation")), ProbeEntryCount: 2, ProbeXattrNameCount: 1,
	}
	readonly := managed
	readonly.MountOptionsDigest, readonly.SourceAccessProfile = roOptions, D09XattrReadonlySourceProfile
	readonly.VisibilityProfileDigest = templateROVisibility
	readonly.ProbeEntryCount, readonly.ProbeXattrNameCount = 5, 6
	managedEncoded, managedSignature, managedDigest, err := SignD09XattrEnumerationCalibration(managed, privateKey)
	if err != nil {
		t.Fatalf("sign managed-RW calibration: %v", err)
	}
	readonlyEncoded, readonlySignature, _, err := SignD09XattrEnumerationCalibration(readonly, privateKey)
	if err != nil {
		t.Fatalf("sign read-only calibration: %v", err)
	}
	// Role closure: each signed file is accepted only in the role it was measured for.
	managedVerified, err := verifyD09XattrCalibrationForSource(managedEncoded, managedSignature, publicKey, D09XattrManagedRWSourceProfile)
	if err != nil {
		t.Fatalf("managed-RW calibration refused in its own role: %v", err)
	}
	readonlyVerified, err := verifyD09XattrCalibrationForSource(readonlyEncoded, readonlySignature, publicKey, D09XattrReadonlySourceProfile)
	if err != nil {
		t.Fatalf("read-only calibration refused in its own role: %v", err)
	}
	if _, err := verifyD09XattrCalibrationForSource(readonlyEncoded, readonlySignature, publicKey, D09XattrManagedRWSourceProfile); err == nil {
		t.Fatal("read-only calibration accepted as the managed-RW calibration")
	}
	if _, err := verifyD09XattrCalibrationForSource(managedEncoded, managedSignature, publicKey, D09XattrReadonlySourceProfile); err == nil {
		t.Fatal("managed-RW calibration accepted as the read-only calibration")
	}
	// Binding: the managed-RW calibration binds to the signed registration even
	// though the RO template digest differs from it (the old comparator would refuse).
	if templateROVisibility == managedVerified.VisibilityProfileDigest {
		t.Fatal("test fixture did not separate the RO template digest from the managed-RW digest")
	}
	registration.ManagedRootXattrCalibrationDigest = managedDigest
	if err := verifyD09ManagedRWXattrCalibrationBinding(managedVerified, registration); err != nil {
		t.Fatalf("production-equivalent managed-RW calibration refused: %v", err)
	}
	if readonlyVerified.VisibilityProfileDigest != templateROVisibility {
		t.Fatal("read-only calibration does not match the RO template visibility digest")
	}
	if err := verifyD09ManagedRWXattrCalibrationBinding(readonlyVerified, registration); err == nil {
		t.Fatal("read-only calibration bound as the managed-RW root")
	}
	swappedMount := registration
	swappedMount.RootMountOptionsDigest = roOptions
	if err := verifyD09ManagedRWXattrCalibrationBinding(managedVerified, swappedMount); err == nil {
		t.Fatal("managed-RW calibration bound to a registration with different mount options")
	}
	swappedOwner := registration
	swappedOwner.ServiceUID = uid + 1
	if err := verifyD09ManagedRWXattrCalibrationBinding(managedVerified, swappedOwner); err == nil {
		t.Fatal("managed-RW calibration bound to a different scanner UID")
	}
	swappedDigest := registration
	swappedDigest.ManagedRootXattrCalibrationDigest = readonlyVerified.Digest
	if err := verifyD09ManagedRWXattrCalibrationBinding(managedVerified, swappedDigest); err == nil {
		t.Fatal("managed-RW calibration accepted under another signed calibration digest")
	}
}

func TestD09XattrEnumerationCalibrationIsDomainSeparatedAndStrict(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	calibration := D09XattrEnumerationCalibration{
		Authority:       D09XattrVisibilityAuthority,
		FilesystemMagic: 0xef53, MountOptionsDigest: DigestBytes([]byte("rw,nodev,nosuid")),
		ScanUID: 1001, ScanGID: 1002, ScanPrivilegeProfile: D09XattrScanPrivilegeProfile,
		SourceAccessProfile: D09XattrManagedRWSourceProfile, EnumerationProfile: D09XattrEnumerationProfile,
		VisibilityProfileDigest: DigestD09XattrVisibilityProfile(0xef53, DigestBytes([]byte("rw,nodev,nosuid")),
			1001, 1002, D09XattrScanPrivilegeProfile),
		ProbeCorpusDigest:      DigestBytes([]byte("reviewed probe corpus")),
		ProbeObservationDigest: DigestBytes([]byte("observed probe")), ProbeEntryCount: 2, ProbeXattrNameCount: 1,
	}
	encoded, signature, digest, err := SignD09XattrEnumerationCalibration(calibration, privateKey)
	if err != nil {
		t.Fatalf("sign calibration: %v", err)
	}
	verified, err := VerifyD09XattrEnumerationCalibration(encoded, signature, publicKey)
	if err != nil || verified.Digest != digest {
		t.Fatalf("verify calibration digest=%q err=%v", verified.Digest, err)
	}
	encoded[len(encoded)/2] ^= 1
	if _, err := VerifyD09XattrEnumerationCalibration(encoded, signature, publicKey); err == nil {
		t.Fatal("tampered calibration JSON verified")
	}
}

func TestMeasureD09XattrEnumerationWalksDescriptorRootedTree(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(root, "nested", "file")
	if err := os.WriteFile(filePath, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Setxattr(filePath, "user.tbound-calibration", []byte("probe"), 0); err != nil {
		t.Skipf("test filesystem does not support user xattrs: %v", err)
	}
	rootFD, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer rootFD.Close()
	observed, err := MeasureD09XattrEnumeration(rootFD)
	if err != nil {
		t.Fatalf("enumerate xattrs: %v", err)
	}
	if observed.EntryCount != 3 || observed.DirectoryCount != 2 || observed.XattrNameCount != 1 ||
		!validDigest(observed.ObservationDigest) {
		t.Fatalf("unexpected enumeration observation: %+v", observed)
	}
}

func TestMeasureD09XattrEnumerationRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("target", filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	rootFD, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer rootFD.Close()
	if _, err := MeasureD09XattrEnumeration(rootFD); err == nil {
		t.Fatal("xattr enumeration accepted a symlink")
	}
}

func TestManagedLiveRootQualificationIssuesWorkspaceXattrAttestation(t *testing.T) {
	evidence := D09ManagedRootXattrEvidence{
		SchemaVersion: "tbound-d09-managed-live-xattr-evidence/v2", Authority: D09XattrVisibilityAuthority, RunID: "managed-run",
		OwnerEpoch: 1, RegistrationDigest: DigestBytes([]byte("registration")),
		CalibrationDigest:   DigestBytes([]byte("managed-rw-calibration")),
		LeaseEvidenceDigest: DigestBytes([]byte("lease")), RootDevice: 1, RootInode: 2,
		RootMountID: 3, FilesystemMagic: 0xef53,
		MountOptionsDigest: DigestBytes([]byte("rw,nodev,nosuid")), ScanUID: 1001, ScanGID: 1002,
		ScanPrivilegeProfile: D09XattrScanPrivilegeProfile, EntryCount: 1, DirectoryCount: 1,
		ObservationDigest: DigestBytes([]byte("observation")),
	}
	message, err := d09ManagedRootXattrEvidenceBytes(evidence)
	if err != nil {
		t.Fatal(err)
	}
	evidence.EvidenceDigest = DigestBytes(message)
	qualification := &D09ManagedLiveRootSnapshotQualification{
		xattr: evidence, digest: DigestBytes([]byte("qualified managed source")),
	}
	attestation, err := qualification.XattrVisibilityAttestation()
	if err != nil {
		t.Fatalf("qualified managed source did not issue xattr attestation: %v", err)
	}
	if attestation.Authority != workspace.XattrAuthorityBoundedNoHiddenTrusted || string(attestation.Authority) != D09XattrBoundedAuthority ||
		attestation.ProfileDigest != evidence.CalibrationDigest {
		t.Fatalf("workspace xattr attestation = %+v, want the calibrated BOUNDED authority", attestation)
	}

	qualification.xattr.EvidenceDigest = DigestBytes([]byte("tampered evidence"))
	if attestation, err := qualification.XattrVisibilityAttestation(); !errors.Is(err, ErrD09ManagedLiveRootRefused) || attestation.Authority != "" {
		t.Fatalf("tampered qualification issued workspace attestation %+v, err=%v", attestation, err)
	}
}

// The conversion boundary: the bounded label produced by the piruntime
// evidence arrives at workspace admission, and nothing else does.
func TestWorkspaceXattrAttestationConversionCarriesTheBoundedLabelToAdmission(t *testing.T) {
	digest := DigestBytes([]byte("calibration"))
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil { // workspace.Scan requires a private root; t.TempDir is 0755 under umask 022
		t.Fatal(err)
	}
	rootFD, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer rootFD.Close()
	admit := func(attestation workspace.XattrVisibilityAttestation) error {
		_, err := workspace.Scan(rootFD, workspace.Options{
			Generation: "g0", MetadataPolicyDigest: DigestBytes([]byte("metadata")), Limits: workspace.DefaultLimits(),
			QuiescentRoot: true, XattrVisibility: attestation,
		})
		return err
	}

	attestation, err := WorkspaceXattrAttestation(digest, D09XattrBoundedAuthority)
	if err != nil {
		t.Fatalf("bounded authority refused: %v", err)
	}
	if attestation.Authority != workspace.XattrAuthorityBoundedNoHiddenTrusted || attestation.ProfileDigest != digest {
		t.Fatalf("converted attestation = %+v", attestation)
	}
	if err := admit(attestation); err != nil {
		t.Fatalf("workspace admission refused the converted bounded attestation: %v", err)
	}
	for _, authority := range []string{"", "complete", "root-profile-measured-complete-enumeration/v1", "root-attested-bounded-no-hidden-trusted/v2"} {
		got, err := WorkspaceXattrAttestation(digest, authority)
		if err == nil || got != (workspace.XattrVisibilityAttestation{}) {
			t.Fatalf("authority %q converted to %+v, err=%v", authority, got, err)
		}
		if err := admit(workspace.XattrVisibilityAttestation{ProfileDigest: digest, Authority: workspace.XattrVisibilityAuthority(authority)}); !errors.Is(err, workspace.ErrXattrVisibilityUnproven) {
			t.Fatalf("workspace admission accepted authority %q: %v", authority, err)
		}
	}
}
