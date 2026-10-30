//go:build linux

package piruntime

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const (
	testBoundedXattrAuthority  = "root-attested-bounded-no-hidden-trusted/v1"
	testRetiredXattrAuthority  = "root-profile-measured-complete-enumeration/v1"
	testCalibrationSchemaLabel = "tbound-d09-xattr-enumeration-calibration/v2"
)

func testBoundedCalibration() D09XattrEnumerationCalibration {
	options := DigestBytes([]byte("rw,nodev,nosuid"))
	return D09XattrEnumerationCalibration{
		Authority:       D09XattrVisibilityAuthority,
		FilesystemMagic: 0xef53, MountOptionsDigest: options,
		ScanUID: 1001, ScanGID: 1002, ScanPrivilegeProfile: D09XattrScanPrivilegeProfile,
		SourceAccessProfile: D09XattrManagedRWSourceProfile, EnumerationProfile: D09XattrEnumerationProfile,
		VisibilityProfileDigest: DigestD09XattrVisibilityProfile(0xef53, options, 1001, 1002, D09XattrScanPrivilegeProfile),
		ProbeCorpusDigest:       DigestBytes([]byte("reviewed probe corpus")),
		ProbeObservationDigest:  DigestBytes([]byte("observed probe")), ProbeEntryCount: 2, ProbeXattrNameCount: 1,
	}
}

func TestD09XattrAuthorityIsExactlyTheBoundedRootAttestedLabel(t *testing.T) {
	if D09XattrBoundedAuthority != testBoundedXattrAuthority || D09XattrVisibilityAuthority != D09XattrBoundedAuthority {
		t.Fatalf("authority constants drifted: %q / %q", D09XattrBoundedAuthority, D09XattrVisibilityAuthority)
	}
	if !ValidD09XattrVisibilityAuthority(testBoundedXattrAuthority) {
		t.Fatal("the bounded authority was refused")
	}
	for _, unknown := range []string{"", testRetiredXattrAuthority, testBoundedXattrAuthority + " ",
		"root-attested-bounded-no-hidden-trusted/v2", "root-profile-measured-complete-enumeration/v2", "complete", "caller-claimed-complete/v1"} {
		if ValidD09XattrVisibilityAuthority(unknown) {
			t.Fatalf("unknown xattr authority %q was admitted", unknown)
		}
	}
}

func TestD09XattrCalibrationCarriesAndSignsTheBoundedAuthority(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if D09XattrEnumerationCalibrationSchema != testCalibrationSchemaLabel {
		t.Fatalf("calibration schema %q must be bumped because its encoded shape gained the authority", D09XattrEnumerationCalibrationSchema)
	}
	encoded, signature, digest, err := SignD09XattrEnumerationCalibration(testBoundedCalibration(), privateKey)
	if err != nil {
		t.Fatalf("bounded calibration refused: %v", err)
	}
	if !strings.Contains(string(encoded), `"authority":"`+testBoundedXattrAuthority+`"`) {
		t.Fatalf("signed calibration does not show the bounded label: %s", encoded)
	}
	verified, err := VerifyD09XattrEnumerationCalibration(encoded, signature, publicKey)
	if err != nil || verified.Authority != testBoundedXattrAuthority || verified.Digest != digest {
		t.Fatalf("verified calibration = %+v, %v", verified, err)
	}
	// The label is under the signature: rewriting it to another value fails.
	var swapped D09XattrEnumerationCalibration
	if err := json.Unmarshal(encoded, &swapped); err != nil {
		t.Fatal(err)
	}
	swapped.Authority = testRetiredXattrAuthority
	rewritten, _ := json.Marshal(swapped)
	if _, err := VerifyD09XattrEnumerationCalibration(rewritten, signature, publicKey); err == nil {
		t.Fatal("calibration with a rewritten authority verified")
	}
	for _, unknown := range []string{"", testRetiredXattrAuthority, "made-up/v1"} {
		bad := testBoundedCalibration()
		bad.Authority = unknown
		if _, _, _, err := SignD09XattrEnumerationCalibration(bad, privateKey); err == nil {
			t.Fatalf("calibration with unknown authority %q was signed", unknown)
		}
	}
}

func TestD09ManagedRootXattrEvidencePropagatesTheBoundedAuthority(t *testing.T) {
	build := func(authority string) D09ManagedRootXattrEvidence {
		evidence := D09ManagedRootXattrEvidence{
			SchemaVersion: "tbound-d09-managed-live-xattr-evidence/v2", Authority: authority, RunID: "managed-run",
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
		return evidence
	}
	good := build(testBoundedXattrAuthority)
	if err := good.Validate(); err != nil {
		t.Fatalf("bounded evidence refused: %v", err)
	}
	encoded, _ := json.Marshal(good)
	if !strings.Contains(string(encoded), testBoundedXattrAuthority) {
		t.Fatalf("evidence does not show the bounded label: %s", encoded)
	}
	for _, unknown := range []string{"", testRetiredXattrAuthority} {
		if err := build(unknown).Validate(); !errors.Is(err, ErrD09ManagedLiveRootRefused) {
			t.Fatalf("evidence with authority %q validated: %v", unknown, err)
		}
	}
	// The label is under the evidence digest: swapping it afterwards is detected.
	tampered := good
	tampered.Authority = testRetiredXattrAuthority
	if err := tampered.Validate(); err == nil {
		t.Fatal("evidence with a swapped authority validated")
	}
}

func TestD09XattrCalibrationWrongKeyIsDistinctFromInvalidTemplate(t *testing.T) {
	_, signer, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded, signature, _, err := SignD09XattrEnumerationCalibration(testBoundedCalibration(), signer)
	if err != nil {
		t.Fatal(err)
	}
	_, err = VerifyD09XattrEnumerationCalibration(encoded, signature, otherPublic)
	if !errors.Is(err, ErrD09XattrCalibrationSignature) {
		t.Fatalf("wrong-key verification error = %v, want the distinct signature error", err)
	}
	if strings.Contains(err.Error(), "cell template") {
		t.Fatalf("wrong-key error still blames the cell template: %q", err)
	}
	if !errors.Is(err, ErrD09TemplateInvalid) {
		t.Fatal("signature error must keep satisfying the existing fail-closed ErrD09TemplateInvalid checks")
	}
	// A malformed artifact remains an ordinary invalid-input error.
	_, err = VerifyD09XattrEnumerationCalibration([]byte("{}"), signature, otherPublic)
	if errors.Is(err, ErrD09XattrCalibrationSignature) {
		t.Fatalf("malformed calibration was reported as a signature failure: %v", err)
	}
}
