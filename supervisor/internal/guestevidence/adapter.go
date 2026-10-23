package guestevidence

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// KindAdapterTests is the capture of the Pi adapter's TypeScript tests (EVQ-SURFACE-ADAPTER-EQUIVALENCE).
// Unlike the guest kinds it is made by `tbound-run evq-adapter-capture` on any machine with the Node
// runtime and an offline npm cache, from a `git archive` of the frozen commit. Its envelope
// binary_sha256 is the digest of the tbound-run executable that made it and is not compared with the
// guest binary; the artifact is bound to the commit by the commit's git tree id, which `evq` compares
// with the tree id of the Go capture.
const KindAdapterTests = "adapter-tests"

// AdapterMinNode is the lowest Node release the adapter's package.json accepts (engines.node).
var AdapterMinNode = [3]int{22, 19, 0}

// AdapterPiCLITest is the one test that needs a Linux host, Node v24.21.0 and the official pi CLI. It
// may be skipped unless the environment of the run required it.
const AdapterPiCLITest = "official Pi CLI 0.87.1 loads the mediated extension and settles simulated host-fixture denials"

// AdapterRequiredTests are the top-level tests that must pass. Together they cover the registered
// model, the Go turn projection, the frame codec shared with Go, the message checks, and the proxy
// tools: all four registered tools (read, write, edit, bash) each round-trip one proposal.
var AdapterRequiredTests = []string{
	"adapter's registered model is the broker-approved model",
	"Go turn projection must match its fixed model and finish/call shape",
	"provider cancellation sends only the matching bounded cancel frame",
	"Go-compatible frame golden vector encodes and decodes",
	"rejects oversized, truncated, and malformed frames",
	"checks token, sequence, replay, and message direction",
	"proxy tools each round-trip one proposal and return its correlated result",
}

// The exact invocations of the capture.
var (
	AdapterInstallArgv = []string{"npm", "ci", "--offline", "--ignore-scripts", "--no-audit", "--no-fund"}
	AdapterCheckArgv   = []string{"npm", "run", "check"}
)

// MaxAdapterOutput bounds the recorded output of the check run.
const MaxAdapterOutput = 256 << 10

// AdapterRun is the data of an adapter-tests artifact.
type AdapterRun struct {
	CommitTree        string            `json:"commit_tree"`
	AdapterTree       string            `json:"adapter_tree"`
	AdapterTreeDigest string            `json:"adapter_tree_digest"`
	AdapterFiles      int               `json:"adapter_files"`
	PackageJSONSHA256 string            `json:"package_json_sha256"`
	PackageLockSHA256 string            `json:"package_lock_sha256"`
	Node              AdapterNode       `json:"node"`
	Npm               AdapterNpm        `json:"npm"`
	Argv              []string          `json:"argv"`
	TestReporter      string            `json:"test_reporter"`
	Env               map[string]string `json:"env"`
	ExitCode          int               `json:"exit_code"`
	Output            string            `json:"output"`
	OutputSHA256      string            `json:"output_sha256"`
	Results           []TestResult      `json:"results"`
}

// AdapterNode identifies the Node runtime that ran the tests.
type AdapterNode struct {
	Version  string `json:"version"`
	SHA256   string `json:"sha256"`
	Platform string `json:"platform"`
	Arch     string `json:"arch"`
	// ExecPath is process.execPath as npm reported it from inside a lifecycle script of the run
	// (npm_node_execpath): the Node the package scripts ran under. The capture refuses a run whose value
	// is not the selected Node.
	ExecPath string `json:"exec_path"`
}

// AdapterNpm identifies the offline install that made adapter/node_modules from the lockfile.
type AdapterNpm struct {
	Version string `json:"version"`
	// CLISHA256 is the digest of npm-cli.js, which the selected Node ran: npm is never executed directly.
	CLISHA256         string   `json:"cli_sha256"`
	InstallArgv       []string `json:"install_argv"`
	InstallExit       int      `json:"install_exit"`
	NodeModulesDigest string   `json:"node_modules_digest"`
	NodeModulesFiles  int      `json:"node_modules_files"`
}

