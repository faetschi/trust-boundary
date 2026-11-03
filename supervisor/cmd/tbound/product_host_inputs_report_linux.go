//go:build linux

package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"syscall"

	"tbound/supervisor/internal/productsetup"
	"tbound/supervisor/internal/releasetrust"
)

// checkProductHostInputs runs the static host-input validators that product-serve runs at start, over the
// inputs setup is about to accept or doctor finds installed, and returns one check per input: the gate
// policy of each project, Git, the calibration, its signature and the registration public key. It does
// not run the live checks (the scanner identity of every scanned root), which stay with the service.
// A check names the path, owner, mode and a digest prefix, or the reason for the refusal. It never reads
// or prints a secret.
func checkProductHostInputs(in productsetup.HostInputs) []productsetup.Check {
	var checks []productsetup.Check
	add := func(name, path string, file productHostFile, digest string, err error) {
		c := productsetup.Check{Name: "input " + name, Status: productsetup.Pass}
		if err != nil {
			c.Status, c.Detail = productsetup.Fail, path+": "+err.Error()
			c.Fix = "place a matching input, then rerun (docs/product-contracts.md, Host inputs)"
		} else {
			c.Detail = fmt.Sprintf("%s uid %d mode %04o %s", path, file.UID, file.Mode, digest)
		}
		checks = append(checks, c)
	}
	for _, id := range slices.Sorted(maps.Keys(in.PolicyDigests)) {
		path, ok := in.GatePolicyPaths[id]
		var file productHostFile
		var err error
		if ok {
			// A verified bundle establishes static configuration only. Startup still requires a qualified runner.
			_, file, err = loadProductGatePolicy(id, in.PolicyDigests[id], path, in.Owners, in.CommandInputsValid)
		} else {
			err = fmt.Errorf("gate_policy_paths has no file for project %q", id)
		}
		add("policy "+id, path, file, "digest "+shortDigest(in.PolicyDigests[id]), err)
	}
	git, err := verifyProductGit(in.GitPath, in.GitDigest, in.Owners)
	if err == nil && in.GitDigest != in.Registration.GitDigest {
		err = fmt.Errorf("git_digest %s differs from the registered git_digest %s", shortDigest(in.GitDigest), shortDigest(in.Registration.GitDigest))
	}
	add("git", in.GitPath, git, "digest "+shortDigest(in.GitDigest), err)

	// The signed registration binds the compiled host identity, which a binary update can change.
	identity, identityErr := productHostIdentity(in.Registration.Profiles)
	if identityErr == nil && identity != in.Registration.HostIdentityDigest {
		identityErr = fmt.Errorf("the compiled identity %s differs from the registered host_identity_digest %s", shortDigest(identity), shortDigest(in.Registration.HostIdentityDigest))
	}
	identityCheck := productsetup.Check{Name: "input host identity", Status: productsetup.Pass, Detail: "compiled identity matches the registration, digest " + shortDigest(identity)}
	if identityErr != nil {
		identityCheck.Status, identityCheck.Detail = productsetup.Fail, identityErr.Error()
		identityCheck.Fix = "sign a registration made by this build's `tbound product registration init`"
	}
	checks = append(checks, identityCheck)

	key, keyErr := decodeProductPublicKey(in.PublicKey)
	var calibration productCalibrationCheck
	if keyErr == nil {
		calibration = checkProductCalibration(in.XattrCalibrationPath, key, in.Owners)
	} else {
		unverified := errors.New("not verified: the registration public key is unusable")
		calibration.FileErr, calibration.SignatureErr = unverified, unverified
	}
	add("calibration", in.XattrCalibrationPath, calibration.File, "digest "+shortDigest(digestBytes(calibration.File.Raw)), calibration.FileErr)
	add("calibration signature", in.XattrCalibrationPath+".sig", calibration.SignatureFile,
		"digest "+shortDigest(digestBytes(calibration.SignatureFile.Raw)), calibration.SignatureErr)

	// The key file's own owner and mode are displayed, not gated: setup reads the administrator's copy, and
	// doctor's layout check already holds the installed copy to root:root 0644.
	var keyFile productHostFile
	if info, statErr := os.Lstat(in.PublicKeyPath); statErr == nil {
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			keyFile = productHostFile{UID: st.Uid, Mode: info.Mode().Perm()}
		}
	}
	fingerprint := ""
	if keyErr == nil {
		fingerprint = "fingerprint " + releasetrust.Fingerprint(key)
	}
	add("registration public key", in.PublicKeyPath, keyFile, fingerprint, keyErr)
	return checks
}

func decodeProductPublicKey(raw []byte) (ed25519.PublicKey, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, errors.New("the registration public key is not a base64 Ed25519 public key")
	}
	return decoded, nil
}

// shortDigest is the first 12 hex characters of a digest, enough to compare by eye.
func shortDigest(digest string) string {
	digest = strings.TrimPrefix(digest, "sha256:")
	return digest[:min(len(digest), 12)]
}
