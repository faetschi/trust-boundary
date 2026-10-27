//go:build linux

package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"golang.org/x/sys/unix"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/productsetup"
	"tbound/supervisor/internal/releasetrust"
)

const calibrationUsage = `Usage (the xattr calibration is a root-attested host input; see docs/product-contracts.md, "Host inputs"):
  tbound product calibration measure --source DIR --probe-corpus-digest sha256:HEX --out FILE [--config FILE] [--force]
  tbound product calibration sign --key PRIVATE --public-key REGISTRATION_PUBLIC_KEY --in UNSIGNED --out SIGNED
measure runs as the service user (not root; NoNewPrivs, no capabilities) on the host. DIR is the held
probe corpus the operator prepared and reviewed; the access profile is fixed to managed writable roots.
It also measures the state root and every project root of the service config (default
/etc/tbound-product/service.json) and refuses when any scanner identity differs from the corpus
measurement. It writes the UNSIGNED measurement to FILE and refuses to replace it without --force.
sign runs offline with the registration key pair (base64 files, the private key mode 0600). It refuses a
private key that does not match the given public key, a calibration that is not for managed writable
roots, and an existing SIGNED or SIGNED.sig. It writes SIGNED and SIGNED.sig (signature last) through
synced temporary files and never prints key material.
Exit codes: 0 success, 2 refused or invalid usage, 1 other.
`

const maxProductCalibrationBytes = 64 << 10

// productCalibrationMeasure measures the operator's probe corpus. Only a test replaces it, to describe the
// scanner offline; no other non-test source may name it (TestProductCalibrationSeamIsNamedOnlyByItsOwnFile).
var productCalibrationMeasure = piruntime.MeasureD09XattrEnumerationCalibration

