//go:build linux

package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestProductAdminHelperProcess lets the install.sh tests use the test binary as `tbound`: a wrapper
// script re-executes it with TBOUND_ADMIN_HELPER set and runs the command line after "--".
func TestProductAdminHelperProcess(t *testing.T) {
	if os.Getenv("TBOUND_ADMIN_HELPER") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	if err := run(args, os.Stdout, os.Stderr); err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(adminExit(err))
	}
	os.Exit(0)
}

type releaseFixture struct {
	t     *testing.T
	dir   string
	dist  string
	keys  string
	pub   string
	name  string
	tarGz string
	// extra are additional archive entries (path to content), written by writeArchive.
	extra map[string]string
	// symlinks are archive entries that are symbolic links (path to link target).
	symlinks map[string]string
	// asRoot runs install.sh as if `id -u` printed 0, so its root-run checks apply to this unprivileged test.
	asRoot bool
	// shellSetup is shell code that runs after install.sh was sourced and before main: stubs.
	shellSetup string
	// completions leaves --no-completions off.
	completions bool
}

func releaseArtifactName() string {
	return "tbound-t1-" + runtime.GOOS + "-" + runtime.GOARCH + ".tar.gz"
}

// newReleaseFixture builds a one-artifact release signed by a fresh key.
func newReleaseFixture(t *testing.T) *releaseFixture {
	t.Helper()
	f := &releaseFixture{t: t, dir: t.TempDir()}
	f.dist, f.keys = filepath.Join(f.dir, "dist"), filepath.Join(f.dir, "keys")
	if err := os.MkdirAll(f.dist, 0o755); err != nil {
		t.Fatal(err)
	}
	f.name = releaseArtifactName()
	f.tarGz = filepath.Join(f.dist, f.name)
	f.writeArchive("genuine")
	mustAdmin(t, "release", "keygen", "--out", f.keys)
	f.pub = filepath.Join(f.keys, "release-signing.pub")
	manifest := filepath.Join(f.dist, "tbound-release-manifest.json")
	mustAdmin(t, "release", "manifest", "--out", manifest, f.tarGz)
	mustAdmin(t, "release", "sign", "--manifest", manifest, "--key", filepath.Join(f.keys, "release-signing.key"), "--out", manifest+".sig")
	return f
}

// writeArchive writes the tarball with bin/tbound, bin/tbound-doctor and bin/tbound-profile.
func (f *releaseFixture) writeArchive(marker string) {
	f.t.Helper()
	file, err := os.Create(f.tarGz)
	if err != nil {
		f.t.Fatal(err)
	}
	defer file.Close()
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"bin/tbound", "bin/tbound-doctor", "bin/tbound-profile"} {
		body := "#!/bin/sh\necho " + marker + "\n"
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			f.t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			f.t.Fatal(err)
		}
	}
	for name, body := range f.extra {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			f.t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			f.t.Fatal(err)
		}
	}
	for name, target := range f.symlinks {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o777, Linkname: target, Typeflag: tar.TypeSymlink}); err != nil {
			f.t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		f.t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		f.t.Fatal(err)
	}
}

// verifier is a wrapper script that runs this test binary as `tbound`.
func (f *releaseFixture) verifier() string {
	path := filepath.Join(f.dir, "tbound-verifier")
	script := "#!/bin/sh\nexec env TBOUND_ADMIN_HELPER=1 '" + os.Args[0] + "' -test.run='^TestProductAdminHelperProcess$' -- \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		f.t.Fatal(err)
	}
	return path
}

// install runs install.sh against the local dist and returns its output, the exit error and the prefix.
func (f *releaseFixture) install(extra ...string) (out, prefix string, err error) {
	f.t.Helper()
	return f.installCommand("", extra...)
}

// asRootShell sources install.sh ($1) and runs its main with `id -u` answering 0.
const asRootShell = `. "$1"; shift; id() { if [ "${1:-}" = -u ]; then echo 0; else command id "$@"; fi; }; eval "$TBOUND_TEST_SETUP"; main "$@"`

