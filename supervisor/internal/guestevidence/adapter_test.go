package guestevidence

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"slices"
	"strings"
	"testing"
)

// adapterOutput is the shape of `npm run check` output with node:test's spec reporter:
// 18 tests of which 8 are top-level, and the official Pi CLI test is skipped.
const adapterOutput = `npm notice run tbound-pi-closure-spike@0.1.0 check
npm notice run npm run typecheck && npm test
npm notice run tsc --noEmit
npm notice run node scripts/test.mjs
✔ adapter's registered model is the broker-approved model (0.6237ms)
✔ Go turn projection must match its fixed model and finish/call shape (4.2986ms)
✔ provider cancellation sends only the matching bounded cancel frame (1.3027ms)
✔ Go-compatible frame golden vector encodes and decodes (2.6538ms)
▶ rejects oversized, truncated, and malformed frames
  ✔ clean EOF is a normal close (4.789ms)
  ✔ oversized announced length (1.2007ms)
  ✔ truncated prefix (0.6509ms)
  ✔ truncated payload (1.146ms)
  ✔ duplicate and unknown fields (2.37ms)
✔ rejects oversized, truncated, and malformed frames (11.2553ms)
▶ checks token, sequence, replay, and message direction
  ✔ binding token mismatch (0.7464ms)
  ✔ sequence must start at one and increase by one (0.7214ms)
  ✔ replayed sequence is rejected (1.0623ms)
  ✔ wrong direction is rejected (0.6149ms)
  ✔ unknown kind is rejected (0.4785ms)
✔ checks token, sequence, replay, and message direction (4.3411ms)
✔ proxy tools each round-trip one proposal and return its correlated result (4.6987ms)
﹣ official Pi CLI 0.87.1 loads the mediated extension and settles simulated host-fixture denials (0.5819ms) # set TBOUND_REQUIRE_PI_CLI_INTEGRATION=1 in CI or provide the pinned pi executable on PATH
ℹ tests 18
ℹ suites 0
ℹ pass 17
ℹ fail 0
ℹ cancelled 0
ℹ skipped 1
ℹ todo 0
ℹ duration_ms 739.0879
`

const adapterTree = "cccccccccccccccccccccccccccccccccccccccc"

func remakeAdapterOutput(r *AdapterRun, output string) {
	sum := sha256.Sum256([]byte(output))
	r.Output, r.OutputSHA256 = output, "sha256:"+hex.EncodeToString(sum[:])
	if results, _, _, err := ParseAdapterOutput(output); err == nil {
		r.Results = results
	}
}

func goodAdapter(t *testing.T) AdapterRun {
	t.Helper()
	r := AdapterRun{CommitTree: adapterTree, AdapterTree: strings.Repeat("d", 40), AdapterTreeDigest: digestOf("adapter tree"), AdapterFiles: 12,
		PackageJSONSHA256: digestOf("package.json"), PackageLockSHA256: digestOf("package-lock.json"),
		Node: AdapterNode{Version: "v24.15.0", SHA256: digestOf("node"), Platform: "linux", Arch: "x64", ExecPath: "/opt/node/bin/node"},
		Npm:  AdapterNpm{Version: "11.6.0", CLISHA256: digestOf("npm-cli"), InstallArgv: slices.Clone(AdapterInstallArgv), NodeModulesDigest: digestOf("node_modules"), NodeModulesFiles: 3000},
		Argv: slices.Clone(AdapterCheckArgv), TestReporter: "spec", Env: map[string]string{"LANG": "C", "NO_COLOR": "1"}}
	remakeAdapterOutput(&r, adapterOutput)
	if len(r.Results) == 0 {
		t.Fatal("the sample output has no results")
	}
	return r
}

func adapterExpect() Expect {
	e := expect()
	e.CommitTree = adapterTree
	return e
}

func TestParseAdapterOutputReadsTopLevelResults(t *testing.T) {
	results, lines, sum, err := ParseAdapterOutput(adapterOutput)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 8 || lines != 18 || sum.Tests != 18 || sum.Pass != 17 || sum.Skipped != 1 {
		t.Fatalf("results %d lines %d summary %+v", len(results), lines, sum)
	}
	if results[7].Action != TestSkip || results[4].Test != "rejects oversized, truncated, and malformed frames" || results[4].Action != TestPass {
		t.Fatalf("results = %+v", results)
	}
	if _, _, _, err := ParseAdapterOutput(strings.Split(adapterOutput, "ℹ tests")[0]); err == nil {
		t.Error("an output without counters (a cut run) was parsed")
	}
}

