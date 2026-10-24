//go:build linux

package productsession

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func dg(s string) string { return sha256Digest([]byte(s)) }

func fixtureKey() (ed25519.PublicKey, ed25519.PrivateKey) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	return priv.Public().(ed25519.PublicKey), priv
}

func fixtureUnsigned() Registration {
	return Registration{
		SchemaVersion:     RegistrationSchema,
		RegistrationID:    "reg-1",
		ServiceIdentity:   "tbound-product",
		AllowedPrincipals: []Principal{1000, 1001},
		Projects: []Project{
			{ProjectID: "alpha", RootIdentityDigest: dg("root-alpha"), PolicyDigest: dg("policy-alpha"),
				BashOutputPaths: []string{"out/a.txt", "out/b.txt"}, PublicationModes: []string{"review", "auto"}},
			{ProjectID: "beta", RootIdentityDigest: dg("root-beta"), PolicyDigest: dg("policy-beta"),
				BashOutputPaths: []string{}, PublicationModes: []string{"review"}},
			{ProjectID: "gamma", RootIdentityDigest: dg("root-gamma"), PolicyDigest: dg("policy-gamma"),
				BashOutputPaths: []string{}, PublicationModes: []string{"auto"}},
		},
		ProviderProfiles: []string{"p1", "p2"},
		Limits: Limits{
			Turn: Budget{10, 1000, 60}, Session: Budget{30, 3000, 180},
			MaxTurnsPerSession: 5, ReviewTimeoutSeconds: 600,
		},
		RuntimeDigest: dg("runtime"), ImageDigest: dg("image"), ExtensionDigest: dg("extension"),
	}
}

func signedRegistration(t *testing.T, mutate func(*Registration)) Registration {
	t.Helper()
	r := fixtureUnsigned()
	if mutate != nil {
		mutate(&r)
	}
	pub, priv := fixtureKey()
	data, sig, err := SignRegistration(r, priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	reg, err := VerifyRegistration(data, sig, pub)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return reg
}

func TestRegistrationSignVerifyRoundTrip(t *testing.T) {
	pub, priv := fixtureKey()
	r := fixtureUnsigned()
	data, sig, err := SignRegistration(r, priv)
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyRegistration(data, sig, pub)
	if err != nil {
		t.Fatal(err)
	}
	signing, err := RegistrationSigningBytes(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(signing, []byte("tbound-product-registration/v1\n")) || got.Digest != sha256Digest(signing) {
		t.Fatalf("digest %q does not cover the domain-prefixed signing bytes", got.Digest)
	}
	r.Digest = got.Digest
	if got.RegistrationID != r.RegistrationID || got.Limits != r.Limits || len(got.Projects) != 3 {
		t.Fatalf("round trip changed the registration: %+v", got)
	}
}

func TestVerifyRegistrationRefusals(t *testing.T) {
	pub, priv := fixtureKey()
	otherPub, _, _ := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{9}, 64)))
	data, sig, err := SignRegistration(fixtureUnsigned(), priv)
	if err != nil {
		t.Fatal(err)
	}
	flipped := bytes.Replace(data, []byte(`"alpha"`), []byte(`"alphb"`), 1)
	badDigest := bytes.Replace(data, []byte(`"digest":"sha256:`), []byte(`"digest":"sha256:0`), 1)
	badSig := append([]byte(nil), sig...)
	badSig[0] ^= 1
	cases := map[string]struct {
		data, sig []byte
		key       ed25519.PublicKey
	}{
		"tampered byte":   {flipped, sig, pub},
		"wrong key":       {data, sig, otherPub},
		"tampered sig":    {data, badSig, pub},
		"unknown field":   {bytes.Replace(data, []byte(`"digest":`), []byte(`"extra":1,"digest":`), 1), sig, pub},
		"bad digest":      {badDigest, sig, pub},
		"duplicate key":   {bytes.Replace(data, []byte(`"registration_id":"reg-1"`), []byte(`"registration_id":"reg-1","registration_id":"reg-1"`), 1), sig, pub},
		"trailing bytes":  {append(append([]byte(nil), data...), []byte(" {}")...), sig, pub},
		"short signature": {data, sig[:10], pub},
		"empty":           {nil, sig, pub},
	}
	for name, c := range cases {
		if _, err := VerifyRegistration(c.data, c.sig, c.key); !errors.Is(err, ErrRegistration) {
			t.Errorf("%s: err=%v, want ErrRegistration", name, err)
		}
	}
}

