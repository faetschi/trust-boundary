//go:build linux

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/productsetup"
	"tbound/supervisor/internal/releasetrust"
)

const registrationUsage = `Usage (offline administrator; the private key never enters the product host):
  tbound product registration keygen --out DIR
  tbound product registration init --out FILE --registration-id ID --service-identity ID
      --principal UID... --provider PROFILE... --project SPEC... --max-turns N --review-timeout SECONDS
      --turn-limit CALLS,TOKENS,SECONDS --session-limit CALLS,TOKENS,SECONDS
      --git-digest sha256:HEX --runtime-digest sha256:HEX --image-digest sha256:HEX --extension-digest sha256:HEX
  tbound product registration sign --registration FILE --key PRIVATE --out FILE
  tbound product registration verify --registration FILE --signature FILE --public-key FILE
--project SPEC is KEY=VALUE pairs joined by commas, list values joined by +:
  id=P,root=sha256:HEX,policy=sha256:HEX,modes=review+auto[,bash=PATH+PATH][,writable=A+B][,read-only=A+B][,protected=A+B]
init writes an unsigned template and refuses values the registration would refuse. It has no
placeholders: limits and artifact digests are explicit flags; the compiled host identity is filled by init. sign writes FILE and FILE.sig and refuses
to overwrite either. Changing a project, scope, policy or limit means signing a new registration.
Exit codes: 0 success, 2 refused or invalid usage, 1 other.
`

func runProductRegistration(args []string, out, stderr io.Writer) error {
	usage := func() error {
		_, _ = io.WriteString(stderr, registrationUsage)
		return adminRefusal("invalid registration command or flags")
	}
	if len(args) == 0 {
		return usage()
	}
	if args[0] == "help" || args[0] == "--help" {
		_, err := io.WriteString(out, registrationUsage)
		return err
	}
	flags := flag.NewFlagSet("product registration "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var outFile, regFile, keyFile, sigFile, pubFile string
	flags.StringVar(&outFile, "out", "", "")
	flags.StringVar(&regFile, "registration", "", "")
	flags.StringVar(&keyFile, "key", "", "")
	flags.StringVar(&sigFile, "signature", "", "")
	flags.StringVar(&pubFile, "public-key", "", "")
	var initFlags registrationInitFlags
	if args[0] == "init" {
		initFlags.register(flags)
	}
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return usage()
	}
	switch args[0] {
	case "keygen":
		if outFile == "" {
			return usage()
		}
		pub, err := releasetrust.WriteKeyPair(outFile, "registration")
		if err != nil {
			return adminRefusal("%v", err)
		}
		_, err = fmt.Fprintf(out, "wrote %s/registration.key (0600, keep it offline) and %s/registration.pub (install it on the product host)\npublic key fingerprint %s\n",
			outFile, outFile, releasetrust.Fingerprint(pub))
		return err
	case "init":
		if outFile == "" {
			return usage()
		}
		return initFlags.write(out, outFile)
	case "sign":
		if regFile == "" || keyFile == "" || outFile == "" {
			return usage()
		}
		return registrationSign(out, regFile, keyFile, outFile)
	case "verify":
		if regFile == "" || sigFile == "" || pubFile == "" {
			return usage()
		}
		reg, err := productsession.LoadRegistration(regFile, sigFile, pubFile)
		if err != nil {
			return adminRefusal("%v", err)
		}
		_, err = io.WriteString(out, registrationSummary(reg).Text)
		return err
	}
	return usage()
}

type registrationInitFlags struct {
	id, service, git, runtime, image, extension, turn, session string
	principals, providers, projects                            repeatedFlag
	maxTurns, reviewTimeout                                    int
}

func (f *registrationInitFlags) register(flags *flag.FlagSet) {
	flags.StringVar(&f.id, "registration-id", "", "")
	flags.StringVar(&f.service, "service-identity", "", "")
	flags.StringVar(&f.git, "git-digest", "", "")
	flags.StringVar(&f.runtime, "runtime-digest", "", "")
	flags.StringVar(&f.image, "image-digest", "", "")
	flags.StringVar(&f.extension, "extension-digest", "", "")
	flags.StringVar(&f.turn, "turn-limit", "", "")
	flags.StringVar(&f.session, "session-limit", "", "")
	flags.Var(&f.principals, "principal", "")
	flags.Var(&f.providers, "provider", "")
	flags.Var(&f.projects, "project", "")
	flags.IntVar(&f.maxTurns, "max-turns", 0, "")
	flags.IntVar(&f.reviewTimeout, "review-timeout", 0, "")
}

