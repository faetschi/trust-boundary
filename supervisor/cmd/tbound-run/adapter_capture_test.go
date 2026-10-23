package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"tbound/supervisor/internal/guestevidence"
)

// The adapter capture is exercised with a throwaway git repository and fake node and npm executables
// (shell scripts): the real ones are exercised by the recorded capture of the frozen commit.

func requireAdapterTools(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the adapter capture runs under a POSIX environment")
	}
	for _, tool := range []string{"git", "sh"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
}

// specOutput is an all-green spec reporter output that has every required test and a skipped Pi CLI test.
func specOutput(failing string) string {
	var b strings.Builder
	b.WriteString("npm notice run node scripts/test.mjs\n")
	n := 0
	for _, name := range guestevidence.AdapterRequiredTests {
		mark := "✔"
		if name == failing {
			mark = "✖"
		}
		fmt.Fprintf(&b, "%s %s (1.5ms)\n", mark, name)
		n++
	}
	fmt.Fprintf(&b, "﹣ %s (0.1ms) # set TBOUND_REQUIRE_PI_CLI_INTEGRATION=1\n", guestevidence.AdapterPiCLITest)
	n++
	failed := 0
	if failing != "" {
		failed = 1
	}
	fmt.Fprintf(&b, "ℹ tests %d\nℹ suites 0\nℹ pass %d\nℹ fail %d\nℹ cancelled 0\nℹ skipped 1\nℹ todo 0\nℹ duration_ms 9.5\n", n, n-1-failed, failed)
	return b.String()
}

type fakeTools struct {
	node, npmCLI, cache string
	// nodeLog records the first argument of every node call.
	nodeLog string
}

// fakeNpmReport is what `npm run env` reports from inside a lifecycle script: the Node npm runs under and
// the PATH the scripts resolve `node` on. The empty value is the honest report of the selected node.
type fakeNpmReport struct{ execPath, path string }

// makeFakeTools writes a node and an npm-cli.js script (the fake node runs its first argument with sh, as
// the real one runs a JavaScript file). checkBody runs inside `npm run check`, in the adapter directory.
func makeFakeTools(t *testing.T, installExit int, checkBody string, checkExit int) fakeTools {
	return makeFakeToolsReporting(t, installExit, checkBody, checkExit, fakeNpmReport{})
}

