//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/guestevidence"
	"tbound/supervisor/internal/taskaccept"
)

func loadConstructManifest(path string) (evalcorpus.Manifest, error) {
	f, err := openRegularFile(path)
	if err != nil {
		return evalcorpus.Manifest{}, err
	}
	defer f.Close()
	return evalcorpus.Load(f)
}

func runCorpusCheck(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound-profile corpus-check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifest := flags.String("manifest", "", "corpus manifest; sources.json, licenses/, notices and artifacts/ must be beside it")
	profile := flags.String("quota-profile", "thesis", "registered quotas: thesis or pilot")
	other := flags.String("other-manifest", "", "optional manifest whose roots must be disjoint")
	node := flags.String("node", "", "local node binary, as in corpus-digest; runs trusted corpus fixtures/references only")
	out := flags.String("out", "", "new directory for report.json and report.md (existing directory refused)")
	evidence := flags.String("guest-evidence", "", "directory holding construct-map.json and the retained producer files it names (docs/guides/construct-witnesses.md)")
	runConfig := flags.String("run-config", "", "frozen run.json supplying expected guest commit and artifact pins (with --guest-evidence)")
	binaryDigest := flags.String("guest-binary-sha256", "", "expected tbound-profile digest from the frozen build (with --guest-evidence)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *manifest == "" || *out == "" {
		return errors.New("--manifest and --out are required; no positional arguments")
	}
	if *evidence == "" && (*runConfig != "" || *binaryDigest != "") {
		return errors.New("--run-config and --guest-binary-sha256 require --guest-evidence")
	}
	quotas, err := quotasFor(*profile)
	if err != nil {
		return err
	}
	if err := os.Mkdir(*out, 0o700); err != nil {
		return err
	}
	failure := func(name string, err error) error {
		return writeConstructReport(*out, corpusadmit.ConstructReport{Schema: "tbound-construct-checks/v1", Summary: map[string]corpusadmit.ConstructCounts{name: {Fail: 1}}, Checks: []corpusadmit.ConstructCheck{{Name: name, Status: "fail", Reason: err.Error()}}}, stdout)
	}
	m, err := loadConstructManifest(*manifest)
	if err != nil {
		return failure("manifest-load", err)
	}
	var peer *evalcorpus.Manifest
	if *other != "" {
		p, err := loadConstructManifest(*other)
		if err != nil {
			return failure("other-manifest-load", err)
		}
		peer = &p
	}
	var runner taskaccept.NodeRunner
	if *node != "" {
		runner = corpusadmit.LocalNodeRunner{Path: *node}
	}
	report := corpusadmit.CheckConstructs(m, os.DirFS(filepath.Dir(*manifest)), quotas, peer, runner)
	if *evidence != "" {
		expect := constructExpect{BinarySHA256: *binaryDigest}
		var inputErr error
		if *runConfig == "" || !guestevidence.IsDigest(*binaryDigest) {
			inputErr = errors.New("--run-config and --guest-binary-sha256 (sha256:<64 hex>) are required with --guest-evidence")
		} else {
			var raw []byte
			raw, inputErr = readConstructConfig(*runConfig)
			if inputErr == nil {
				expect, inputErr = parseConstructExpect(raw, *binaryDigest)
			}
		}
		root, rootErr := os.OpenRoot(*evidence)
		if rootErr != nil && inputErr == nil {
			inputErr = rootErr
		}
		var set fs.FS
		if root != nil {
			defer root.Close()
			set = constructFS{root}
		}
		artifacts := filepath.Join(filepath.Dir(*manifest), "artifacts")
		expect.ConfigPath = *runConfig
		applyConstructEvidence(&report, constructInput{Manifest: m, Quotas: quotas, Set: set, Artifacts: os.DirFS(artifacts), ArtifactsDir: artifacts, Expect: expect, Err: inputErr})
	}
	return writeConstructReport(*out, report, stdout)
}

func writeConstructReport(out string, report corpusadmit.ConstructReport, stdout io.Writer) error {
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "report.json"), append(raw, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "report.md"), []byte(constructMarkdown(report)), 0o600); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "%s\n%s\n", filepath.Join(out, "report.json"), filepath.Join(out, "report.md")); err != nil {
		return err
	}
	if !report.Passed {
		return errors.New("deterministic construct checks failed; see retained report")
	}
	return nil
}

func constructMarkdown(r corpusadmit.ConstructReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Deterministic construct checks\n\nManifest: `%s`\n\nLocal checks passed: **%t**. The AI panel may run once they pass; guest-only checks must pass before the corpus freeze.\n\nHuman/AI review rules are outside step 1; %d existing review violations retained in JSON.\n\n", r.ManifestDigest, r.Passed, len(r.DeferredReview))
	if constructAnchored(r) {
		fmt.Fprintf(&b, "Imported guest results are recomputed from original producer files the operator kept (%s); each row's provenance in report.json lists their sha256.\n\n", corpusadmit.ConstructAnchor)
	}
	b.WriteString("| Check | pass | fail | guest-only | not-checked |\n|---|---:|---:|---:|---:|\n")
	names := make([]string, 0, len(r.Summary))
	for name := range r.Summary {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		n := r.Summary[name]
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d |\n", name, n.Pass, n.Fail, n.GuestOnly, n.NotChecked)
	}
	b.WriteString("\nFailures and license conditions (all case results and reasons are in JSON):\n\n")
	conditions := map[string]int{}
	line := func(id string, c corpusadmit.ConstructCheck) {
		if c.Status == "fail" {
			fmt.Fprintf(&b, "- %s / %s: %s\n", id, c.Name, strings.ReplaceAll(c.Reason, "\n", " "))
		}
		for _, condition := range c.Conditions {
			conditions[condition]++
		}
	}
	for _, check := range r.Checks {
		line("corpus", check)
	}
	for _, c := range r.Cases {
		for _, check := range c.Checks {
			line(c.CaseID, check)
		}
	}
	var conditionNames []string
	for condition := range conditions {
		conditionNames = append(conditionNames, condition)
	}
	sort.Strings(conditionNames)
	for _, condition := range conditionNames {
		fmt.Fprintf(&b, "- License condition (%d cases): %s\n", conditions[condition], strings.ReplaceAll(condition, "\n", " "))
	}
	return b.String()
}

func constructAnchored(r corpusadmit.ConstructReport) bool {
	for _, c := range r.Cases {
		for _, check := range c.Checks {
			if check.Anchor != "" {
				return true
			}
		}
	}
	return false
}