func parseBudget(s string) (productsession.Budget, error) {
	parts := strings.Split(s, ",")
	var n [3]int64
	if len(parts) != 3 {
		return productsession.Budget{}, errors.New("a limit is CALLS,TOKENS,SECONDS")
	}
	for i, p := range parts {
		v, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return productsession.Budget{}, fmt.Errorf("limit %q is not a number", p)
		}
		n[i] = v
	}
	return productsession.Budget{HostCalls: n[0], Tokens: n[1], DurationSeconds: n[2]}, nil
}

func parseProject(spec string) (productsession.Project, error) {
	var p productsession.Project
	var scope productsession.ProductScope
	list := func(v string) []string { return strings.Split(v, "+") }
	for _, pair := range strings.Split(spec, ",") {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || value == "" {
			return p, fmt.Errorf("project spec element %q is not KEY=VALUE", pair)
		}
		switch key {
		case "id":
			p.ProjectID = value
		case "root":
			p.RootIdentityDigest = value
		case "policy":
			p.PolicyDigest = value
		case "modes":
			p.PublicationModes = list(value)
		case "bash":
			p.BashOutputPaths = list(value)
		case "writable":
			scope.Writable = list(value)
		case "read-only":
			scope.ReadOnly = list(value)
		case "protected":
			scope.Protected = list(value)
		default:
			return p, fmt.Errorf("unknown project spec key %q", key)
		}
	}
	if scope.Writable != nil || scope.ReadOnly != nil || scope.Protected != nil {
		p.Scope = &scope
	}
	if p.BashOutputPaths == nil {
		p.BashOutputPaths = []string{}
	}
	return p, nil
}

func (f registrationInitFlags) registration() (productsession.Registration, error) {
	r := productsession.Registration{
		SchemaVersion: productsession.RegistrationSchema, RegistrationID: f.id, ServiceIdentity: f.service,
		GitDigest: f.git, ProviderProfiles: f.providers, RuntimeDigest: f.runtime, ImageDigest: f.image, ExtensionDigest: f.extension,
		Limits: productsession.Limits{MaxTurnsPerSession: f.maxTurns, ReviewTimeoutSeconds: f.reviewTimeout},
	}
	for _, p := range f.principals {
		uid, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return r, fmt.Errorf("principal %q is not a UID", p)
		}
		r.AllowedPrincipals = append(r.AllowedPrincipals, productsession.Principal(uid))
	}
	var err error
	if r.HostIdentityDigest, err = productHostIdentity(r.ProviderProfiles); err != nil {
		return r, err
	}
	if r.Limits.Turn, err = parseBudget(f.turn); err != nil {
		return r, fmt.Errorf("--turn-limit: %w", err)
	}
	if r.Limits.Session, err = parseBudget(f.session); err != nil {
		return r, fmt.Errorf("--session-limit: %w", err)
	}
	for _, spec := range f.projects {
		p, err := parseProject(spec)
		if err != nil {
			return r, err
		}
		r.Projects = append(r.Projects, p)
	}
	return r, nil
}

