package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"tbound/supervisor/internal/releasetrust"
)

const releaseUsage = `Usage:
  tbound release verify --manifest F --signature F --public-key F [--fingerprint HEX16] [--artifact F]...
  tbound release manifest --out F ARTIFACT...
  tbound release sign --manifest F --key PRIVATE --out F
  tbound release keygen --out DIR
  tbound release fingerprint --public-key F
verify refuses a bad signature, an artifact the signed manifest does not list, and a size or SHA-256
mismatch. A checksum file (SHA256SUMS) that comes with the artifacts is never authentication: only a
signature that verifies under a publisher key obtained separately is. Compare the printed key
fingerprint with the README and the release notes, or pass it as --fingerprint.
manifest, sign and keygen are for the release maintainer. They refuse to overwrite a file.
Files: manifest = canonical JSON; signature, public key and private key = base64 text.
Exit codes: 0 success, 2 refused or invalid usage, 1 other.
`

// adminError carries the exit code of a product administration command.
type adminError struct {
	msg  string
	code int
}

func (e *adminError) Error() string { return e.msg }
func (e *adminError) ExitCode() int { return e.code }

func adminRefusal(format string, args ...any) error {
	return &adminError{msg: fmt.Sprintf(format, args...), code: 2}
}

// repeatedFlag collects a flag that may appear several times.
type repeatedFlag []string

func (r *repeatedFlag) String() string     { return strings.Join(*r, ",") }
func (r *repeatedFlag) Set(v string) error { *r = append(*r, v); return nil }

func runRelease(args []string, out, stderr io.Writer) error {
	usage := func() error {
		_, _ = io.WriteString(stderr, releaseUsage)
		return adminRefusal("invalid release command or flags")
	}
	if len(args) == 0 {
		return usage()
	}
	if args[0] == "help" || args[0] == "--help" {
		_, err := io.WriteString(out, releaseUsage)
		return err
	}
	flags := flag.NewFlagSet("release "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var manifest, signature, publicKey, key, outFile, fingerprint string
	var artifacts repeatedFlag
	flags.StringVar(&outFile, "out", "", "output file or directory")
	flags.StringVar(&manifest, "manifest", "", "manifest file")
	flags.StringVar(&signature, "signature", "", "signature file")
	flags.StringVar(&publicKey, "public-key", "", "publisher public key file")
	flags.StringVar(&key, "key", "", "private key file")
	flags.StringVar(&fingerprint, "fingerprint", "", "expected public key fingerprint")
	flags.Var(&artifacts, "artifact", "artifact file to check against the manifest")
	if err := flags.Parse(args[1:]); err != nil {
		return usage()
	}
	switch args[0] {
	case "verify":
		if manifest == "" || signature == "" || publicKey == "" || flags.NArg() != 0 {
			return usage()
		}
		return releaseVerify(out, manifest, signature, publicKey, fingerprint, artifacts)
	case "manifest":
		if outFile == "" || flags.NArg() == 0 {
			return usage()
		}
		data, err := releasetrust.BuildManifest(flags.Args())
		if err != nil {
			return adminRefusal("%v", err)
		}
		if err := releasetrust.WriteNew(outFile, append(data, '\n'), 0o644, false); err != nil {
			return adminRefusal("%v", err)
		}
		_, err = fmt.Fprintf(out, "wrote %s (%d artifact(s)); sign it with `tbound release sign`\n", outFile, flags.NArg())
		return err
	case "sign":
		if manifest == "" || key == "" || outFile == "" || flags.NArg() != 0 {
			return usage()
		}
		return releaseSign(out, manifest, key, outFile)
	case "keygen":
		if outFile == "" || flags.NArg() != 0 {
			return usage()
		}
		pub, err := releasetrust.WriteKeyPair(outFile, "release-signing")
		if err != nil {
			return adminRefusal("%v", err)
		}
		_, err = fmt.Fprintf(out, "wrote %s and %s\nkeep the private key offline. Publish the fingerprint %s in the README and the release notes, separately from the artifacts.\n",
			filepath.Join(outFile, "release-signing.key"), filepath.Join(outFile, "release-signing.pub"), releasetrust.Fingerprint(pub))
		return err
	case "fingerprint":
		if publicKey == "" || flags.NArg() != 0 {
			return usage()
		}
		pub, err := releasetrust.ReadPublicKey(publicKey)
		if err != nil {
			return adminRefusal("%v", err)
		}
		_, err = fmt.Fprintln(out, releasetrust.Fingerprint(pub))
		return err
	}
	return usage()
}

func releaseVerify(out io.Writer, manifestFile, signatureFile, publicKeyFile, fingerprint string, artifacts []string) error {
	read := func(file string) ([]byte, error) {
		info, err := os.Lstat(file)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return nil, errors.New(file + " is not a regular file within 1 MiB")
		}
		return os.ReadFile(file)
	}
	manifest, err := read(manifestFile)
	if err != nil {
		return adminRefusal("%v", err)
	}
	signature, err := releasetrust.ReadSignature(signatureFile)
	if err != nil {
		return adminRefusal("%v", err)
	}
	pub, err := releasetrust.ReadPublicKey(publicKeyFile)
	if err != nil {
		return adminRefusal("%v", err)
	}
	got := releasetrust.Fingerprint(pub)
	if fingerprint != "" && !strings.EqualFold(fingerprint, got) {
		return adminRefusal("public key fingerprint %s does not match the expected %s", got, fingerprint)
	}
	m, err := releasetrust.Verify(manifest, signature, pub)
	if err != nil {
		return adminRefusal("%v", err)
	}
	if err := releasetrust.VerifyArtifacts(m, artifacts); err != nil {
		return adminRefusal("%v", err)
	}
	fmt.Fprintf(out, "signature ok; public key fingerprint %s (compare it with the README and the release notes)\n", got)
	for _, a := range artifacts {
		fmt.Fprintf(out, "artifact ok: %s\n", filepath.Base(a))
	}
	_, err = fmt.Fprintf(out, "manifest lists %d artifact(s); %d checked\n", len(m.Artifacts), len(artifacts))
	return err
}

func releaseSign(out io.Writer, manifestFile, keyFile, outFile string) error {
	manifest, err := os.ReadFile(manifestFile)
	if err != nil {
		return adminRefusal("cannot read the manifest: %v", err)
	}
	key, err := releasetrust.ReadPrivateKey(keyFile)
	if err != nil {
		return adminRefusal("%v", err)
	}
	signature, err := releasetrust.Sign(manifest, key)
	if err != nil {
		return adminRefusal("%v", err)
	}
	if err := releasetrust.WriteNew(outFile, signature, 0o644, true); err != nil {
		return adminRefusal("%v", err)
	}
	_, err = fmt.Fprintf(out, "wrote %s; verify it with `tbound release verify`\n", outFile)
	return err
}
