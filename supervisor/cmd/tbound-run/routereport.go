package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"slices"
	"sort"
	"strings"

	"tbound/supervisor/internal/guestevidence"
	"tbound/supervisor/internal/routematrix"
)

// routeMatrixSchema names the pinned E06 route matrix definition.
const routeMatrixSchema = "tbound-route-matrix/v1"

type routeEvidenceFile struct {
	Kind    string `json:"kind"`
	TestRef string `json:"test_ref"`
}

type routeCellFile struct {
	Route     string              `json:"route"`
	Class     string              `json:"class"`
	Treatment string              `json:"treatment"`
	Status    string              `json:"status"`
	Reason    string              `json:"reason"`
	Evidence  []routeEvidenceFile `json:"evidence"`
	// GuestArtifact is the guest artifact kind whose validated presence the cell needs for production.
	GuestArtifact string `json:"guest_artifact"`
}

// parseRouteMatrix reads the pinned route matrix definition strictly. It does not validate the
// matrix: routematrix.Coverage does that, so an invalid definition fails when the report is built.
func parseRouteMatrix(data []byte) (routematrix.Matrix, error) {
	var doc struct {
		Schema  string          `json:"schema"`
		Classes []string        `json:"classes"`
		Cells   []routeCellFile `json:"cells"`
	}
	if err := decodeStrict(data, &doc); err != nil {
		return routematrix.Matrix{}, fmt.Errorf("route matrix: %w", err)
	}
	if doc.Schema != routeMatrixSchema {
		return routematrix.Matrix{}, fmt.Errorf("route matrix: schema %q, want %q", doc.Schema, routeMatrixSchema)
	}
	var m routematrix.Matrix
	for _, c := range doc.Classes {
		m.Classes = append(m.Classes, routematrix.SensitiveClass(c))
	}
	for _, c := range doc.Cells {
		cell := routematrix.Cell{Route: routematrix.Route(c.Route), Class: routematrix.SensitiveClass(c.Class),
			Treatment: routematrix.Treatment(c.Treatment), Status: routematrix.Status(c.Status), Reason: c.Reason,
			GuestArtifact: c.GuestArtifact}
		for _, e := range c.Evidence {
			cell.Evidence = append(cell.Evidence, routematrix.Evidence{Kind: routematrix.EvidenceKind(e.Kind), TestRef: e.TestRef})
		}
		m.Cells = append(m.Cells, cell)
	}
	return m, nil
}

// refPassed resolves a routematrix test reference "<package import path>.<TestName>" against the
// test results: the test must have passed.
func (o *testOutcomes) refPassed(ref string) bool {
	i := strings.LastIndex(ref, ".Test")
	return i > 0 && o.passed(ref[:i], ref[i+1:])
}

// buildRouteReport turns the pinned matrix and the test results into the routematrix.Report that
// run.json names as route_report. A cell the definition declares Covered stays Covered only if every
// test reference of it passed; otherwise it is reported Missing. The lookup of routematrix.Coverage is
// "the test passed", so a reference that failed, was skipped or is absent keeps the report incomplete.
func buildRouteReport(m routematrix.Matrix, o *testOutcomes) (routematrix.Report, error) {
	return buildRouteReportWithGuest(m, o, guestRouteArtifacts{})
}

// guestRouteArtifacts is the guest evidence a route report is given: Digests maps an artifact kind to
// the sha256 of its file once the file passed the kind's validator, Errors maps a kind to why a
// supplied file did not.
type guestRouteArtifacts struct {
	Digests, Errors map[string]string
}

