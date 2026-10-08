package install

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestVerifyHostProfileValidAndTampered(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	profile := []byte(`{"profile_id":"pi-host","host":"eval-vm"}`)
	sig := ed25519.Sign(priv, profile)

	dir := "/etc/tbound"
	files := map[string][]byte{
		filepath.Join(dir, ProfileJSONName): profile,
		filepath.Join(dir, ProfilePubName):  pub,
		filepath.Join(dir, ProfileSigName):  sig,
	}
	read := func(name string) ([]byte, error) {
		if b, ok := files[name]; ok {
			return b, nil
		}
		return nil, os.ErrNotExist
	}
	stat := func(string) (os.FileInfo, error) { return statFile{}, nil }

	opts := HostProfileOptions{Dir: dir, RequireRootOwner: true, OwnerUID: func(string) (int, bool) { return 0, true }, ReadFile: read, Stat: stat}
	res, err := VerifyHostProfile(opts)
	if err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}
	if !res.Valid || res.ProfileDigest == "" {
		t.Fatalf("expected valid profile with digest, got %+v", res)
	}

	files[filepath.Join(dir, ProfileJSONName)] = []byte(`{"profile_id":"pi-host","host":"tampered"}`)
	if _, err := VerifyHostProfile(opts); err == nil {
		t.Fatal("tampered profile must fail")
	}

	files[filepath.Join(dir, ProfileJSONName)] = profile
	badOwner := opts
	badOwner.OwnerUID = func(string) (int, bool) { return 1000, true }
	if _, err := VerifyHostProfile(badOwner); err == nil {
		t.Fatal("non-root-owned profile must fail")
	}
}

func TestVerifyHostProfileRefusesSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "real.json")
	if err := os.WriteFile(real, []byte(`{"profile_id":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ProfileJSONName)
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	opts := HostProfileOptions{
		Dir:      dir,
		ReadFile: os.ReadFile,
		Stat:     os.Lstat, // must not follow the link
	}
	if _, err := VerifyHostProfile(opts); err == nil {
		t.Fatal("symlinked host profile must be refused")
	}
}