// installCommand is install with an optional install.sh subcommand such as "update".
func (f *releaseFixture) installCommand(subcommand string, extra ...string) (out, prefix string, err error) {
	f.t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		f.t.Skip("bash is not available")
	}
	home := filepath.Join(f.dir, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		f.t.Fatal(err)
	}
	prefix = filepath.Join(home, "prefix")
	args := []string{"../../../install/install.sh"}
	if subcommand != "" {
		args = append(args, subcommand)
	}
	args = append(args, "--version", "t1", "--prefix", prefix, "--local-dist", f.dist, "--no-path")
	if !f.completions {
		args = append(args, "--no-completions")
	}
	args = append(args, extra...)
	if f.asRoot {
		args = append([]string{"-c", asRootShell, "tbound-test"}, args...)
	}
	cmd := exec.Command(bash, args...)
	cmd.Env = []string{"HOME=" + home, "TMPDIR=" + f.dir, "PATH=/usr/bin:/bin", "XDG_CONFIG_HOME=" + filepath.Join(home, "cfg"), "TBOUND_TEST_SETUP=" + f.shellSetup}
	combined, err := cmd.CombinedOutput()
	return string(combined), prefix, err
}

func TestProductAdminReleaseVerifyRoundTripAndFingerprint(t *testing.T) {
	f := newReleaseFixture(t)
	manifest := filepath.Join(f.dist, "tbound-release-manifest.json")
	out := mustAdmin(t, "release", "verify", "--manifest", manifest, "--signature", manifest+".sig", "--public-key", f.pub, "--artifact", f.tarGz)
	fingerprint := strings.TrimSpace(mustAdmin(t, "release", "fingerprint", "--public-key", f.pub))
	if len(fingerprint) != 16 || !strings.Contains(out, "public key fingerprint "+fingerprint) || !strings.Contains(out, "artifact ok: "+f.name) {
		t.Fatalf("verify output (fingerprint %q):\n%s", fingerprint, out)
	}
	mustAdmin(t, "release", "verify", "--manifest", manifest, "--signature", manifest+".sig", "--public-key", f.pub, "--fingerprint", strings.ToUpper(fingerprint))
	if _, err := adminRun("release", "verify", "--manifest", manifest, "--signature", manifest+".sig", "--public-key", f.pub, "--fingerprint", "0000000000000000"); adminExit(err) != 2 {
		t.Fatalf("a wrong expected fingerprint was accepted: %v", err)
	}
	if _, err := adminRun("release", "keygen", "--out", f.keys); adminExit(err) != 2 {
		t.Fatalf("release keygen overwrote a key: %v", err)
	}
	if _, err := adminRun("release", "sign", "--manifest", manifest, "--key", filepath.Join(f.keys, "release-signing.key"), "--out", manifest+".sig"); adminExit(err) != 2 {
		t.Fatalf("release sign overwrote a signature: %v", err)
	}
}

func TestProductAdminReleaseRefusesTamperingEvenWithAReplacedChecksumFile(t *testing.T) {
	f := newReleaseFixture(t)
	manifest := filepath.Join(f.dist, "tbound-release-manifest.json")
	f.writeArchive("malicious")
	sums := "0000000000000000000000000000000000000000000000000000000000000000  " + f.name + "\n"
	if err := os.WriteFile(filepath.Join(f.dist, "SHA256SUMS"), []byte(sums), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := adminRun("release", "verify", "--manifest", manifest, "--signature", manifest+".sig", "--public-key", f.pub, "--artifact", f.tarGz)
	if adminExit(err) != 2 || !strings.Contains(err.Error(), "does not match the signed manifest") {
		t.Fatalf("a tampered artifact verified: %v\n%s", err, out)
	}
	other := filepath.Join(f.dir, "other-keys")
	mustAdmin(t, "release", "keygen", "--out", other)
	if _, err := adminRun("release", "verify", "--manifest", manifest, "--signature", manifest+".sig", "--public-key", filepath.Join(other, "release-signing.pub")); adminExit(err) != 2 {
		t.Fatalf("a signature verified under another publisher's key: %v", err)
	}
}

func TestProductAdminReleaseInstallScriptVerifiesTheSignatureBeforeExtracting(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("install.sh supports amd64 and arm64")
	}
	f := newReleaseFixture(t)
	fingerprint := strings.TrimSpace(mustAdmin(t, "release", "fingerprint", "--public-key", f.pub))

	out, prefix, err := f.install("--public-key", f.pub, "--verifier", f.verifier(), "--key-fingerprint", fingerprint)
	if err != nil {
		t.Fatalf("a genuine signed install failed: %v\n%s", err, out)
	}
	if _, statErr := os.Stat(filepath.Join(prefix, "bin", "tbound")); statErr != nil || !strings.Contains(out, "signature ok; public key fingerprint "+fingerprint) {
		t.Fatalf("install did not complete after verification: %v\n%s", statErr, out)
	}

	f = newReleaseFixture(t)
	f.writeArchive("malicious") // the artifact is replaced, and so is the checksum file
	sums := "0000000000000000000000000000000000000000000000000000000000000000  " + f.name + "\n"
	if err := os.WriteFile(filepath.Join(f.dist, "SHA256SUMS"), []byte(sums), 0o644); err != nil {
		t.Fatal(err)
	}
	out, prefix, err = f.install("--public-key", f.pub, "--verifier", f.verifier())
	if err == nil || !strings.Contains(out, "nothing was extracted") {
		t.Fatalf("a tampered artifact was installed: %v\n%s", err, out)
	}
	if _, statErr := os.Stat(prefix); statErr == nil {
		t.Fatal("a refused install created the prefix")
	}
	entries, _ := filepath.Glob(filepath.Join(filepath.Dir(prefix), ".tbound-stage.*"))
	if len(entries) != 0 {
		t.Fatalf("a refused install extracted into %v", entries)
	}

	f = newReleaseFixture(t)
	other := filepath.Join(f.dir, "other")
	mustAdmin(t, "release", "keygen", "--out", other)
	if out, _, err := f.install("--public-key", filepath.Join(other, "release-signing.pub"), "--verifier", f.verifier()); err == nil {
		t.Fatalf("an install under another publisher's key succeeded:\n%s", out)
	}
	if out, _, err := f.install("--public-key", f.pub, "--verifier", f.verifier(), "--key-fingerprint", "0000000000000000"); err == nil {
		t.Fatalf("an install with a wrong expected fingerprint succeeded:\n%s", out)
	}
	if out, _, err := f.install("--public-key", f.pub); err == nil || !strings.Contains(out, "no verifier") {
		t.Fatalf("an install without a verifier must refuse and say so: %v\n%s", err, out)
	}
	if err := os.Remove(filepath.Join(f.dist, "tbound-release-manifest.json.sig")); err != nil {
		t.Fatal(err)
	}
	if out, _, err := f.install("--public-key", f.pub, "--verifier", f.verifier()); err == nil || !strings.Contains(out, "missing tbound-release-manifest.json.sig") {
		t.Fatalf("a missing signature must refuse the install: %v\n%s", err, out)
	}
}

