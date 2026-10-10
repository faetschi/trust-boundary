//go:build linux

// Command tbound-profile generates, signs, and verifies the Linux host profile
// the governed Pi launcher admits. It is Linux-only because the signed profile
// format, the signature domain, and the digest computation are Linux-specific.
//
// It never prints, logs, or copies private key material: sign reads the raw
// Ed25519 private key from the given path only, and the private key is never an
// output of any subcommand.
package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/piruntime"
)

const usageText = `tbound-profile — Linux host-profile generator, signer, and verifier

usage:
  tbound-profile init            --out <profile.json> [--force]
  tbound-profile sign            --profile <profile.json> --key <ed25519-private>
                                 --out-sig <signature.ed25519> --out-pub <public.pub>
                                 [--out-profile <signed.json>]
  tbound-profile verify          --profile <profile.json> --sig <signature.ed25519> --pub <public.pub>
  tbound-profile template-init   --out <d09-cell.json> [--force]
  tbound-profile template-digest --template <d09-cell.json>
  tbound-profile xattr-measure   --source <protected-read-only-directory>
  tbound-profile xattr-digest    --filesystem-magic <n> --mount-options-digest <sha256> --uid <n> --gid <n>
  tbound-profile landlock-digest --read-only <comma-absolute-paths> --read-write <comma-absolute-paths>
  tbound-profile managed-root-measure --root <path> --service-name <name> --service-group <name>
                                     --service-unit <systemd-unit> --mount-unit <systemd-mount-unit>
                                     --runner <protected-tbound-binary> --service-cgroup </system.slice/name.service>
  tbound-profile managed-root-sign --registration <json> --key <ed25519-private>
                                    --out-sig <signature> [--out-registration <signed.json>]
  tbound-profile managed-root-verify --registration <json> --sig <signature> --pub <public-key>
  tbound-profile xattr-measure  --source <protected-read-only-directory>
  tbound-profile xattr-digest   --filesystem-magic <n> --mount-options-digest <sha256> --uid <n> --gid <n>

The private key is read from --key only and is never printed. The signature
covers the verifier's domain-prefixed canonical profile bytes, and the written
profile Digest is exactly the digest the production verifier recomputes.

Production admission uses the fixed, root-owned admin public key at
/etc/tbound/pi-host-profile.pub; the profile path can never choose its own trust
root. The 'verify' subcommand is offline review tooling and takes an explicit
public key.

The managed-root registration is signed with the same offline-authorized key
and domain-separated from HostProfile. Runtime verification always uses the
fixed /etc/tbound trust anchor and fixed registration paths; the offline
managed-root-verify command accepts an explicit public key for review only.
Registration authenticates filesystem/service policy, not current quiescence.

The D09 cell template digest printed by template-digest must be installed into
the signed profile's ContainmentProfileDigest so no D09 identity can be switched
after the profile is verified.
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("a subcommand is required\n" + usageText)
	}
	switch args[0] {
	case "init":
		return runInit(args[1:], stdout, stderr)
	case "sign":
		return runSign(args[1:], stdout, stderr)
	case "verify":
		return runVerify(args[1:], stdout, stderr)
	case "template-init":
		return runTemplateInit(args[1:], stdout, stderr)
	case "template-digest":
		return runTemplateDigest(args[1:], stdout, stderr)
	case "xattr-measure":
		return runXattrMeasure(args[1:], stdout, stderr)
	case "xattr-digest":
		return runXattrDigest(args[1:], stdout, stderr)
	case "landlock-digest":
		return runLandlockDigest(args[1:], stdout, stderr)
	case "managed-root-measure":
		return runManagedRootMeasure(args[1:], stdout, stderr)
	case "managed-root-sign":
		return runManagedRootSign(args[1:], stdout, stderr)
	case "managed-root-verify":
		return runManagedRootVerify(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		if _, err := io.WriteString(stdout, usageText); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unknown tbound-profile subcommand %q\n%s", args[0], usageText)
	}
}

func runInit(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound-profile init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	out := flags.String("out", "", "output path for the canonical unsigned HostProfile template")
	force := flags.Bool("force", false, "overwrite an existing output file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if strings.TrimSpace(*out) == "" {
		return errors.New("--out is required")
	}
	encoded, err := piruntime.MarshalUnsignedLinuxHostProfileTemplate()
	if err != nil {
		return err
	}
	if err := writeNewFile(*out, encoded, *force); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "wrote canonical unsigned host profile template: %s\nDigest is empty until signed; replace placeholder digests with the measured experiment values, then:\n  tbound-profile sign --profile %s --key <ed25519-private> --out-sig %s.ed25519 --out-pub %s.pub\n",
		*out, *out, strings.TrimSuffix(*out, ".json"), strings.TrimSuffix(*out, ".json")); err != nil {
		return err
	}
	return nil
}

func runSign(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound-profile sign", flag.ContinueOnError)
	flags.SetOutput(stderr)
	profilePath := flags.String("profile", "", "path to the unsigned or partially signed HostProfile JSON")
	keyPath := flags.String("key", "", "path to a raw 64-byte Ed25519 private key")
	outSig := flags.String("out-sig", "", "output path for the raw 64-byte Ed25519 signature")
	outPub := flags.String("out-pub", "", "output path for the raw 32-byte Ed25519 public key")
	outProfile := flags.String("out-profile", "", "output path for the signed profile JSON (default: rewrite --profile)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if strings.TrimSpace(*profilePath) == "" || strings.TrimSpace(*keyPath) == "" ||
		strings.TrimSpace(*outSig) == "" || strings.TrimSpace(*outPub) == "" {
		return errors.New("--profile, --key, --out-sig, and --out-pub are required")
	}
	profileBytes, err := os.ReadFile(*profilePath)
	if err != nil {
		return fmt.Errorf("read host profile: %w", err)
	}
	profile, err := decodeStrictHostProfile(profileBytes)
	if err != nil {
		return err
	}
	privateKey, err := os.ReadFile(*keyPath)
	if err != nil {
		return fmt.Errorf("read Ed25519 private key: %w", err)
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("Ed25519 private key must be exactly %d bytes", ed25519.PrivateKeySize)
	}
	signedJSON, signature, publicKey, err := piruntime.SignLinuxHostProfile(profile, ed25519.PrivateKey(privateKey))
	if err != nil {
		return err
	}
	signedProfile, err := decodeStrictHostProfile(signedJSON)
	if err != nil {
		return err
	}
	target := strings.TrimSpace(*outProfile)
	if target == "" {
		target = *profilePath
	}
	if err := os.WriteFile(*outSig, signature, 0o600); err != nil {
		return fmt.Errorf("write Ed25519 signature: %w", err)
	}
	if err := os.WriteFile(*outPub, publicKey, 0o644); err != nil {
		return fmt.Errorf("write Ed25519 public key: %w", err)
	}
	if err := os.WriteFile(target, signedJSON, 0o600); err != nil {
		return fmt.Errorf("write signed host profile: %w", err)
	}
	if _, err := fmt.Fprintf(stdout, "signed host profile: %s\nDigest: %s\nPublic key (hex): %s\nSignature: %s\n",
		target, signedProfile.Digest, hex.EncodeToString(publicKey), *outSig); err != nil {
		return err
	}
	return nil
}

func runVerify(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound-profile verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	profilePath := flags.String("profile", "", "path to the signed HostProfile JSON")
	sigPath := flags.String("sig", "", "path to the raw 64-byte Ed25519 signature")
	pubPath := flags.String("pub", "", "path to the raw 32-byte Ed25519 public key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if strings.TrimSpace(*profilePath) == "" || strings.TrimSpace(*sigPath) == "" || strings.TrimSpace(*pubPath) == "" {
		return errors.New("--profile, --sig, and --pub are required")
	}
	profile, err := piruntime.VerifyLinuxSignedHostProfileFiles(*profilePath, *sigPath, *pubPath)
	if err != nil {
		return fmt.Errorf("host profile verification failed: %w", err)
	}
	if _, err := fmt.Fprintf(stdout, "verified signed host profile %s (digest %s, mode %s)\n", profile.ID, profile.Digest, profile.Mode); err != nil {
		return err
	}
	return nil
}

func runTemplateInit(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound-profile template-init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	out := flags.String("out", "", "output path for the canonical placeholder D09 cell template")
	force := flags.Bool("force", false, "overwrite an existing output file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if strings.TrimSpace(*out) == "" {
		return errors.New("--out is required")
	}
	encoded, err := piruntime.MarshalDefaultD09CellTemplate()
	if err != nil {
		return err
	}
	if err := writeNewFile(*out, encoded, *force); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "wrote unmeasured draft D09 cell template: %s\nReplace every empty digest (including managed_live_root_registration_digest), the exact Landlock ABI, and resource bounds with measured values. Sign the protected managed-root registration with managed-root-sign before binding its digest here. Then compute:\n  tbound-profile template-digest --template %s\n  (set the signed profile ContainmentProfileDigest to that digest)\n",
		*out, *out); err != nil {
		return err
	}
	return nil
}

func runTemplateDigest(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound-profile template-digest", flag.ContinueOnError)
	flags.SetOutput(stderr)
	templatePath := flags.String("template", "", "path to the D09 cell template JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if strings.TrimSpace(*templatePath) == "" {
		return errors.New("--template is required")
	}
	raw, err := os.ReadFile(*templatePath)
	if err != nil {
		return fmt.Errorf("read D09 cell template: %w", err)
	}
	template, err := piruntime.DecodeD09CellTemplate(raw)
	if err != nil {
		return err
	}
	digest, err := template.Digest()
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "D09 cell template canonical digest: %s\n", digest); err != nil {
		return err
	}
	return nil
}

func runXattrMeasure(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound-profile xattr-measure", flag.ContinueOnError)
	flags.SetOutput(stderr)
	sourcePath := flags.String("source", "", "protected source directory whose filesystem/mount/scanner identity is measured")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*sourcePath) == "" || !filepath.IsAbs(*sourcePath) {
		return errors.New("--source must be an absolute path")
	}
	file, err := os.Open(*sourcePath)
	if err != nil {
		return fmt.Errorf("open xattr profile source descriptor: %w", err)
	}
	defer file.Close()
	measurement, err := piruntime.MeasureD09XattrVisibility(file)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(measurement)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s\n", encoded)
	return err
}

func runXattrDigest(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound-profile xattr-digest", flag.ContinueOnError)
	flags.SetOutput(stderr)
	magic := flags.Uint64("filesystem-magic", 0, "measured filesystem magic (decimal)")
	mountDigest := flags.String("mount-options-digest", "", "measured mount-options sha256 digest")
	uid := flags.Int("uid", -1, "measured scanning uid")
	gid := flags.Int("gid", -1, "measured scanning gid")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *magic == 0 || !piruntime.ValidD09Digest(*mountDigest) || *uid < 0 || *gid < 0 {
		return errors.New("--filesystem-magic, --mount-options-digest, --uid, and --gid are required")
	}
	digest := piruntime.DigestD09XattrVisibilityProfile(*magic, *mountDigest, *uid, *gid, piruntime.D09XattrScanPrivilegeProfile)
	_, err := fmt.Fprintln(stdout, digest)
	return err
}

func runLandlockDigest(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound-profile landlock-digest", flag.ContinueOnError)
	flags.SetOutput(stderr)
	readOnly := flags.String("read-only", "", "comma-separated reviewed absolute read/execute paths")
	readWrite := flags.String("read-write", "", "comma-separated reviewed absolute writable paths")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	parse := func(value string) []string {
		var paths []string
		for _, part := range strings.Split(value, ",") {
			if path := strings.TrimSpace(part); path != "" {
				paths = append(paths, path)
			}
		}
		return paths
	}
	digest, err := piruntime.DigestD09LandlockRules(parse(*readOnly), parse(*readWrite))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, digest)
	return err
}

func runManagedRootMeasure(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound-profile managed-root-measure", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "", "exact dedicated writable live-root directory")
	service := flags.String("service-name", "", "dedicated local non-login service account name")
	group := flags.String("service-group", "", "dedicated local service group name")
	serviceUnit := flags.String("service-unit", "", "protected systemd .service unit path")
	mountUnit := flags.String("mount-unit", "", "protected systemd .mount unit path for --root")
	runner := flags.String("runner", "", "root-owned read-only TBound executable path used by ExecStart")
	cgroup := flags.String("service-cgroup", "", "exact unified cgroup path, e.g. /system.slice/tbound.service")
	out := flags.String("out", "", "optional path for the unsigned measured registration JSON")
	force := flags.Bool("force", false, "overwrite --out if it exists")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	measured, err := piruntime.MeasureD09ManagedLiveRootRegistration(*root, *service, *group, *serviceUnit, *mountUnit, *runner, *cgroup)
	if err != nil {
		return fmt.Errorf("measure managed live-root registration: %w", err)
	}
	encoded, err := json.MarshalIndent(measured, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if *out != "" {
		if err := writeManagedOutput(*out, encoded, *force); err != nil {
			return err
		}
	}
	_, err = stdout.Write(encoded)
	return err
}

func runManagedRootSign(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound-profile managed-root-sign", flag.ContinueOnError)
	flags.SetOutput(stderr)
	registrationPath := flags.String("registration", "", "measured managed-live-root registration JSON")
	keyPath := flags.String("key", "", "offline authorized raw 64-byte Ed25519 private key")
	outSig := flags.String("out-sig", "", "output path for the raw registration signature")
	outRegistration := flags.String("out-registration", "", "output path for the signed registration JSON (default: rewrite --registration)")
	force := flags.Bool("force", false, "overwrite signature/registration output paths")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *registrationPath == "" || *keyPath == "" || *outSig == "" {
		return errors.New("--registration, --key, and --out-sig are required")
	}
	encoded, err := os.ReadFile(*registrationPath)
	if err != nil {
		return fmt.Errorf("read managed-root registration: %w", err)
	}
	if err := protocol.ValidateStrictJSON(encoded); err != nil {
		return fmt.Errorf("registration JSON is not strict: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var registration piruntime.D09ManagedLiveRootRegistration
	if err := decoder.Decode(&registration); err != nil {
		return fmt.Errorf("decode managed-root registration: %w", err)
	}
	privateKey, err := os.ReadFile(*keyPath)
	if err != nil {
		return fmt.Errorf("read offline Ed25519 key: %w", err)
	}
	defer clear(privateKey)
	if len(privateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("Ed25519 private key must be exactly %d bytes", ed25519.PrivateKeySize)
	}
	signed, signature, digest, err := piruntime.SignD09ManagedLiveRootRegistration(registration, ed25519.PrivateKey(privateKey))
	if err != nil {
		return err
	}
	target := *outRegistration
	if target == "" {
		target = *registrationPath
	}
	if err := writeManagedOutput(*outSig, signature, *force); err != nil {
		return err
	}
	if err := writeManagedOutput(target, signed, *force); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "signed managed live-root registration: %s\nDigest (bind into D09 template): %s\nSignature: %s\n", target, digest, *outSig)
	return err
}

// writeManagedOutput is the safe writer for trust-registration JSON/signatures.
// Even --force never follows a destination symlink or truncates a non-regular
// object such as a FIFO/device.
func writeManagedOutput(path string, data []byte, force bool) error {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("managed-root output path must be a canonical absolute path")
	}
	parent, err := openAbsoluteOutputDirectory(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer parent.Close()
	name := filepath.Base(path)
	flags := syscall.O_WRONLY | syscall.O_CLOEXEC | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if force {
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.Join(errors.New("--force target must be an existing regular file, not a symlink/device"), statErr)
		}
	} else {
		flags |= syscall.O_CREAT | syscall.O_EXCL
	}
	fd, err := syscall.Openat(int(parent.Fd()), name, flags, 0o600)
	if err != nil {
		return fmt.Errorf("open managed-root output without following links: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.Join(errors.New("managed-root output is not a regular file"), err)
	}
	if force {
		before, statErr := os.Lstat(path)
		if statErr != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, info) {
			return errors.Join(errors.New("managed-root output changed before safe replacement"), statErr)
		}
		if err := file.Truncate(0); err != nil {
			return err
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return nil
}

func openAbsoluteOutputDirectory(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("output parent must be a canonical absolute directory")
	}
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), "managed-root-output-root")
	for _, component := range strings.Split(strings.TrimPrefix(path, string(os.PathSeparator)), string(os.PathSeparator)) {
		if component == "" {
			continue
		}
		nextFD, openErr := syscall.Openat(int(current.Fd()), component, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if openErr != nil {
			_ = current.Close()
			return nil, openErr
		}
		next := os.NewFile(uintptr(nextFD), component)
		_ = current.Close()
		current = next
	}
	return current, nil
}

func runManagedRootVerify(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound-profile managed-root-verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	registrationPath := flags.String("registration", "", "signed managed-live-root registration JSON")
	sigPath := flags.String("sig", "", "raw Ed25519 registration signature")
	pubPath := flags.String("pub", "", "authorized raw Ed25519 public key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *registrationPath == "" || *sigPath == "" || *pubPath == "" {
		return errors.New("--registration, --sig, and --pub are required")
	}
	encoded, err := os.ReadFile(*registrationPath)
	if err != nil {
		return err
	}
	signature, err := os.ReadFile(*sigPath)
	if err != nil {
		return err
	}
	publicKey, err := os.ReadFile(*pubPath)
	if err != nil {
		return err
	}
	registration, err := piruntime.VerifyD09ManagedLiveRootRegistration(encoded, signature, ed25519.PublicKey(publicKey))
	if err != nil {
		return fmt.Errorf("managed-root signature/policy verification failed: %w", err)
	}
	_, err = fmt.Fprintf(stdout, "verified signed managed live-root policy %s for %s (service %s, digest %s)\nThis verifies policy/signature only; it is not a live-root quiescence permit.\n",
		registration.SchemaVersion, registration.RootPath, registration.ServiceName, registration.Digest)
	return err
}

func decodeStrictHostProfile(data []byte) (piruntime.HostProfile, error) {
	var profile piruntime.HostProfile
	if err := protocol.ValidateStrictJSON(data); err != nil {
		return profile, fmt.Errorf("host profile is not strict JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&profile); err != nil {
		return profile, fmt.Errorf("decode host profile: %w", err)
	}
	return profile, nil
}

func writeNewFile(path string, data []byte, force bool) error {
	if !force {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("refusing to overwrite existing file %s (use --force)", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