// buildRouteReportWithGuest is buildRouteReport for a matrix whose production cells need guest
// artifacts. A cell that names a guest artifact is Covered only if every test of it passed (the
// prototype evidence) and its artifact is present and valid; otherwise it is Pending_Guest, with the
// reason, or Missing if a test did not pass. A cell declared Missing stays Missing.
func buildRouteReportWithGuest(m routematrix.Matrix, o *testOutcomes, guest guestRouteArtifacts) (routematrix.Report, error) {
	cells := make([]routematrix.Cell, len(m.Cells))
	copy(cells, m.Cells)
	var pending []string
	for i, c := range cells {
		if c.Status == routematrix.StatusMissing {
			continue
		}
		if c.Status == routematrix.StatusPendingGuest && c.GuestArtifact == "" {
			continue
		}
		testsPassed := true
		for _, e := range c.Evidence {
			if !o.refPassed(e.TestRef) {
				cells[i].Status = routematrix.StatusMissing
				cells[i].Reason = strings.TrimSpace(c.Reason + " test " + e.TestRef + " did not pass in the supplied test JSON.")
				testsPassed = false
				break
			}
		}
		if !testsPassed || c.GuestArtifact == "" {
			continue
		}
		switch digest := guest.Digests[c.GuestArtifact]; {
		case digest != "":
			cells[i].Status = routematrix.StatusCovered
			cells[i].Reason = strings.TrimSpace(c.Reason + " Guest artifact " + c.GuestArtifact + " " + digest + ".")
		default:
			cells[i].Status = routematrix.StatusPendingGuest
			why := "guest artifact " + c.GuestArtifact + " not supplied"
			if e := guest.Errors[c.GuestArtifact]; e != "" {
				why = "guest artifact " + c.GuestArtifact + " refused: " + e
			}
			cells[i].Reason = strings.TrimSpace(c.Reason + " " + why + ".")
			pending = append(pending, fmt.Sprintf("route %q class %q: %s", c.Route, c.Class, why))
		}
	}
	rep, err := routematrix.Coverage(routematrix.Matrix{Classes: m.Classes, Cells: cells}, o.refPassed)
	if err != nil {
		return routematrix.Report{}, err
	}
	rep.Reasons = append(rep.Reasons, pending...)
	sort.Strings(rep.Reasons)
	if rep.Reasons == nil {
		rep.Reasons = []string{}
	}
	return rep, nil
}

// routeBinding is what the report carries about its origin.
func routeBinding(commit, captureDigest, matrixDigest string) string {
	return "commit=" + commit + ";evq-capture=" + captureDigest + ";route-matrix=" + matrixDigest
}

// routeBoundTo reports whether a route report names the frozen commit.
func routeBoundTo(binding, freezeCommit string) bool {
	return slices.Contains(strings.Split(binding, ";"), "commit="+freezeCommit)
}

