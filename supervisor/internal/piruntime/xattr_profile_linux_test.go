//go:build linux

package piruntime

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestD09XattrEnumerationCalibrationIsDomainSeparatedAndStrict(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	calibration := D09XattrEnumerationCalibration{
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