func TestProductAdminReleaseInstallScriptChecksumOnlyPathStillWorksAndSaysItIsNotAuthentication(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("install.sh supports amd64 and arm64")
	}
	f := newReleaseFixture(t)
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("sha256sum is not available")
	}
	cmd := exec.Command("sha256sum", f.name)
	cmd.Dir = f.dist
	sums, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dist, "SHA256SUMS"), sums, 0o644); err != nil {
		t.Fatal(err)
	}
	out, prefix, err := f.install()
	if err != nil || !strings.Contains(out, "not publisher authentication") {
		t.Fatalf("checksum-only install: %v\n%s", err, out)
	}
	if _, statErr := os.Stat(filepath.Join(prefix, "bin", "tbound")); statErr != nil {
		t.Fatalf("checksum-only install did not install: %v", statErr)
	}
	if help, err := exec.Command("bash", "../../../install/install.sh", "--help").CombinedOutput(); err != nil || !strings.Contains(string(help), "FIRST-INSTALL LIMIT") {
		t.Fatalf("the help text must state the first-install trust limit: %v\n%s", err, help)
	}
}

// publishSums replaces the checksum file the way a same-origin attacker who also replaced the artifact would.
func (f *releaseFixture) publishSums() {
	f.t.Helper()
	data, err := os.ReadFile(f.tarGz)
	if err != nil {
		f.t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if err := os.WriteFile(filepath.Join(f.dist, "SHA256SUMS"), []byte(hex.EncodeToString(sum[:])+"  "+f.name+"\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func installedMarker(t *testing.T, prefix string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(prefix, "bin", "tbound"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(strings.TrimPrefix(string(data), "#!/bin/sh\necho "))
}

// resign replaces the manifest and its signature with a fresh signature by the key in keyDir over the
// archive as it is now.
func (f *releaseFixture) resign(keyDir string) {
	f.t.Helper()
	manifest := filepath.Join(f.dist, "tbound-release-manifest.json")
	for _, stale := range []string{manifest, manifest + ".sig"} {
		if err := os.Remove(stale); err != nil {
			f.t.Fatal(err)
		}
	}
	mustAdmin(f.t, "release", "manifest", "--out", manifest, f.tarGz)
	mustAdmin(f.t, "release", "sign", "--manifest", manifest, "--key", filepath.Join(keyDir, "release-signing.key"), "--out", manifest+".sig")
}

func TestProductAdminReleaseInstallScriptPinsThePublisherKeyForEveryLaterInstall(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("install.sh supports amd64 and arm64")
	}
	f := newReleaseFixture(t)
	fingerprint := strings.TrimSpace(mustAdmin(t, "release", "fingerprint", "--public-key", f.pub))
	out, prefix, err := f.install("--public-key", f.pub, "--verifier", f.verifier(), "--key-fingerprint", fingerprint)
	if err != nil {
		t.Fatalf("signed first install: %v\n%s", err, out)
	}
	pinned := filepath.Join(prefix, ".tbound-publisher.pub")
	info, err := os.Stat(pinned)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("the first authenticated install must pin the publisher key as a 0644 file in the prefix: %v %v", err, info)
	}
	want, _ := os.ReadFile(f.pub)
	if got, _ := os.ReadFile(pinned); string(got) != string(want) {
		t.Fatal("the pinned key is not the key the install verified")
	}

	// The artifact and the same-origin checksum file are both replaced, and no --public-key is given.
	f.writeArchive("malicious")
	f.publishSums()
	out, _, err = f.installCommand("update", "--verifier", f.verifier())
	if err == nil || !strings.Contains(out, "pinned publisher key") || !strings.Contains(out, "nothing was extracted") {
		t.Fatalf("an update without a signature was accepted for a keyed prefix: %v\n%s", err, out)
	}

	// The attacker also re-signs a manifest with their own key: the pinned key still decides.
	attacker := filepath.Join(f.dir, "attacker")
	mustAdmin(t, "release", "keygen", "--out", attacker)
	f.resign(attacker)
	if out, _, err := f.installCommand("update", "--verifier", f.verifier()); err == nil || !strings.Contains(out, "nothing was extracted") {
		t.Fatalf("a manifest signed by another key was accepted for a keyed prefix: %v\n%s", err, out)
	}
	if out, _, err := f.installCommand("update", "--verifier", f.verifier(), "--public-key", filepath.Join(attacker, "release-signing.pub")); err == nil || !strings.Contains(out, "differs from the pinned publisher key") {
		t.Fatalf("a different --public-key replaced the pinned key: %v\n%s", err, out)
	}
	if got := installedMarker(t, prefix); got != "genuine" {
		t.Fatalf("a refused update replaced the installation: %q", got)
	}

	// The genuine signed release still updates, from the pinned key alone.
	f.writeArchive("genuine")
	f.resign(f.keys)
	if out, _, err := f.installCommand("update", "--verifier", f.verifier()); err != nil || !strings.Contains(out, "pinned publisher key") {
		t.Fatalf("a genuine signed update failed under the pinned key: %v\n%s", err, out)
	}
}

func TestProductAdminReleaseInstallScriptSaysWhenAPrefixHasNoPinnedKey(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("install.sh supports amd64 and arm64")
	}
	f := newReleaseFixture(t)
	f.publishSums()
	if out, _, err := f.install(); err != nil {
		t.Fatalf("checksum-only install: %v\n%s", err, out)
	}
	out, prefix, err := f.installCommand("update")
	if err != nil || !strings.Contains(out, "installed without a publisher key") {
		t.Fatalf("an update of an unkeyed prefix must proceed and say so: %v\n%s", err, out)
	}
	if _, statErr := os.Stat(filepath.Join(prefix, ".tbound-publisher.pub")); statErr == nil {
		t.Fatal("an unauthenticated install pinned a key")
	}
	fingerprint := strings.TrimSpace(mustAdmin(t, "release", "fingerprint", "--public-key", f.pub))
	if out, _, err := f.installCommand("update", "--public-key", f.pub, "--verifier", f.verifier(), "--key-fingerprint", fingerprint); err != nil {
		t.Fatalf("the first authenticated update of an unkeyed prefix failed: %v\n%s", err, out)
	}
	if _, statErr := os.Stat(filepath.Join(prefix, ".tbound-publisher.pub")); statErr != nil {
		t.Fatalf("the first authenticated update did not pin the key: %v", statErr)
	}
}