func runProductCalibration(args []string, out, stderr io.Writer) error {
	usage := func() error {
		_, _ = io.WriteString(stderr, calibrationUsage)
		return adminRefusal("invalid calibration command or flags")
	}
	if len(args) == 0 {
		return usage()
	}
	if args[0] == "help" || args[0] == "--help" {
		_, err := io.WriteString(out, calibrationUsage)
		return err
	}
	flags := flag.NewFlagSet("product calibration "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var keyFile, publicKeyFile, inFile, outFile, source, probeCorpusDigest string
	configFile := productsetup.ServiceConfigPath
	var force bool
	flags.StringVar(&outFile, "out", "", "")
	switch args[0] {
	case "sign":
		flags.StringVar(&keyFile, "key", "", "")
		flags.StringVar(&publicKeyFile, "public-key", "", "")
		flags.StringVar(&inFile, "in", "", "")
	case "measure":
		flags.StringVar(&source, "source", "", "")
		flags.StringVar(&probeCorpusDigest, "probe-corpus-digest", "", "")
		flags.StringVar(&configFile, "config", configFile, "")
		flags.BoolVar(&force, "force", false, "")
	default:
		return usage()
	}
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || outFile == "" || configFile == "" {
		return usage()
	}
	if args[0] == "measure" {
		if source == "" || probeCorpusDigest == "" {
			return usage()
		}
		return calibrationMeasure(out, source, probeCorpusDigest, configFile, outFile, force)
	}
	if keyFile == "" || publicKeyFile == "" || inFile == "" {
		return usage()
	}
	return calibrationSign(out, keyFile, publicKeyFile, inFile, outFile)
}

// calibrationMeasure measures the operator's probe corpus as the dedicated service identity and checks
// that the state root and every project root have that same scanner identity. The result is unsigned: the
// authority it can later carry is root's attestation that this filesystem hides no trusted.* attribute,
// which enumeration by an unprivileged scanner cannot prove.
func calibrationMeasure(out io.Writer, source, probeCorpusDigest, configFile, outFile string, force bool) error {
	if !filepath.IsAbs(source) || filepath.Clean(source) != source {
		return adminRefusal("--source must be a canonical absolute path")
	}
	if !piruntime.ValidD09Digest(probeCorpusDigest) {
		return adminRefusal("--probe-corpus-digest must be a canonical sha256 digest of the reviewed probe corpus")
	}
	if os.Geteuid() == 0 {
		return adminRefusal("calibration measure must run as the service user, not root: the scanner identity it records is the unprivileged one")
	}
	cfg, err := loadProductServiceConfig(configFile)
	if err != nil {
		return adminRefusal("cannot read the service config: %v", err)
	}
	if cfg.StateRoot == "" || len(cfg.Projects) == 0 {
		return adminRefusal("the service config names no state root or no project")
	}
	corpus, err := os.Open(source)
	if err != nil {
		return adminRefusal("cannot measure the probe corpus: %v", err)
	}
	calibration, err := productCalibrationMeasure(corpus, piruntime.D09XattrManagedRWSourceProfile, probeCorpusDigest)
	if err = errors.Join(err, corpus.Close()); err != nil {
		return adminRefusal("cannot measure the probe corpus: %v", err)
	}
	roots := []string{cfg.StateRoot}
	for _, id := range slices.Sorted(maps.Keys(cfg.Projects)) {
		roots = append(roots, cfg.Projects[id])
	}
	for _, root := range roots {
		dir, err := os.Open(root)
		if err != nil {
			return adminRefusal("cannot measure %s: %v", root, err)
		}
		m, measureErr := productHostMeasureXattr(dir)
		if err := errors.Join(measureErr, dir.Close()); err != nil {
			return adminRefusal("cannot measure %s: %v", root, err)
		}
		if !productScannerMatchesCalibration(m, calibration) {
			return adminRefusal("the filesystem, mount or scanner of %s differs from the probe corpus measurement", root)
		}
	}
	encoded, err := json.MarshalIndent(calibration, "", "  ")
	if err != nil {
		return err
	}
	if err := publishProductFile(outFile, append(encoded, '\n'), force); err != nil {
		return adminRefusal("%v", err)
	}
	_, err = fmt.Fprintf(out, "wrote the UNSIGNED measurement %s (%d probe entries, %d attribute names, scanner uid %d)\n"+
		"review it, then sign it offline with `tbound product calibration sign`; the signature is root's attestation, not a proof of attribute visibility\n",
		outFile, calibration.ProbeEntryCount, calibration.ProbeXattrNameCount, calibration.ScanUID)
	return err
}

// decodeProductCalibration strictly decodes a calibration measurement or signed calibration.
func decodeProductCalibration(raw []byte) (piruntime.D09XattrEnumerationCalibration, error) {
	var calibration piruntime.D09XattrEnumerationCalibration
	if err := protocol.ValidateStrictJSON(raw); err != nil {
		return calibration, fmt.Errorf("the calibration is not strict JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&calibration); err != nil {
		return calibration, fmt.Errorf("the calibration does not match its schema: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return calibration, errors.New("the calibration has trailing data")
	}
	return calibration, nil
}

func calibrationSign(out io.Writer, keyFile, publicKeyFile, inFile, outFile string) error {
	info, err := os.Lstat(inFile)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxProductCalibrationBytes {
		return adminRefusal("cannot read the unsigned calibration %s: it must be a regular file of at most %d bytes", inFile, maxProductCalibrationBytes)
	}
	raw, err := os.ReadFile(inFile)
	if err != nil {
		return adminRefusal("cannot read the unsigned calibration %s", inFile)
	}
	calibration, err := decodeProductCalibration(raw)
	if err != nil {
		return adminRefusal("%v", err)
	}
	key, err := releasetrust.ReadPrivateKey(keyFile)
	if err != nil {
		return adminRefusal("%v", err)
	}
	pub, err := releasetrust.ReadPublicKey(publicKeyFile)
	if err != nil {
		return adminRefusal("%v", err)
	}
	if derived, _ := key.Public().(ed25519.PublicKey); !derived.Equal(pub) {
		return adminRefusal("the private key does not match the registration public key")
	}
	if calibration.SourceAccessProfile != piruntime.D09XattrManagedRWSourceProfile {
		return adminRefusal("the calibration must cover managed writable roots (source profile %s)", piruntime.D09XattrManagedRWSourceProfile)
	}
	encoded, signature, digest, err := piruntime.SignD09XattrEnumerationCalibration(calibration, key)
	if err != nil {
		return adminRefusal("cannot sign the calibration: %v", err)
	}
	if _, err := piruntime.VerifyD09XattrEnumerationCalibration(encoded, signature, pub); err != nil {
		return adminRefusal("the signed calibration does not verify under the registration public key: %v", err)
	}
	for _, f := range []string{outFile, outFile + ".sig"} {
		if _, err := os.Lstat(f); err == nil {
			return adminRefusal("refusing to overwrite %s", f)
		}
	}
	if err := publishProductFile(outFile, encoded, false); err != nil {
		return adminRefusal("%v", err)
	}
	// The signature goes last: a crash between the two renames leaves a calibration without its signature,
	// which the verifier refuses.
	if err := publishProductFile(outFile+".sig", []byte(base64.StdEncoding.EncodeToString(signature)+"\n"), false); err != nil {
		_ = os.Remove(outFile)
		return adminRefusal("%v", err)
	}
	if err := syncProductDirectory(filepath.Dir(outFile)); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "wrote %s and %s.sig\ncalibration digest %s\nsigned with the key whose fingerprint is %s\n", outFile, outFile, digest, releasetrust.Fingerprint(pub))
	return err
}

// publishProductFile writes data to path through a synced temporary file in the same directory and
// renames it into place. Without replace an existing path is refused atomically; with it only a regular
// non-symlink file is replaced. The file is mode 0644: a calibration holds no secret, and the service
// accepts it only when neither group nor others can write it.
func publishProductFile(path string, data []byte, replace bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("output path %s must be a canonical absolute path", path)
	}
	if info, err := os.Lstat(path); err == nil && (!replace || !info.Mode().IsRegular()) {
		return fmt.Errorf("refusing to overwrite %s", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tbound-calibration-*")
	if err != nil {
		return fmt.Errorf("refusing to write %s: %w", path, err)
	}
	defer os.Remove(tmp.Name()) // a no-op once the rename succeeded
	if err = tmp.Chmod(0o644); err == nil {
		if _, err = tmp.Write(data); err == nil {
			err = tmp.Sync()
		}
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if replace {
		err = os.Rename(tmp.Name(), path)
	} else {
		err = unix.Renameat2(unix.AT_FDCWD, tmp.Name(), unix.AT_FDCWD, path, unix.RENAME_NOREPLACE)
	}
	if err != nil {
		return fmt.Errorf("refusing to write %s: %w", path, err)
	}
	return nil
}

func syncProductDirectory(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