func (f registrationInitFlags) write(out io.Writer, outFile string) error {
	r, err := f.registration()
	if err != nil {
		return adminRefusal("%v", err)
	}
	// A throwaway signature runs the registration's own field validation; its result is discarded.
	_, throwaway, err := ed25519.GenerateKey(rand.Reader)
	if err == nil {
		_, _, err = productsession.SignRegistration(r, throwaway)
	}
	if err != nil {
		return adminRefusal("%v", err)
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := releasetrust.WriteNew(outFile, append(data, '\n'), 0o644, false); err != nil {
		return adminRefusal("%v", err)
	}
	_, err = fmt.Fprintf(out, "wrote the unsigned template %s; review it, then `tbound product registration sign`\n", outFile)
	return err
}

func registrationSign(out io.Writer, regFile, keyFile, outFile string) error {
	raw, err := os.ReadFile(regFile)
	if err != nil || len(raw) > productsession.MaxRegistrationBytes {
		return adminRefusal("cannot read the registration template")
	}
	var reg productsession.Registration
	if err := protocol.ValidateStrictJSON(raw); err != nil {
		return adminRefusal("the registration template is not strict JSON: %v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reg); err != nil {
		return adminRefusal("the registration template does not match its schema: %v", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return adminRefusal("the registration template contains trailing data")
	}
	key, err := releasetrust.ReadPrivateKey(keyFile)
	if err != nil {
		return adminRefusal("%v", err)
	}
	data, signature, err := productsession.SignRegistration(reg, key)
	if err != nil {
		return adminRefusal("%v", err)
	}
	pub, _ := key.Public().(ed25519.PublicKey)
	signed, err := productsession.VerifyRegistration(data, signature, pub)
	if err != nil {
		return adminRefusal("the signed registration does not verify: %v", err)
	}
	for _, f := range []string{outFile, outFile + ".sig"} {
		if _, err := os.Lstat(f); err == nil {
			return adminRefusal("refusing to overwrite %s", f)
		}
	}
	if err := releasetrust.WriteNew(outFile, data, 0o644, false); err != nil {
		return adminRefusal("%v", err)
	}
	if err := releasetrust.WriteNew(outFile+".sig", signature, 0o644, true); err != nil {
		_ = os.Remove(outFile)
		return adminRefusal("%v", err)
	}
	_, err = fmt.Fprintf(out, "wrote %s and %s.sig\ndigest %s\nsigned with the key whose fingerprint is %s\n", outFile, outFile, signed.Digest, releasetrust.Fingerprint(pub))
	return err
}

// registrationSummary describes a verified registration for people.
func registrationSummary(r productsession.Registration) productsetup.RegistrationSummary {
	s := productsetup.RegistrationSummary{ID: r.RegistrationID, Digest: r.Digest, Profiles: r.ProviderProfiles, PolicyDigests: map[string]string{}}
	var b strings.Builder
	fmt.Fprintf(&b, "registration %s (service identity %s)\ndigest %s\n", r.RegistrationID, r.ServiceIdentity, r.Digest)
	fmt.Fprintf(&b, "principals (UIDs): %v\nprovider profiles: %s\n", r.AllowedPrincipals, strings.Join(r.ProviderProfiles, ", "))
	for _, p := range r.Projects {
		s.Projects = append(s.Projects, p.ProjectID)
		s.PolicyDigests[p.ProjectID] = p.PolicyDigest
		scope := "exact-path admission"
		if p.Scope != nil {
			scope = fmt.Sprintf("scope: %d writable, %d read-only, %d protected", len(p.Scope.Writable), len(p.Scope.ReadOnly), len(p.Scope.Protected))
		}
		fmt.Fprintf(&b, "project %s: modes %s; bash outputs %d; %s\n", p.ProjectID, strings.Join(p.PublicationModes, "+"), len(p.BashOutputPaths), scope)
	}
	fmt.Fprintf(&b, "limits: turn %+v, session %+v, max turns %d, review timeout %ds\n", r.Limits.Turn, r.Limits.Session, r.Limits.MaxTurnsPerSession, r.Limits.ReviewTimeoutSeconds)
	fmt.Fprintf(&b, "git %s\nhost identity %s\n", r.GitDigest, r.HostIdentityDigest)
	fmt.Fprintf(&b, "unverified runtime %s\nunverified image %s\nunverified extension %s\n", r.RuntimeDigest, r.ImageDigest, r.ExtensionDigest)
	s.Text = b.String()
	return s
}

// verifyRegistrationBytes is setup's registration seam: file contents as installed (base64 signature
// and key), verified like the service verifies them.
func verifyRegistrationBytes(registration, signature, key []byte) (productsetup.RegistrationSummary, error) {
	decode := func(raw []byte) ([]byte, error) {
		return base64.StdEncoding.DecodeString(strings.TrimSuffix(string(raw), "\n"))
	}
	sig, err := decode(signature)
	if err != nil {
		return productsetup.RegistrationSummary{}, errors.New("the signature file is not standard base64")
	}
	pub, err := decode(key)
	if err != nil {
		return productsetup.RegistrationSummary{}, errors.New("the public key file is not standard base64")
	}
	r, err := productsession.VerifyRegistration(registration, sig, ed25519.PublicKey(pub))
	if err != nil {
		return productsetup.RegistrationSummary{}, err
	}
	return registrationSummary(r), nil
}
