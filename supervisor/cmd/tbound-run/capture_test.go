package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"tbound/supervisor/internal/guestevidence"
)

// capSpec describes a synthetic capture directory. It is what `evq-capture` would have written for
// a run with these packages; the verifier is tested on it, the capture itself in evqcapture_linux_test.go.
type capSpec struct {
	commit  string
	pkgs    []pkgRun
	mapping []byte
	matrix  []byte
	// mutate may change the record and the test JSON after the record's digest of the JSON was taken.
	mutate func(*evqCapture, *[]byte)
}

func lfDigest(data []byte) string {
	return sha256Of(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")))
}

// testBinaries is the guest test binary digest map a capture records.
func testBinaries() map[string]string {
	m := map[string]string{}
	for _, p := range guestevidence.TestPackages() {
		m[p] = fixtureDigest("test binary " + p)
	}
	return m
}

func writeCapture(t *testing.T, dir string, s capSpec) string {
	t.Helper()
	if s.commit == "" {
		s.commit = commit
	}
	if s.mapping == nil {
		s.mapping = []byte("mapping")
	}
	if s.matrix == nil {
		s.matrix = []byte("matrix")
	}
	tj := wellFormed(s.pkgs...)
	o := outcomesOf(t, tj)
	rec := evqCapture{
		Schema: evqCaptureSchema, Commit: s.commit, GitTree: strings.Repeat("b", 40), ArchiveTreeDigest: fixtureDigest("tree"), ArchiveFiles: 12,
		PinnedInputs: map[string]string{evqPinnedInputs[0]: lfDigest(s.mapping), evqPinnedInputs[1]: lfDigest(s.matrix)},
		GoVersion:    evqPinnedGoVersion, GoBinarySHA256: fixtureDigest("go"), Argv: slices.Clone(evqCaptureArgv), WorkDir: evqModuleDir,
		Env:      map[string]string{"GOFLAGS": "-mod=readonly", "GOTOOLCHAIN": "local", "GOSUMDB": "off", "GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "1", "GOPROXY": "off"},
		Packages: o.packageNames(), TestJSONSHA256: sha256Of(tj), TestBinaries: testBinaries(),
	}
	if o.anyFailed() {
		rec.ExitStatus = 1
	}
	if s.mutate != nil {
		s.mutate(&rec, &tj)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, evqCaptureRecord), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, evqCaptureTestJSON), tj, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadEVQCaptureAcceptsAGoodCapture(t *testing.T) {
	dir := writeCapture(t, t.TempDir(), capSpec{pkgs: []pkgRun{passingPkg(pkgA, "TestX"), passingPkg(pkgB, "TestY")}})
	rec, digest, o, err := loadEVQCapture(dir, commit)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, evqCaptureRecord))
	if digest != sha256Of(data) || rec.Commit != commit || !o.passed(pkgA, "TestX") || !o.passed(pkgB, "TestY") {
		t.Fatalf("capture = %+v %s", rec, digest)
	}
}