// AdapterSummary is the counters node:test prints at the end of a run.
type AdapterSummary struct {
	Tests, Pass, Fail, Cancelled, Skipped, Todo int
}

var (
	adapterResultRE  = regexp.MustCompile(`^( *)(✔|✖|﹣) (.+?) \([0-9.]+ms\)(?: # .*)?$`)
	adapterSummaryRE = regexp.MustCompile(`^ℹ (tests|suites|pass|fail|cancelled|skipped|todo|duration_ms) ([0-9.]+)$`)
	nodeVersionRE    = regexp.MustCompile(`^v([0-9]+)\.([0-9]+)\.([0-9]+)$`)
	adapterPlainEnv  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ParseAdapterOutput reads the spec reporter output of `node --test --test-reporter=spec`:
// the top-level result lines (indentation 0), the number of result lines at every depth and the final
// counters. A top-level test with subtests is reported by its own closing line.
func ParseAdapterOutput(output string) (results []TestResult, lines int, sum AdapterSummary, err error) {
	seenSummary := map[string]int{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := adapterResultRE.FindStringSubmatch(line); m != nil {
			lines++
			if m[1] != "" {
				continue
			}
			action := TestPass
			switch m[2] {
			case "✖":
				action = TestFail
			case "﹣":
				action = TestSkip
			}
			results = append(results, TestResult{Test: m[3], Action: action})
			continue
		}
		if m := adapterSummaryRE.FindStringSubmatch(line); m != nil && m[1] != "duration_ms" {
			n, convErr := strconv.Atoi(m[2])
			if convErr != nil {
				return nil, 0, sum, fmt.Errorf("summary counter %q", line)
			}
			if _, dup := seenSummary[m[1]]; dup {
				return nil, 0, sum, fmt.Errorf("summary counter %s is printed twice", m[1])
			}
			seenSummary[m[1]] = n
		}
	}
	for _, k := range []string{"tests", "suites", "pass", "fail", "cancelled", "skipped", "todo"} {
		if _, ok := seenSummary[k]; !ok {
			return nil, 0, sum, fmt.Errorf("the output has no %q counter: the run did not finish", k)
		}
	}
	sum = AdapterSummary{Tests: seenSummary["tests"], Pass: seenSummary["pass"], Fail: seenSummary["fail"],
		Cancelled: seenSummary["cancelled"], Skipped: seenSummary["skipped"], Todo: seenSummary["todo"]}
	return results, lines, sum, nil
}

func parseNodeVersion(v string) ([3]int, bool) {
	m := nodeVersionRE.FindStringSubmatch(v)
	if m == nil {
		return [3]int{}, false
	}
	var out [3]int
	for i := range out {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

func validateAdapter(raw []byte, e Expect) error {
	var r AdapterRun
	if err := DecodeStrict(raw, &r); err != nil {
		return err
	}
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	switch {
	case !IsCommit(e.CommitTree):
		add("the capture of the frozen commit gave no git tree id to bind the adapter run to")
	case r.CommitTree != e.CommitTree:
		add("the run is of the tree %q, the capture of the frozen commit is of %q", r.CommitTree, e.CommitTree)
	}
	if !IsCommit(r.AdapterTree) || !IsDigest(r.AdapterTreeDigest) || r.AdapterFiles <= 0 {
		add("adapter/ is not identified (tree %q, digest %q, %d files)", r.AdapterTree, r.AdapterTreeDigest, r.AdapterFiles)
	}
	if !IsDigest(r.PackageJSONSHA256) || !IsDigest(r.PackageLockSHA256) {
		add("package.json or package-lock.json is not digested")
	}

	ver, ok := parseNodeVersion(r.Node.Version)
	switch {
	case !ok:
		add("node version %q is not vMAJOR.MINOR.PATCH", r.Node.Version)
	case slices.Compare(ver[:], AdapterMinNode[:]) < 0:
		add("node %s is below the engines minimum v%d.%d.%d", r.Node.Version, AdapterMinNode[0], AdapterMinNode[1], AdapterMinNode[2])
	}
	if !IsDigest(r.Node.SHA256) || r.Node.Platform == "" || r.Node.Arch == "" {
		add("the Node runtime is not identified (digest %q, platform %q, arch %q)", r.Node.SHA256, r.Node.Platform, r.Node.Arch)
	}

	if !path.IsAbs(r.Node.ExecPath) || path.Clean(r.Node.ExecPath) != r.Node.ExecPath {
		add("node exec path %q is not a clean absolute path", r.Node.ExecPath)
	}
	if !IsDigest(r.Npm.CLISHA256) {
		add("npm-cli.js is not digested")
	}
	if strings.TrimSpace(r.Npm.Version) == "" {
		add("npm version is missing")
	}
	if !slices.Equal(r.Npm.InstallArgv, AdapterInstallArgv) {
		add("install invocation %q, want the offline lockfile install %q", r.Npm.InstallArgv, AdapterInstallArgv)
	}
	if r.Npm.InstallExit != 0 {
		add("the offline install exited %d", r.Npm.InstallExit)
	}
	if !IsDigest(r.Npm.NodeModulesDigest) || r.Npm.NodeModulesFiles <= 0 {
		add("the installed node_modules is not digested")
	}

	if !slices.Equal(r.Argv, AdapterCheckArgv) {
		add("invocation %q, want %q", r.Argv, AdapterCheckArgv)
	}
	if r.TestReporter != "spec" {
		add("test reporter %q, want spec", r.TestReporter)
	}
	if r.ExitCode != 0 {
		add("the check exited %d", r.ExitCode)
	}
	for k := range r.Env {
		if !adapterPlainEnv.MatchString(k) {
			add("environment name %q", k)
		}
	}
	if _, has := r.Env["NODE_OPTIONS"]; has {
		add("NODE_OPTIONS was set: it can change what the tests run")
	}
	if len(r.Output) == 0 || len(r.Output) > MaxAdapterOutput {
		add("output size %d is outside 1..%d bytes", len(r.Output), MaxAdapterOutput)
	}
	sum := sha256.Sum256([]byte(r.Output))
	if r.OutputSHA256 != "sha256:"+hex.EncodeToString(sum[:]) {
		add("output_sha256 does not match the output")
	}

	parsed, lines, counters, err := ParseAdapterOutput(r.Output)
	if err != nil {
		add("%v", err)
	} else {
		if !slices.Equal(parsed, r.Results) {
			add("the stated results differ from the results in the output")
		}
		switch {
		case counters.Fail != 0 || counters.Cancelled != 0 || counters.Todo != 0:
			add("the run reports fail %d, cancelled %d, todo %d", counters.Fail, counters.Cancelled, counters.Todo)
		case counters.Pass+counters.Skipped != counters.Tests || lines != counters.Tests:
			add("the counters (tests %d, pass %d, skipped %d) do not agree with the %d result lines", counters.Tests, counters.Pass, counters.Skipped, lines)
		}
	}

	seen := map[string]string{}
	for _, res := range r.Results {
		if res.Test == "" || (res.Action != TestPass && res.Action != TestFail && res.Action != TestSkip) {
			add("result %+v is malformed", res)
			continue
		}
		if prev, dup := seen[res.Test]; dup {
			add("test %q is reported twice (%s, %s)", res.Test, prev, res.Action)
			continue
		}
		seen[res.Test] = res.Action
		if res.Action == TestFail {
			add("test %q failed", res.Test)
		}
	}
	for _, name := range AdapterRequiredTests {
		if seen[name] != TestPass {
			add("required test %q did not pass (%q)", name, seen[name])
		}
	}
	switch seen[AdapterPiCLITest] {
	case TestPass:
	case TestSkip:
		if r.Env["TBOUND_REQUIRE_PI_CLI_INTEGRATION"] == "1" || r.Env["TBOUND_PI_CLI_BIN"] != "" {
			add("the official Pi CLI test was required by the environment and was skipped")
		}
	default:
		add("the official Pi CLI test is %q: it must pass or be skipped", seen[AdapterPiCLITest])
	}

	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}