func TestAdapterReporterSamples(t *testing.T) {
	// Captured offline in adapter/ on the same source, before adding the runner regression test.
	for _, sample := range []struct {
		file, node, reporter string
	}{
		{"node-22.19.0-tap.txt", "v22.19.0", "tap"},
		{"node-24.15.0-spec.txt", "v24.15.0", "spec"},
	} {
		t.Run(sample.file, func(t *testing.T) {
			output, err := os.ReadFile("testdata/adapter-reporter/" + sample.file)
			if err != nil {
				t.Fatal(err)
			}
			r := goodAdapter(t)
			r.Node.Version, r.TestReporter = sample.node, sample.reporter
			remakeAdapterOutput(&r, string(output))
			err = Validate(KindAdapterTests, encode(t, KindAdapterTests, r), adapterExpect())
			if sample.reporter == "spec" {
				if err != nil {
					t.Fatalf("spec output refused: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), `no "tests" counter`) {
				t.Fatalf("unselected TAP output must be refused, got %v", err)
			}
		})
	}
}

func TestAdapterRefusesEveryMissingCounter(t *testing.T) {
	for _, counter := range []string{"tests", "suites", "pass", "fail", "cancelled", "skipped", "todo"} {
		t.Run(counter, func(t *testing.T) {
			r := goodAdapter(t)
			for _, line := range strings.Split(adapterOutput, "\n") {
				if strings.HasPrefix(line, "ℹ "+counter+" ") {
					remakeAdapterOutput(&r, strings.Replace(adapterOutput, line+"\n", "", 1))
					break
				}
			}
			if err := Validate(KindAdapterTests, encode(t, KindAdapterTests, r), adapterExpect()); err == nil || !strings.Contains(err.Error(), `no "`+counter+`" counter`) {
				t.Fatalf("missing %s counter: %v", counter, err)
			}
		})
	}
}

func TestAdapterRequiredValues(t *testing.T) {
	failed := strings.Replace(strings.Replace(adapterOutput, "✔ Go-compatible frame golden vector encodes and decodes", "✖ Go-compatible frame golden vector encodes and decodes", 1), "ℹ fail 0", "ℹ fail 1", 1)
	for name, mutate := range map[string]func(*AdapterRun){
		"another tree":           func(r *AdapterRun) { r.CommitTree = strings.Repeat("e", 40) },
		"no adapter tree":        func(r *AdapterRun) { r.AdapterTree = "" },
		"no adapter files":       func(r *AdapterRun) { r.AdapterFiles = 0 },
		"no lockfile digest":     func(r *AdapterRun) { r.PackageLockSHA256 = "" },
		"no package digest":      func(r *AdapterRun) { r.PackageJSONSHA256 = "" },
		"node too old":           func(r *AdapterRun) { r.Node.Version = "v22.18.9" },
		"node not a release":     func(r *AdapterRun) { r.Node.Version = "v24.15" },
		"no node digest":         func(r *AdapterRun) { r.Node.SHA256 = "" },
		"no platform":            func(r *AdapterRun) { r.Node.Platform = "" },
		"no node exec path":      func(r *AdapterRun) { r.Node.ExecPath = "" },
		"relative exec path":     func(r *AdapterRun) { r.Node.ExecPath = "node" },
		"no npm cli digest":      func(r *AdapterRun) { r.Npm.CLISHA256 = "" },
		"no npm version":         func(r *AdapterRun) { r.Npm.Version = "" },
		"online install":         func(r *AdapterRun) { r.Npm.InstallArgv = []string{"npm", "ci"} },
		"install with scripts":   func(r *AdapterRun) { r.Npm.InstallArgv = []string{"npm", "ci", "--offline"} },
		"failed install":         func(r *AdapterRun) { r.Npm.InstallExit = 1 },
		"no node_modules digest": func(r *AdapterRun) { r.Npm.NodeModulesDigest = "" },
		"empty node_modules":     func(r *AdapterRun) { r.Npm.NodeModulesFiles = 0 },
		"typecheck only":         func(r *AdapterRun) { r.Argv = []string{"npm", "run", "typecheck"} },
		"filtered tests":         func(r *AdapterRun) { r.Argv = append(r.Argv, "--", "--test-name-pattern=x") },
		"non-zero exit":          func(r *AdapterRun) { r.ExitCode = 1 },
		"missing reporter":       func(r *AdapterRun) { r.TestReporter = "" },
		"different reporter":     func(r *AdapterRun) { r.TestReporter = "tap" },
		"NODE_OPTIONS":           func(r *AdapterRun) { r.Env["NODE_OPTIONS"] = "--test-only" },
		"malformed env name":     func(r *AdapterRun) { r.Env["A=B"] = "x" },
		"output digest mismatch": func(r *AdapterRun) { r.OutputSHA256 = digestOf("another output") },
		"no output":              func(r *AdapterRun) { r.Output = "" },
		"stated results differ":  func(r *AdapterRun) { r.Results[0].Test = "invented" },
		"a result was dropped":   func(r *AdapterRun) { r.Results = r.Results[1:] },
		"a result is repeated":   func(r *AdapterRun) { r.Results = append(r.Results, r.Results[0]) },
		"a failure":              func(r *AdapterRun) { remakeAdapterOutput(r, failed) },
		"failure counter only": func(r *AdapterRun) {
			remakeAdapterOutput(r, strings.Replace(adapterOutput, "ℹ fail 0", "ℹ fail 1", 1))
		},
		"cancelled run": func(r *AdapterRun) {
			remakeAdapterOutput(r, strings.Replace(adapterOutput, "ℹ cancelled 0", "ℹ cancelled 1", 1))
		},
		"cut run": func(r *AdapterRun) { remakeAdapterOutput(r, strings.Split(adapterOutput, "ℹ tests")[0]) },
		"counters disagree": func(r *AdapterRun) {
			remakeAdapterOutput(r, strings.Replace(adapterOutput, "ℹ tests 18", "ℹ tests 19", 1))
		},
		"a skipped required test": func(r *AdapterRun) {
			remakeAdapterOutput(r, strings.Replace(adapterOutput, "✔ proxy tools", "﹣ proxy tools", 1))
		},
		"proxy tools test missing": func(r *AdapterRun) {
			remakeAdapterOutput(r, strings.Replace(adapterOutput, "✔ proxy tools each round-trip one proposal and return its correlated result (4.6987ms)\n", "", 1))
		},
		"pi cli required but skipped": func(r *AdapterRun) { r.Env["TBOUND_REQUIRE_PI_CLI_INTEGRATION"] = "1" },
		"pi cli missing": func(r *AdapterRun) {
			remakeAdapterOutput(r, strings.Replace(adapterOutput, "﹣ official Pi CLI", "✔ unofficial Pi CLI", 1))
		},
	} {
		r := goodAdapter(t)
		mutate(&r)
		if err := Validate(KindAdapterTests, encode(t, KindAdapterTests, r), adapterExpect()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := Validate(KindAdapterTests, encode(t, KindAdapterTests, goodAdapter(t)), adapterExpect()); err != nil {
		t.Fatalf("good run refused: %v", err)
	}
	// A run that executed the Pi CLI test passes it with the environment that required it.
	cli := goodAdapter(t)
	cli.Env["TBOUND_REQUIRE_PI_CLI_INTEGRATION"] = "1"
	cli.Env["TBOUND_PI_CLI_BIN"] = "/opt/pi"
	passed := strings.NewReplacer("﹣ official Pi CLI", "✔ official Pi CLI", "ℹ pass 17", "ℹ pass 18", "ℹ skipped 1", "ℹ skipped 0").Replace(adapterOutput)
	remakeAdapterOutput(&cli, passed)
	if err := Validate(KindAdapterTests, encode(t, KindAdapterTests, cli), adapterExpect()); err != nil {
		t.Errorf("a run with the Pi CLI test passed was refused: %v", err)
	}
	// Without the tree id of the Go capture the run is bound to nothing.
	noTree := adapterExpect()
	noTree.CommitTree = ""
	if err := Validate(KindAdapterTests, encode(t, KindAdapterTests, goodAdapter(t)), noTree); err == nil {
		t.Error("accepted although the capture gave no tree id")
	}
}

func TestAdapterIsNotBoundToTheGuestBinary(t *testing.T) {
	e := adapterExpect()
	e.BinarySHA256 = ""
	if err := Validate(KindAdapterTests, encode(t, KindAdapterTests, goodAdapter(t)), e); err != nil {
		t.Fatalf("an adapter run needs no guest binary digest: %v", err)
	}
	if IsGuestKind(KindAdapterTests) || !IsGuestKind(KindCapability) || IsGuestKind("nonsense") {
		t.Error("IsGuestKind")
	}
}