func TestRegistrationValidationRefusals(t *testing.T) {
	_, priv := fixtureKey()
	cases := map[string]func(*Registration){
		"no principals":          func(r *Registration) { r.AllowedPrincipals = nil },
		"duplicate principal":    func(r *Registration) { r.AllowedPrincipals = []Principal{5, 5} },
		"negative principal":     func(r *Registration) { r.AllowedPrincipals = []Principal{-1} },
		"duplicate project":      func(r *Registration) { r.Projects[1].ProjectID = "alpha" },
		"no projects":            func(r *Registration) { r.Projects = nil },
		"uppercase digest":       func(r *Registration) { r.Projects[0].PolicyDigest = "sha256:" + strings.Repeat("A", 64) },
		"short digest":           func(r *Registration) { r.RuntimeDigest = "sha256:abc" },
		"missing prefix":         func(r *Registration) { r.ImageDigest = strings.Repeat("a", 64) },
		"bad extension digest":   func(r *Registration) { r.ExtensionDigest = "" },
		"bad root digest":        func(r *Registration) { r.Projects[2].RootIdentityDigest = "md5:" + strings.Repeat("a", 64) },
		"unknown mode":           func(r *Registration) { r.Projects[0].PublicationModes = []string{"publish"} },
		"no modes":               func(r *Registration) { r.Projects[0].PublicationModes = nil },
		"duplicate mode":         func(r *Registration) { r.Projects[0].PublicationModes = []string{"auto", "auto"} },
		"nil bash scope":         func(r *Registration) { r.Projects[1].BashOutputPaths = nil },
		"unsorted bash scope":    func(r *Registration) { r.Projects[0].BashOutputPaths = []string{"b", "a"} },
		"absolute bash path":     func(r *Registration) { r.Projects[0].BashOutputPaths = []string{"/etc/passwd"} },
		"zero turn limit":        func(r *Registration) { r.Limits.Turn.Tokens = 0 },
		"session below turn":     func(r *Registration) { r.Limits.Session.HostCalls = 9 },
		"zero max turns":         func(r *Registration) { r.Limits.MaxTurnsPerSession = 0 },
		"too many max turns":     func(r *Registration) { r.Limits.MaxTurnsPerSession = 10001 },
		"zero review timeout":    func(r *Registration) { r.Limits.ReviewTimeoutSeconds = 0 },
		"review timeout too big": func(r *Registration) { r.Limits.ReviewTimeoutSeconds = 86401 },
		"long identity":          func(r *Registration) { r.RegistrationID = strings.Repeat("x", 129) },
		"identity with space":    func(r *Registration) { r.ServiceIdentity = "a b" },
		"duplicate profile":      func(r *Registration) { r.ProviderProfiles = []string{"p", "p"} },
		"wrong schema":           func(r *Registration) { r.SchemaVersion = "v0" },
	}
	for name, mutate := range cases {
		r := fixtureUnsigned()
		mutate(&r)
		if _, _, err := SignRegistration(r, priv); !errors.Is(err, ErrRegistration) {
			t.Errorf("%s: err=%v, want ErrRegistration", name, err)
		}
	}
	long := fixtureUnsigned()
	long.RegistrationID = strings.Repeat("x", 128)
	if _, _, err := SignRegistration(long, priv); err != nil {
		t.Errorf("128-byte identity refused: %v", err)
	}
}

func TestLoadRegistration(t *testing.T) {
	pub, priv := fixtureKey()
	data, sig, err := SignRegistration(fixtureUnsigned(), priv)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write := func(name string, content []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, content, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	regPath := write("registration.json", data)
	sigPath := write("registration.json.sig", []byte(base64.StdEncoding.EncodeToString(sig)+"\n"))
	pubPath := write("registration.pub", []byte(base64.StdEncoding.EncodeToString(pub)))
	got, err := LoadRegistration(regPath, sigPath, pubPath)
	if err != nil || got.RegistrationID != "reg-1" {
		t.Fatalf("load: %v %+v", err, got)
	}

	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(regPath, link); err != nil {
		t.Fatal(err)
	}
	otherPub, _, _ := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{3}, 64)))
	refused := map[string][3]string{
		"symlink registration": {link, sigPath, pubPath},
		"oversize registration": {write("big.json", bytes.Repeat([]byte("x"), MaxRegistrationBytes+1)),
			sigPath, pubPath},
		"bad base64 signature": {regPath, write("bad.sig", []byte("not base64!!")), pubPath},
		"short key":            {regPath, sigPath, write("short.pub", []byte(base64.StdEncoding.EncodeToString(pub[:16])))},
		"wrong key":            {regPath, sigPath, write("other.pub", []byte(base64.StdEncoding.EncodeToString(otherPub)))},
		"missing file":         {filepath.Join(dir, "missing"), sigPath, pubPath},
		"directory":            {dir, sigPath, pubPath},
	}
	for name, p := range refused {
		if _, err := LoadRegistration(p[0], p[1], p[2]); !errors.Is(err, ErrRegistration) {
			t.Errorf("%s: err=%v, want ErrRegistration", name, err)
		}
	}
}