// routeReportCmd is `tbound-run route-report`. The report is derived from the pinned route matrix
// definition and the capture of the frozen commit only; it is never authored by hand.
func (a *app) routeReportCmd(args []string) error {
	flags := flag.NewFlagSet("tbound-run route-report", flag.ContinueOnError)
	flags.SetOutput(a.errOut)
	matrix := flags.String("matrix", "", "route matrix definition (infra/conformance/route-matrix.json)")
	config := flags.String("config", "", "run.json: its freeze_commit binds the evidence")
	capture := flags.String("capture", "", "capture directory made by `tbound-run evq-capture` for the freeze commit")
	out := flags.String("out", "", "route_report.json to create")
	guestBinary := flags.String("guest-binary-sha256", "", "sha256:<hex> of the tbound-profile binary built from the freeze commit (P1); required with --artifact")
	var artifacts stringList
	flags.Var(&artifacts, "artifact", "<kind>=<file>: a guest artifact named by a cell of the matrix (repeatable)")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if flags.NArg() != 0 || *matrix == "" || *config == "" || *capture == "" || *out == "" {
		return fmt.Errorf("%w: route-report takes --matrix, --config, --capture, --out and optional --guest-binary-sha256 with --artifact kind=file", errUsage)
	}
	if len(artifacts) > 0 && !guestevidence.IsDigest(*guestBinary) {
		return fmt.Errorf("%w: --artifact needs --guest-binary-sha256 sha256:<hex>", errUsage)
	}
	commit, profileHash, frozenSource, err := loadFreezeBinding(*config)
	if err != nil {
		return err
	}
	mdata, err := readRegularFile(*matrix, maxConfigBytes)
	if err != nil {
		return fmt.Errorf("route matrix: %w", err)
	}
	m, err := parseRouteMatrix(mdata)
	if err != nil {
		return err
	}
	rec, recordDigest, o, err := loadEVQCapture(*capture, commit)
	if err != nil {
		return err
	}
	if err := rec.requirePinnedInput(evqPinnedInputs[1], mdata); err != nil {
		return fmt.Errorf("route matrix: %w", err)
	}
	guest, err := loadGuestRouteArtifacts(m, artifacts, guestevidence.Expect{Commit: commit, BinarySHA256: *guestBinary,
		ProfileHash: profileHash, FrozenSourceDigest: frozenSource})
	if err != nil {
		return err
	}
	rep, err := buildRouteReportWithGuest(m, o, guest)
	if err != nil {
		return fmt.Errorf("route matrix: %w", err)
	}
	rep.Binding = routeBinding(commit, recordDigest, routematrix.Digest(m))
	for _, kind := range slices.Sorted(func(yield func(string) bool) {
		for k := range guest.Digests {
			if !yield(k) {
				return
			}
		}
	}) {
		rep.Binding += ";artifact:" + kind + "=" + guest.Digests[kind]
	}
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := parseRouteReport(data, commit); err != nil {
		return fmt.Errorf("generated report is not accepted by the runner: %w", err)
	}
	if err := writeNewFile(*out, data); err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	fmt.Fprintf(a.out, "commit %s\nevq_capture %s\nmatrix_digest %s\ntest_json %s\n", commit, recordDigest, routematrix.Digest(m), rec.TestJSONSHA256)
	fmt.Fprintf(a.out, "cells %d\ncovered %d\npending_guest %d\nmissing %d\n", rep.Total,
		rep.ByStatus[routematrix.StatusCovered], rep.ByStatus[routematrix.StatusPendingGuest], rep.ByStatus[routematrix.StatusMissing])
	fmt.Fprintf(a.out, "complete %t\n", rep.Complete)
	for _, r := range rep.Reasons {
		fmt.Fprintf(a.out, "  %s\n", r)
	}
	fmt.Fprintf(a.out, "route_report_sha256 sha256:%s\n", hex.EncodeToString(sum[:]))
	return nil
}

// loadGuestRouteArtifacts validates the supplied guest artifacts against what the run freezes. An
// artifact whose kind no cell of the matrix names, a repeated kind and a malformed argument are usage
// errors; a file that fails its validator is not: its cell stays Pending_Guest and the report says why.
func loadGuestRouteArtifacts(m routematrix.Matrix, specs []string, expect guestevidence.Expect) (guestRouteArtifacts, error) {
	named := map[string]bool{}
	for _, c := range m.Cells {
		if c.GuestArtifact != "" {
			named[c.GuestArtifact] = true
		}
	}
	out := guestRouteArtifacts{Digests: map[string]string{}, Errors: map[string]string{}}
	seen := map[string]bool{}
	for _, spec := range specs {
		kind, path, ok := strings.Cut(spec, "=")
		if !ok || kind == "" || path == "" {
			return out, fmt.Errorf("%w: --artifact %q is not <kind>=<file>", errUsage, spec)
		}
		if !named[kind] || !slices.Contains(guestevidence.Kinds(), kind) {
			return out, fmt.Errorf("%w: --artifact kind %q is not named by a cell of the matrix", errUsage, kind)
		}
		if seen[kind] {
			return out, fmt.Errorf("%w: --artifact kind %q is given twice", errUsage, kind)
		}
		seen[kind] = true
		data, err := readRegularFile(path, guestevidence.MaxBytes)
		if err != nil {
			out.Errors[kind] = err.Error()
			continue
		}
		if err := guestevidence.Validate(kind, data, expect); err != nil {
			out.Errors[kind] = err.Error()
			continue
		}
		sum := sha256.Sum256(data)
		out.Digests[kind] = "sha256:" + hex.EncodeToString(sum[:])
	}
	return out, nil
}