func TestProductAdminReleaseInstallScriptNeverTakesAPublisherPinFromTheArchive(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("install.sh supports amd64 and arm64")
	}
	f := newReleaseFixture(t)
	attacker := filepath.Join(f.dir, "attacker")
	mustAdmin(t, "release", "keygen", "--out", attacker)
	attackerKey, err := os.ReadFile(filepath.Join(attacker, "release-signing.pub"))
	if err != nil {
		t.Fatal(err)
	}
	f.extra = map[string]string{".tbound-publisher.pub": string(attackerKey)}
	f.writeArchive("malicious")
	f.publishSums()
	out, prefix, err := f.install()
	if err != nil {
		t.Fatalf("checksum-only install: %v\n%s", err, out)
	}
	if _, statErr := os.Lstat(filepath.Join(prefix, ".tbound-publisher.pub")); statErr == nil {
		t.Fatal("a checksum-only archive planted the publisher pin of the prefix")
	}
	if out, _, err := f.installCommand("update"); err != nil || !strings.Contains(out, "installed without a publisher key") {
		t.Fatalf("the prefix must stay unkeyed after a checksum-only install: %v\n%s", err, out)
	}
}

func TestProductAdminReleaseInstallScriptAsRootRefusesAPrefixBelowADirectoryAnotherUserOwns(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("install.sh supports amd64 and arm64")
	}
	if os.Geteuid() == 0 {
		t.Skip("the prefix below this test's directory would be root-owned")
	}
	f := newReleaseFixture(t)
	fingerprint := strings.TrimSpace(mustAdmin(t, "release", "fingerprint", "--public-key", f.pub))
	if out, _, err := f.install("--public-key", f.pub, "--verifier", f.verifier(), "--key-fingerprint", fingerprint); err != nil {
		t.Fatalf("signed first install: %v\n%s", err, out)
	}

	// The owner of the directories above the prefix replaces the prefix with a marker and their own pin, and
	// signs a malicious release with their own key.
	attacker := filepath.Join(f.dir, "attacker")
	mustAdmin(t, "release", "keygen", "--out", attacker)
	f.writeArchive("malicious")
	f.resign(attacker)
	attackerKey, err := os.ReadFile(filepath.Join(attacker, "release-signing.pub"))
	if err != nil {
		t.Fatal(err)
	}
	f.asRoot = true
	prefix := filepath.Join(f.dir, "home", "prefix")
	if err := os.WriteFile(filepath.Join(prefix, ".tbound-publisher.pub"), attackerKey, 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err := f.installCommand("update", "--verifier", f.verifier())
	if err == nil || !strings.Contains(out, "refusing to act as root") || !strings.Contains(out, "owned by uid") {
		t.Fatalf("a root update accepted a prefix whose ancestors another user owns: %v\n%s", err, out)
	}
	if got := installedMarker(t, prefix); got != "genuine" {
		t.Fatalf("a refused root update replaced the installation: %q", got)
	}

	if err := os.RemoveAll(prefix); err != nil {
		t.Fatal(err)
	}
	if out, _, err := f.install("--public-key", f.pub, "--verifier", f.verifier()); err == nil || !strings.Contains(out, "refusing to act as root") {
		t.Fatalf("a fresh root install below a directory another user owns was accepted: %v\n%s", err, out)
	}
	if _, statErr := os.Lstat(prefix); statErr == nil {
		t.Fatal("a refused root install created the prefix")
	}
}

