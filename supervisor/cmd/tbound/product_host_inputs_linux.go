//go:build linux

package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"syscall"

	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
)

// The static host-input validators below hold no live state. product-serve runs them at start with the
// service's own owner set, and `product setup` and `product doctor` run them with the owner set of the
// installation (root and the service account), so all three agree. The live checks stay with the service:
// the scanner identity of every scanned root (productHostXattr) and the publication-time revalidation.

// productHostFile is an installed host input that passed the protected-file rules.
type productHostFile struct {
	Raw  []byte
	UID  uint32
	Mode os.FileMode
}

// productServiceOwners are the UIDs that may own an installed host input from the service's view: root
// and the account the service runs as.
func productServiceOwners() []uint32 { return []uint32{0, uint32(os.Geteuid())} }

// readProtectedHostFile reads an installed host input: a regular file opened without following a symlink,
// owned by one of owners, writable by neither group nor others, and within its size bound.
func readProtectedHostFile(path string, limit int64, owners []uint32) (productHostFile, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return productHostFile{}, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return productHostFile{}, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || !slices.Contains(owners, st.Uid) || info.Size() > limit {
		return productHostFile{}, errors.New("host input must be a protected bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return productHostFile{}, errors.Join(errors.New("host input exceeds its bound"), err)
	}
	return productHostFile{Raw: raw, UID: st.Uid, Mode: info.Mode().Perm()}, nil
}

// readProductHostFile is readProtectedHostFile as the service reads its inputs.
func readProductHostFile(path string, limit int64) ([]byte, error) {
	file, err := readProtectedHostFile(path, limit, productServiceOwners())
	return file.Raw, err
}

func readProductHostBase64(path string, owners []uint32) ([]byte, error) {
	file, err := readProtectedHostFile(path, 1024, owners)
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(strings.TrimSpace(string(file.Raw)))
}

// productRegisteredPolicyDigest is a gate policy's digest in the form the signed registration commits to.
func productRegisteredPolicyDigest(policy gate.Policy) string {
	return sha256DigestFromBarePin(barePolicyDigest(policy.Digest()))
}

// productBashPolicyMode says what a policy read decides about a policy that allows bash.
type productBashPolicyMode int

const (
	// productBashRefused rejects such a policy: the caller has no command host to admit it.
	productBashRefused productBashPolicyMode = iota
	// productBashStructural reads such a policy without admitting it. The reader reports only that the policy is
	// configured. Admission belongs to the qualification a service start runs, and no static reader may imply it.
	productBashStructural
)

// loadProductGatePolicy checks one project's installed gate policy: a protected file that parses, whose
// digest equals the one the signed registration commits to. mode decides what happens to a policy that allows bash.
func loadProductGatePolicy(projectID, registeredDigest, path string, owners []uint32, mode productBashPolicyMode) (gate.Policy, productHostFile, error) {
	file, err := readProtectedHostFile(path, gate.MaxPolicyBytes, owners)
	if err != nil {
		return gate.Policy{}, file, fmt.Errorf("read the gate policy of project %q: %w", projectID, err)
	}
	policy, err := gate.NewPolicyFromJSON(file.Raw)
	if err != nil {
		return gate.Policy{}, file, fmt.Errorf("parse the gate policy of project %q: %w", projectID, err)
	}
	if productRegisteredPolicyDigest(policy) != registeredDigest {
		return gate.Policy{}, file, fmt.Errorf("installed gate policy of project %q differs from its registration", projectID)
	}
	if productPolicyAllowsBash(policy) && mode == productBashRefused {
		return gate.Policy{}, file, fmt.Errorf("the gate policy of project %q allows bash, but the product host has no contained command runner: "+
			"install a policy that denies bash until D09 command cells are available", projectID)
	}
	return policy, file, nil
}

// verifyProductGit applies the production Git pin (digest, mode, trusted ancestors). The pin holds the
// executable and every directory above it to owners, and the owner and mode reported are the pinned
// descriptor's.
func verifyProductGit(path, digest string, owners []uint32) (productHostFile, error) {
	git, _, err := pinProductionGit(productionOperatorArtifacts{GitPath: path, GitDigest: digest, GitBoundary: productGitBoundary}, owners)
	if err != nil {
		return productHostFile{}, err
	}
	id := git.Identity()
	if err := git.Close(); err != nil {
		return productHostFile{}, err
	}
	return productHostFile{UID: id.UID, Mode: os.FileMode(id.Mode).Perm()}, nil
}

// productCalibrationCheck is the outcome of checking the signed xattr calibration. FileErr and
// SignatureErr attribute a refusal to the calibration file or to its signature, so a report can name the
// input that is wrong; both files are read even when one fails.
type productCalibrationCheck struct {
	Calibration           piruntime.D09XattrEnumerationCalibration
	File, SignatureFile   productHostFile
	FileErr, SignatureErr error
}

// Err is the first refusal, calibration file first.
func (c productCalibrationCheck) Err() error {
	if c.FileErr != nil {
		return c.FileErr
	}
	return c.SignatureErr
}

// checkProductCalibration checks the signed xattr calibration: both files protected, the signature
// verifying under the registration public key over the canonical bytes, and the managed-rw source profile.
// The signature sits at the calibration path plus ".sig".
func checkProductCalibration(path string, publicKey ed25519.PublicKey, owners []uint32) productCalibrationCheck {
	var c productCalibrationCheck
	var err error
	if c.File, err = readProtectedHostFile(path, 64<<10, owners); err != nil {
		c.FileErr = fmt.Errorf("read the xattr calibration: %w", err)
	}
	var signature []byte
	if c.SignatureFile, err = readProtectedHostFile(path+".sig", 1024, owners); err == nil {
		signature, err = base64.StdEncoding.DecodeString(strings.TrimSpace(string(c.SignatureFile.Raw)))
	}
	if err != nil {
		c.SignatureErr = fmt.Errorf("read the xattr calibration signature: %w", err)
	}
	if c.Err() != nil {
		return c
	}
	calibration, err := piruntime.VerifyD09XattrEnumerationCalibration(c.File.Raw, signature, publicKey)
	switch {
	case errors.Is(err, piruntime.ErrD09XattrCalibrationSignature):
		c.SignatureErr = fmt.Errorf("verify the xattr calibration: %w", err)
	case err != nil:
		c.FileErr = fmt.Errorf("verify the xattr calibration: %w", err)
	case calibration.SourceAccessProfile != piruntime.D09XattrManagedRWSourceProfile:
		c.FileErr = errors.New("the xattr calibration must cover managed writable roots")
	default:
		c.Calibration = calibration
	}
	return c
}

// verifyProductCalibration is checkProductCalibration for a caller that needs one verdict.
func verifyProductCalibration(path string, publicKey ed25519.PublicKey, owners []uint32) (piruntime.D09XattrEnumerationCalibration, productHostFile, productHostFile, error) {
	c := checkProductCalibration(path, publicKey, owners)
	return c.Calibration, c.File, c.SignatureFile, c.Err()
}

// productScannerMatchesCalibration is the identity rule shared by the service's live scanner check and the
// measurement command: a writable filesystem, mount and scanner identical to the calibration's, with zero
// capabilities, NoNewPrivs and the initial user namespace.
func productScannerMatchesCalibration(m piruntime.D09XattrVisibilityMeasurements, c piruntime.D09XattrEnumerationCalibration) bool {
	return !m.ReadOnly && m.FilesystemMagic == c.FilesystemMagic && m.MountOptionsDigest == c.MountOptionsDigest &&
		m.ScanUID == c.ScanUID && m.ScanGID == c.ScanGID && m.ScanPrivilegeProfile == c.ScanPrivilegeProfile &&
		m.EffectiveCapabilities == "0000000000000000" && m.NoNewPrivs && m.InitialUserNamespace
}
