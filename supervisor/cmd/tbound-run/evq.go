package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/runner"
)

// evqMappingSchema names the pinned mapping from EVQ obligations to the evidence that establishes them.
const evqMappingSchema = "tbound-evq-mapping/v1"

var (
	evqIDRE        = regexp.MustCompile(`^EVQ-[A-Z0-9]+(-[A-Z0-9]+)*$`)
	packagePathRE  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)*$`)
	testNameRE     = regexp.MustCompile(`^Test[A-Z0-9_][A-Za-z0-9_]*$`)
	artifactKindRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

// evqObligation is one EVQ obligation and the evidence that establishes it: named top-level Go tests
// per package (all must pass, none skipped) and guest artifact kinds (all must be supplied). An
// obligation with neither is unmapped and is never passed.
type evqObligation struct {
	ID        string              `json:"id"`
	Title     string              `json:"title"`
	Tests     map[string][]string `json:"tests"`
	Artifacts []string            `json:"artifacts"`
}

func (o evqObligation) unmapped() bool { return len(o.Tests) == 0 && len(o.Artifacts) == 0 }

// evqMapping is the parsed mapping file. Digest is the sha256 of its bytes.
type evqMapping struct {
	Obligations []evqObligation
	Digest      string
}

// decodeStrict decodes one JSON document into v: a duplicate key, an unknown member and trailing data
// are refused.
func decodeStrict(data []byte, v any) error {
	if err := protocol.ValidateStrictJSON(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data")
	}
	return nil
}

// parseEVQMapping reads the mapping strictly.
func parseEVQMapping(data []byte) (evqMapping, error) {
	var doc struct {
		Schema      string          `json:"schema"`
		Obligations []evqObligation `json:"obligations"`
	}
	if err := decodeStrict(data, &doc); err != nil {
		return evqMapping{}, fmt.Errorf("EVQ mapping: %w", err)
	}
	if doc.Schema != evqMappingSchema {
		return evqMapping{}, fmt.Errorf("EVQ mapping: schema %q, want %q", doc.Schema, evqMappingSchema)
	}
	if len(doc.Obligations) == 0 {
		return evqMapping{}, errors.New("EVQ mapping: no obligations")
	}
	seen := map[string]bool{}
	for _, o := range doc.Obligations {
		if !evqIDRE.MatchString(o.ID) || seen[o.ID] {
			return evqMapping{}, fmt.Errorf("EVQ mapping: obligation id %q is malformed or repeated", o.ID)
		}
		seen[o.ID] = true
		if strings.TrimSpace(o.Title) == "" {
			return evqMapping{}, fmt.Errorf("EVQ mapping: %s has no title", o.ID)
		}
		for pkg, names := range o.Tests {
			if !packagePathRE.MatchString(pkg) {
				return evqMapping{}, fmt.Errorf("EVQ mapping: %s: package %q is not an import path", o.ID, pkg)
			}
			if len(names) == 0 {
				return evqMapping{}, fmt.Errorf("EVQ mapping: %s: package %q lists no tests", o.ID, pkg)
			}
			for i, n := range names {
				if !testNameRE.MatchString(n) || slices.Contains(names[:i], n) {
					return evqMapping{}, fmt.Errorf("EVQ mapping: %s: %q is not a top-level test name or is repeated", o.ID, n)
				}
			}
		}
		for i, k := range o.Artifacts {
			if !artifactKindRE.MatchString(k) || slices.Contains(o.Artifacts[:i], k) {
				return evqMapping{}, fmt.Errorf("EVQ mapping: %s: artifact kind %q is malformed or repeated", o.ID, k)
			}
		}
	}
	sum := sha256.Sum256(data)
	return evqMapping{Obligations: doc.Obligations, Digest: "sha256:" + hex.EncodeToString(sum[:])}, nil
}

// artifactKinds lists the artifact kinds the mapping names, sorted.
func (m evqMapping) artifactKinds() []string {
	set := map[string]bool{}
	for _, o := range m.Obligations {
		for _, k := range o.Artifacts {
			set[k] = true
		}
	}
	return slices.Sorted(func(yield func(string) bool) {
		for k := range set {
			if !yield(k) {
				return
			}
		}
	})
}

// evqInputs is the evidence an EVQ run is given: the parsed test results, the sha256 of each test JSON
// file, and the sha256 of each artifact by kind.
type evqInputs struct {
	Outcomes  *testOutcomes
	TestJSON  []string
	Artifacts map[string]string
}

// evqStatus is the outcome of one obligation with the reasons it is not passed.
type evqStatus struct {
	ID       string
	Passed   bool
	Unmapped bool
	Reasons  []string
}

// buildEVQResults decides every obligation of the mapping, in mapping order. An obligation is passed
// only if it is mapped, every mapped test passed and every mapped artifact is present. A passed
// obligation's evidence_ref binds the mapping, the test JSON files and the artifacts it used by sha256.
func buildEVQResults(m evqMapping, in evqInputs) (runner.EVQResults, []evqStatus) {
	results := make(runner.EVQResults, 0, len(m.Obligations))
	statuses := make([]evqStatus, 0, len(m.Obligations))
	for _, o := range m.Obligations {
		st := evqStatus{ID: o.ID, Unmapped: o.unmapped()}
		if st.Unmapped {
			st.Reasons = append(st.Reasons, "no evidence is mapped yet")
		}
		for _, pkg := range slices.Sorted(func(yield func(string) bool) {
			for p := range o.Tests {
				if !yield(p) {
					return
				}
			}
		}) {
			for _, name := range slices.Sorted(slices.Values(o.Tests[pkg])) {
				if why := in.Outcomes.state(pkg, name); why != "" {
					st.Reasons = append(st.Reasons, fmt.Sprintf("test %s.%s: %s", pkg, name, why))
				}
			}
		}
		for _, kind := range slices.Sorted(slices.Values(o.Artifacts)) {
			if in.Artifacts[kind] == "" {
				st.Reasons = append(st.Reasons, fmt.Sprintf("artifact %s: not supplied", kind))
			}
		}
		st.Passed = len(st.Reasons) == 0
		res := runner.EVQResult{ID: o.ID, Passed: st.Passed}
		if st.Passed {
			parts := []string{"evq-mapping=" + m.Digest}
			if len(o.Tests) > 0 {
				parts = append(parts, "go-test-json="+strings.Join(in.TestJSON, ","))
			}
			for _, kind := range slices.Sorted(slices.Values(o.Artifacts)) {
				parts = append(parts, "artifact:"+kind+"="+in.Artifacts[kind])
			}
			res.EvidenceRef = strings.Join(parts, ";")
		}
		results = append(results, res)
		statuses = append(statuses, st)
	}
	return results, statuses
}

// evqCmd is `tbound-run evq`. The results are established once for a frozen configuration, from tests
// run on the frozen commit and artifacts collected on the guest, and the file is pinned in run.json by
// its sha256. The command never overwrites a results file.
func (a *app) evqCmd(args []string) error {
	flags := flag.NewFlagSet("tbound-run evq", flag.ContinueOnError)
	flags.SetOutput(a.errOut)
	mapping := flags.String("mapping", "", "EVQ mapping file (infra/conformance/evq-mapping.json)")
	out := flags.String("out", "", "evq_results.json to create")
	var testJSON, artifacts stringList
	flags.Var(&testJSON, "test-json", "`go test -json` output of the frozen commit (repeatable)")
	flags.Var(&artifacts, "artifact", "<kind>=<file>: a guest artifact named by the mapping (repeatable)")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if flags.NArg() != 0 || *mapping == "" || *out == "" || len(testJSON) == 0 {
		return fmt.Errorf("%w: evq takes --mapping, --test-json (one or more), --out and optional --artifact kind=file", errUsage)
	}
	mdata, err := readRegularFile(*mapping, maxConfigBytes)
	if err != nil {
		return fmt.Errorf("EVQ mapping: %w", err)
	}
	m, err := parseEVQMapping(mdata)
	if err != nil {
		return err
	}
	in := evqInputs{Outcomes: newTestOutcomes(), Artifacts: map[string]string{}}
	for _, p := range testJSON {
		digest, err := parseTestJSONFile(p, in.Outcomes)
		if err != nil {
			return err
		}
		in.TestJSON = append(in.TestJSON, digest)
	}
	known := m.artifactKinds()
	for _, spec := range artifacts {
		kind, path, ok := strings.Cut(spec, "=")
		if !ok || kind == "" || path == "" {
			return fmt.Errorf("%w: --artifact %q is not <kind>=<file>", errUsage, spec)
		}
		if !slices.Contains(known, kind) {
			return fmt.Errorf("%w: --artifact kind %q is not named by the mapping (known: %s)", errUsage, kind, strings.Join(known, ", "))
		}
		if _, dup := in.Artifacts[kind]; dup {
			return fmt.Errorf("%w: --artifact kind %q is given twice", errUsage, kind)
		}
		digest, err := digestNonEmptyFile(path)
		if err != nil {
			return fmt.Errorf("artifact %s: %w", kind, err)
		}
		in.Artifacts[kind] = digest
	}
	results, statuses := buildEVQResults(m, in)
	data, err := json.MarshalIndent(struct {
		Obligations runner.EVQResults `json:"obligations"`
	}{results}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := runner.ParseEVQResults(data); err != nil {
		return fmt.Errorf("generated results are not accepted by the runner: %w", err)
	}
	if err := writeNewFile(*out, data); err != nil {
		return err
	}
	return a.printEVQReport(m, statuses, data)
}

func (a *app) printEVQReport(m evqMapping, statuses []evqStatus, data []byte) error {
	var passed, unmapped int
	ids := make([]string, 0, len(statuses))
	for _, s := range statuses {
		ids = append(ids, s.ID)
		switch {
		case s.Passed:
			passed++
			fmt.Fprintf(a.out, "%s passed\n", s.ID)
		case s.Unmapped:
			unmapped++
			fmt.Fprintf(a.out, "%s not_passed unmapped\n", s.ID)
		default:
			fmt.Fprintf(a.out, "%s not_passed\n", s.ID)
			for _, r := range s.Reasons {
				fmt.Fprintf(a.out, "  %s\n", r)
			}
		}
	}
	idList, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	fmt.Fprintf(a.out, "passed %d\nnot_passed %d\nunmapped %d\n", passed, len(statuses)-passed, unmapped)
	fmt.Fprintf(a.out, "mapping_sha256 %s\nresults_sha256 sha256:%s\nevq_obligations %s\n", m.Digest, hex.EncodeToString(sum[:]), idList)
	return nil
}