func makeFakeToolsReporting(t *testing.T, installExit int, checkBody string, checkExit int, report fakeNpmReport) fakeTools {
	t.Helper()
	dir := t.TempDir()
	node := filepath.Join(dir, "node")
	nodeLog := filepath.Join(dir, "node.log")
	script := "#!/bin/sh\necho \"$1\" >> " + nodeLog + "\nif [ \"$1\" = \"-p\" ]; then echo 'v24.15.0 linux x64'; exit 0; fi\nexec /bin/sh \"$@\"\n"
	if err := os.WriteFile(node, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if report.execPath == "" {
		report.execPath = node
	}
	if report.path == "" {
		report.path = dir + ":/usr/bin:/bin"
	}
	npm := filepath.Join(dir, "npm-cli.js")
	npmScript := `case "$1" in
--version) echo 11.6.0 ;;
ci)
  [ "$npm_config_cache" = "` + filepath.Join(dir, "cache") + `" ] || exit 9
  case " $* " in *" --offline "*) ;; *) exit 8 ;; esac
  [ ` + fmt.Sprint(installExit) + ` = 0 ] || exit ` + fmt.Sprint(installExit) + `
  mkdir -p node_modules/.bin node_modules/pkg && echo module > node_modules/pkg/index.js && ln -s ../pkg/index.js node_modules/.bin/pkg ;;
run)
  if [ "$2" = env ]; then echo "npm_node_execpath=` + report.execPath + `"; echo "PATH=` + report.path + `"; exit 0; fi
  [ -d node_modules/pkg ] && [ -f package-lock.json ] || exit 7
  ` + checkBody + `
  cat <<'OUT'
` + specOutput("") + `OUT
  exit ` + fmt.Sprint(checkExit) + ` ;;
esac
`
	if err := os.WriteFile(npm, []byte(npmScript), 0o755); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(dir, "cache")
	if err := os.Mkdir(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	return fakeTools{node: node, npmCLI: npm, cache: cache, nodeLog: nodeLog}
}

func adapterRepo(t *testing.T, lock bool) (string, string, map[string]miniFile) {
	t.Helper()
	files := map[string]miniFile{
		"adapter/package.json": {"{\"name\":\"x\"}\n", 0o644},
		"adapter/src/a.ts":     {"export const a = 1;\n", 0o644},
		"docs/note.md":         {"note\n", 0o644},
	}
	files["scripts/run.sh"] = miniFile{"#!/bin/sh\n", 0o755}
	if lock {
		files["adapter/package-lock.json"] = miniFile{"{\"lockfileVersion\":3}\n", 0o644}
	}
	repo, commit := makeMiniRepo(t, files, nil)
	return repo, commit, files
}

func (f fakeTools) options(repo, commit, out string) adapterCaptureOptions {
	return adapterCaptureOptions{repo: repo, commit: commit, out: out, nodeBin: f.node, npmCLI: f.npmCLI, npmCache: f.cache, gitBin: "git", stdout: &bytes.Buffer{}}
}

func TestAdapterCaptureWritesAValidatedArtifactBoundToTheTree(t *testing.T) {
	requireAdapterTools(t)
	repo, commit, files := adapterRepo(t, true)
	tools := makeFakeTools(t, 0, ":", 0)
	out := filepath.Join(t.TempDir(), "adapter.json")
	if err := runAdapterCapture(context.Background(), tools.options(repo, commit, out)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	tree := gitIn(t, repo, "rev-parse", commit+"^{tree}")
	if err := guestevidence.Validate(guestevidence.KindAdapterTests, data, guestevidence.Expect{Commit: commit, CommitTree: tree}); err != nil {
		t.Fatalf("the artifact is refused: %v", err)
	}
	if err := guestevidence.Validate(guestevidence.KindAdapterTests, data, guestevidence.Expect{Commit: commit, CommitTree: strings.Repeat("0", 40)}); err == nil {
		t.Error("the artifact was accepted for another tree")
	}
	var env struct {
		Data guestevidence.AdapterRun `json:"data"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	r := env.Data
	if r.TestReporter != "spec" {
		t.Errorf("test_reporter = %q, want spec", r.TestReporter)
	}
	adapterOnly := map[string]miniFile{}
	for n, f := range files {
		if strings.HasPrefix(n, "adapter/") {
			adapterOnly[n] = f
		}
	}
	if r.AdapterTreeDigest != expectedTreeDigest(adapterOnly) || r.AdapterFiles != 3 || r.AdapterTree != gitIn(t, repo, "rev-parse", commit+":adapter") {
		t.Errorf("adapter identity = %+v", r)
	}
	lock := sha256.Sum256([]byte(files["adapter/package-lock.json"].content))
	if r.PackageLockSHA256 != "sha256:"+hex.EncodeToString(lock[:]) || r.Node.Version != "v24.15.0" || r.Npm.Version != "11.6.0" || r.Npm.NodeModulesFiles != 2 {
		t.Errorf("record = %+v", r)
	}
	if len(r.Results) != len(guestevidence.AdapterRequiredTests)+1 {
		t.Errorf("results = %+v", r.Results)
	}
	cliData, err := os.ReadFile(tools.npmCLI)
	if err != nil {
		t.Fatal(err)
	}
	cliSum := sha256.Sum256(cliData)
	if r.Npm.CLISHA256 != "sha256:"+hex.EncodeToString(cliSum[:]) || r.Node.ExecPath != tools.node {
		t.Errorf("npm cli digest %q, node exec path %q: want the digest of %s and %s", r.Npm.CLISHA256, r.Node.ExecPath, tools.npmCLI, tools.node)
	}
	log, err := os.ReadFile(tools.nodeLog)
	if err != nil {
		t.Fatal(err)
	}
	// Every npm invocation goes through the selected node: its first argument is npm-cli.js, never an npm executable run directly.
	if got := strings.Count(string(log), tools.npmCLI+"\n"); got < 4 {
		t.Errorf("node ran npm-cli.js %d times, want every npm call (version, ci, run env, run check):\n%s", got, log)
	}
	if err := runAdapterCapture(context.Background(), tools.options(repo, commit, out)); err == nil {
		t.Error("an existing artifact was overwritten")
	}
}

func TestAdapterCaptureRefusesAndLeavesNoArtifact(t *testing.T) {
	requireAdapterTools(t)
	repo, commit, _ := adapterRepo(t, true)
	noLockRepo, noLockCommit, _ := adapterRepo(t, false)
	otherNodeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(otherNodeDir, "node"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		repo, commit string
		tools        fakeTools
		mutate       func(*adapterCaptureOptions)
	}{
		"the install fails offline":                  {repo, commit, makeFakeTools(t, 1, ":", 0), nil},
		"the check fails":                            {repo, commit, makeFakeTools(t, 0, ":", 1), nil},
		"the check changes the tree":                 {repo, commit, makeFakeTools(t, 0, "echo changed >> src/a.ts", 0), nil},
		"the check adds a file":                      {repo, commit, makeFakeTools(t, 0, "echo new > src/new.ts", 0), nil},
		"no lockfile in the commit":                  {noLockRepo, noLockCommit, makeFakeTools(t, 0, ":", 0), nil},
		"an abbreviated commit":                      {repo, commit[:12], makeFakeTools(t, 0, ":", 0), nil},
		"a commit that does not exist":               {repo, strings.Repeat("1", 40), makeFakeTools(t, 0, ":", 0), nil},
		"a relative output path":                     {repo, commit, makeFakeTools(t, 0, ":", 0), func(o *adapterCaptureOptions) { o.out = "adapter.json" }},
		"a missing npm cache":                        {repo, commit, makeFakeTools(t, 0, ":", 0), func(o *adapterCaptureOptions) { o.npmCache = filepath.Join(t.TempDir(), "none") }},
		"a relative node path":                       {repo, commit, makeFakeTools(t, 0, ":", 0), func(o *adapterCaptureOptions) { o.nodeBin = "node" }},
		"npm reports another node":                   {repo, commit, makeFakeToolsReporting(t, 0, ":", 0, fakeNpmReport{execPath: "/usr/bin/node"}), nil},
		"scripts resolve node on another PATH entry": {repo, commit, makeFakeToolsReporting(t, 0, ":", 0, fakeNpmReport{path: otherNodeDir + ":/usr/bin"}), nil},
		"scripts have no selected node on PATH":      {repo, commit, makeFakeToolsReporting(t, 0, ":", 0, fakeNpmReport{path: "/nonexistent:/usr/bin"}), nil},
		"a relative npm-cli path":                    {repo, commit, makeFakeTools(t, 0, ":", 0), func(o *adapterCaptureOptions) { o.npmCLI = "npm-cli.js" }},
		"a required Pi CLI that skips":               {repo, commit, makeFakeTools(t, 0, ":", 0), func(o *adapterCaptureOptions) { o.piCLIBin = "/opt/pi" }},
	}
	for name, c := range cases {
		out := filepath.Join(t.TempDir(), "adapter.json")
		o := c.tools.options(c.repo, c.commit, out)
		if c.mutate != nil {
			c.mutate(&o)
		}
		if err := runAdapterCapture(context.Background(), o); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if _, err := os.Lstat(out); err == nil {
			t.Errorf("%s: an artifact was left behind", name)
		}
	}
}

func TestEVQAcceptsAnAdapterArtifactWithoutAGuestBinary(t *testing.T) {
	requireAdapterTools(t)
	// The usage check only: a guest kind needs --guest-binary-sha256, the adapter kind does not.
	a := &app{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
	err := a.evqCmd([]string{"--mapping", "m", "--config", "c", "--capture", "k", "--out", "o", "--artifact", "guest-capability=f"})
	if err == nil || !strings.Contains(err.Error(), "needs --guest-binary-sha256") {
		t.Fatalf("a guest kind without the binary digest: %v", err)
	}
	err = a.evqCmd([]string{"--mapping", "m", "--config", "c", "--capture", "k", "--out", "o", "--artifact", "adapter-tests=f"})
	if err != nil && strings.Contains(err.Error(), "needs --guest-binary-sha256") {
		t.Fatalf("an adapter artifact asked for a guest binary digest: %v", err)
	}
}