// Every property the capture is trusted for is re-checked; any one failing refuses the capture.
func TestLoadEVQCaptureRefusals(t *testing.T) {
	good := []pkgRun{passingPkg(pkgA, "TestX"), passingPkg(pkgB, "TestY")}
	failing := []pkgRun{passingPkg(pkgA, "TestX"), {pkg: pkgB, tests: []testRun{{"TestY", "fail"}}, result: "fail"}}
	otherCommit := strings.Repeat("c", 40)
	for name, c := range map[string]struct {
		freeze string
		spec   capSpec
	}{
		"another commit than run.json freezes": {commit, capSpec{commit: otherCommit, pkgs: good}},
		"run.json freezes another commit":      {otherCommit, capSpec{pkgs: good}},
		"no freeze commit":                     {"", capSpec{pkgs: good}},
		"wrong schema":                         {commit, capSpec{pkgs: good, mutate: func(r *evqCapture, _ *[]byte) { r.Schema = "tbound-evq-capture/v0" }}},
		"-run filter": {commit, capSpec{pkgs: good, mutate: func(r *evqCapture, _ *[]byte) {
			r.Argv = []string{"go", "test", "-race", "-count=1", "-json", "-run", "TestX", "./..."}
		}}},
		"-skip filter": {commit, capSpec{pkgs: good, mutate: func(r *evqCapture, _ *[]byte) {
			r.Argv = []string{"go", "test", "-race", "-count=1", "-json", "-skip", "TestY", "./..."}
		}}},
		"-short": {commit, capSpec{pkgs: good, mutate: func(r *evqCapture, _ *[]byte) {
			r.Argv = []string{"go", "test", "-race", "-count=1", "-short", "-json", "./..."}
		}}},
		"one package only": {commit, capSpec{pkgs: good, mutate: func(r *evqCapture, _ *[]byte) {
			r.Argv = []string{"go", "test", "-race", "-count=1", "-json", "./internal/alpha"}
		}}},
		"no race": {commit, capSpec{pkgs: good, mutate: func(r *evqCapture, _ *[]byte) {
			r.Argv = []string{"go", "test", "-count=1", "-json", "./..."}
		}}},
		"cached run": {commit, capSpec{pkgs: good, mutate: func(r *evqCapture, _ *[]byte) {
			r.Argv = []string{"go", "test", "-race", "-json", "./..."}
		}}},
		"another toolchain":  {commit, capSpec{pkgs: good, mutate: func(r *evqCapture, _ *[]byte) { r.GoVersion = "go version go1.26.0 linux/amd64" }}},
		"mod=mod":            {commit, capSpec{pkgs: good, mutate: func(r *evqCapture, _ *[]byte) { r.Env["GOFLAGS"] = "-mod=mod" }}},
		"toolchain download": {commit, capSpec{pkgs: good, mutate: func(r *evqCapture, _ *[]byte) { r.Env["GOTOOLCHAIN"] = "auto" }}},
		"no cgo":             {commit, capSpec{pkgs: good, mutate: func(r *evqCapture, _ *[]byte) { r.Env["CGO_ENABLED"] = "0" }}},
		"windows":            {commit, capSpec{pkgs: good, mutate: func(r *evqCapture, _ *[]byte) { r.Env["GOOS"] = "windows" }}},
		"no archive tree":    {commit, capSpec{pkgs: good, mutate: func(r *evqCapture, _ *[]byte) { r.ArchiveTreeDigest = "" }}},
		"unknown exit":       {commit, capSpec{pkgs: good, mutate: func(r *evqCapture, _ *[]byte) { r.ExitStatus = 2 }}},
		"test json tampered": {commit, capSpec{pkgs: good, mutate: func(_ *evqCapture, tj *[]byte) {
			*tj = wellFormed(passingPkg(pkgA, "TestX"), passingPkg(pkgB, "TestY"), passingPkg(pkgC, "TestZ"))
		}}},
		"truncated test json": {commit, capSpec{pkgs: good, mutate: func(r *evqCapture, tj *[]byte) {
			lines := strings.Split(strings.TrimSpace(string(*tj)), "\n")
			*tj = []byte(strings.Join(lines[:len(lines)-1], "\n") + "\n") // the last package never finishes
			r.TestJSONSHA256 = sha256Of(*tj)
		}}},
		"a package is missing from the test json": {commit, capSpec{pkgs: good, mutate: func(r *evqCapture, tj *[]byte) {
			*tj = wellFormed(passingPkg(pkgA, "TestX"))
			r.TestJSONSHA256 = sha256Of(*tj)
		}}},
		"a package the module does not have": {commit, capSpec{pkgs: good, mutate: func(r *evqCapture, _ *[]byte) { r.Packages = append(r.Packages, pkgC) }}},
		"exit 0 with a failure":              {commit, capSpec{pkgs: failing, mutate: func(r *evqCapture, _ *[]byte) { r.ExitStatus = 0 }}},
		"exit 1 without a failure":           {commit, capSpec{pkgs: good, mutate: func(r *evqCapture, _ *[]byte) { r.ExitStatus = 1 }}},
		"no test binary for a guest package": {commit, capSpec{pkgs: good, mutate: func(r *evqCapture, _ *[]byte) { r.TestBinaries = map[string]string{} }}},
		"no digest of the mapping":           {commit, capSpec{pkgs: good, mutate: func(r *evqCapture, _ *[]byte) { delete(r.PinnedInputs, evqPinnedInputs[0]) }}},
	} {
		dir := writeCapture(t, t.TempDir(), c.spec)
		if _, _, _, err := loadEVQCapture(dir, c.freeze); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A capture with a failing test is still a capture: the failure is the evidence.
	dir := writeCapture(t, t.TempDir(), capSpec{pkgs: failing})
	if _, _, o, err := loadEVQCapture(dir, commit); err != nil || o.passed(pkgB, "TestY") || !o.passed(pkgA, "TestX") {
		t.Fatalf("failing capture: %v", err)
	}
	// Missing files and a record that is not strict JSON.
	empty := t.TempDir()
	if _, _, _, err := loadEVQCapture(empty, commit); err == nil {
		t.Error("an empty directory was accepted")
	}
	dir = writeCapture(t, t.TempDir(), capSpec{pkgs: good})
	data, _ := os.ReadFile(filepath.Join(dir, evqCaptureRecord))
	if err := os.WriteFile(filepath.Join(dir, evqCaptureRecord), bytes.Replace(data, []byte(`"schema"`), []byte(`"extra": 1, "schema"`), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := loadEVQCapture(dir, commit); err == nil {
		t.Error("a record with an unknown member was accepted")
	}
}

func TestRequirePinnedInputIsTheFrozenCommitsCopy(t *testing.T) {
	rec := evqCapture{PinnedInputs: map[string]string{evqPinnedInputs[0]: lfDigest([]byte("a\nb\n"))}}
	if err := rec.requirePinnedInput(evqPinnedInputs[0], []byte("a\nb\n")); err != nil {
		t.Fatal(err)
	}
	if err := rec.requirePinnedInput(evqPinnedInputs[0], []byte("a\r\nb\r\n")); err != nil {
		t.Fatalf("a CRLF checkout of the same bytes: %v", err)
	}
	if err := rec.requirePinnedInput(evqPinnedInputs[0], []byte("a\nc\n")); err == nil {
		t.Fatal("a mapping that is not the commit's copy was accepted")
	}
}

func TestLoadFreezeBinding(t *testing.T) {
	dir := t.TempDir()
	write := func(content string) string {
		p := filepath.Join(dir, "run.json")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	b, err := loadFreezeBinding(write(`{"freeze_commit":"` + commit + `","pins":{"profile_hash":"p","frozen_source_digest":"f","containment_profile_digest":"c"},"other":1}`))
	if err != nil || b.Commit != commit || b.ProfileHash != "p" || b.FrozenSourceDigest != "f" || b.ContainmentProfileDigest != "c" {
		t.Fatalf("binding = %+v %v", b, err)
	}
	for name, content := range map[string]string{
		"no commit":      `{"pins":{}}`,
		"short commit":   `{"freeze_commit":"abc"}`,
		"upper case":     `{"freeze_commit":"` + strings.ToUpper(commit) + `"}`,
		"duplicate key":  `{"freeze_commit":"` + commit + `","freeze_commit":"` + commit + `"}`,
		"trailing data":  `{"freeze_commit":"` + commit + `"} {}`,
		"not an object":  `[]`,
		"commit numeric": `{"freeze_commit":1}`,
	} {
		if _, err := loadFreezeBinding(write(content)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