// installShellFunction runs one install.sh function with arguments in a shell that sourced the script.
func installShellFunction(t *testing.T, function string, args ...string) (string, error) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not available")
	}
	cmd := exec.Command(bash, append([]string{"-c", `. "$1"; shift; "$@"`, "tbound-test", "../../../install/install.sh", function}, args...)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestProductAdminReleaseInstallScriptRootTrustChecksRefuseSymlinksForeignOwnersAndWritablePaths(t *testing.T) {
	uid := strconv.Itoa(os.Geteuid())
	dir := t.TempDir()
	write := func(name string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("key\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write("good", 0o644)
	if out, err := installShellFunction(t, "trusted_entry", good, uid); err != nil {
		t.Fatalf("a file of the trusted owner that only the owner writes was refused: %v\n%s", err, out)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct{ path, uid, want string }{
		"symlink to a trusted file": {link, uid, "is a symlink"},
		"group writable":            {write("group", 0o664), uid, "writable by group or others"},
		"other writable":            {write("other", 0o646), uid, "writable by group or others"},
	}
	if os.Geteuid() != 0 {
		cases["owned by someone else"] = struct{ path, uid, want string }{good, "0", "owned by uid " + uid}
	}
	for name, c := range cases {
		out, err := installShellFunction(t, "trusted_entry", c.path, c.uid)
		if err == nil || !strings.Contains(out, "refusing to act as root") || !strings.Contains(out, c.want) {
			t.Errorf("%s: want a refusal naming %q: %v\n%s", name, c.want, err, out)
		}
	}
	for _, p := range []string{"/usr", "/usr/tbound-no-such-dir/prefix"} {
		if out, err := installShellFunction(t, "require_trusted_path", p, "0"); err != nil {
			t.Errorf("%s: a chain of root-owned, non-writable directories was refused: %v\n%s", p, err, out)
		}
	}
}
