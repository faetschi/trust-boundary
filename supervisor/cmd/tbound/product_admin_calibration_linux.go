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
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/releasetrust"
)

const calibrationUsage = `Usage (the xattr calibration is a root-attested host input; see docs/product-contracts.md, "Host inputs"):
  tbound product calibration sign --key PRIVATE --public-key REGISTRATION_PUBLIC_KEY --in UNSIGNED --out SIGNED
sign runs offline with the registration key pair (base64 files, the private key mode 0600). It refuses a
private key that does not match the given public key, a calibration that is not for managed writable
roots, and an existing SIGNED or SIGNED.sig. It writes SIGNED and SIGNED.sig (signature last) through
synced temporary files and never prints key material.
Exit codes: 0 success, 2 refused or invalid usage, 1 other.
`

const maxProductCalibrationBytes = 64 << 10

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
	var keyFile, publicKeyFile, inFile, outFile string
	flags.StringVar(&outFile, "out", "", "")
	switch args[0] {
	case "sign":
		flags.StringVar(&keyFile, "key", "", "")
		flags.StringVar(&publicKeyFile, "public-key", "", "")
		flags.StringVar(&inFile, "in", "", "")
	default:
		return usage()
	}
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || keyFile == "" || publicKeyFile == "" || inFile == "" || outFile == "" {
		return usage()
	}
	return calibrationSign(out, keyFile, publicKeyFile, inFile, outFile)
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
