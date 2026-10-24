package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/cellview"
	"tbound/supervisor/internal/guestevidence"
	"tbound/supervisor/internal/routematrix"
)

// The bash-view and direct-runtime cells of the pinned matrix are evidenced for production only by a
// guest artifact. The prototype tests stay as evidence, but the cell is Covered only when its artifact
// has been supplied and has passed its validator.

func guestRouteDoc() []byte {
	cell := func(route, status, reason, artifact string, evs ...string) string {
		extra := ""
		if artifact != "" {
			extra = `,"guest_artifact":"` + artifact + `"`
		}
		c := routeCell(route, "c1", "unreachable", status, reason, evs...)
		return strings.TrimSuffix(c, "}") + extra + "}"
	}
	return routeDoc(`"c1"`,
		cell("declared-read", "Covered", "", "", "AttemptedRead", refDR1, "SuccessfulRead", refDR2),
		cell("bash-view", "Pending_Guest", "prototype sandbox only", guestevidence.KindRouteBashView, "AttemptedRead", refBV1, "SuccessfulRead", refBV2),
		cell("direct-runtime", "Pending_Guest", "prototype sandbox only", guestevidence.KindRouteDirectRuntime, "AttemptedRead", refRT1, "SuccessfulRead", refRT2),
		routeCell("provider-broker", "c1", "proposal-denied", "Covered", "", "ProviderChannelDisclosure", refPB))
}

func TestParseRouteMatrixReadsGuestArtifacts(t *testing.T) {
	m, err := parseRouteMatrix(guestRouteDoc())
	if err != nil {
		t.Fatal(err)
	}
	got := map[routematrix.Route]string{}
	for _, c := range m.Cells {
		got[c.Route] = c.GuestArtifact
	}
	if got[routematrix.RouteBashView] != guestevidence.KindRouteBashView || got[routematrix.RouteDirectRuntime] != guestevidence.KindRouteDirectRuntime ||
		got[routematrix.RouteDeclaredRead] != "" {
		t.Fatalf("guest artifacts = %v", got)
	}
}

func TestRouteReportGuestCellsStayPendingWithoutTheirArtifacts(t *testing.T) {
	m, err := parseRouteMatrix(guestRouteDoc())
	if err != nil {
		t.Fatal(err)
	}
	rep, err := buildRouteReport(m, outcomesOf(t, passAll(allRouteRefs...)))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Complete || rep.ByStatus[routematrix.StatusCovered] != 2 || rep.ByStatus[routematrix.StatusPendingGuest] != 2 {
		t.Fatalf("report = %+v", rep)
	}
	reasons := strings.Join(rep.Reasons, "\n")
	for _, kind := range []string{guestevidence.KindRouteBashView, guestevidence.KindRouteDirectRuntime} {
		if !strings.Contains(reasons, kind) {
			t.Errorf("reasons do not name the artifact %s: %v", kind, rep.Reasons)
		}
	}
}

func TestRouteReportGuestCellsAreCoveredByTheirArtifactsAndTheirTests(t *testing.T) {
	m, err := parseRouteMatrix(guestRouteDoc())
	if err != nil {
		t.Fatal(err)
	}
	art := guestRouteArtifacts{Digests: map[string]string{
		guestevidence.KindRouteBashView:      "sha256:" + strings.Repeat("1", 64),
		guestevidence.KindRouteDirectRuntime: "sha256:" + strings.Repeat("2", 64),
	}}
	rep, err := buildRouteReportWithGuest(m, outcomesOf(t, passAll(allRouteRefs...)), art)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Complete || rep.ByStatus[routematrix.StatusCovered] != 4 {
		t.Fatalf("report = %+v", rep)
	}
	// One artifact alone covers only its own cell.
	one := guestRouteArtifacts{Digests: map[string]string{guestevidence.KindRouteBashView: art.Digests[guestevidence.KindRouteBashView]}}
	rep, err = buildRouteReportWithGuest(m, outcomesOf(t, passAll(allRouteRefs...)), one)
	if err != nil || rep.Complete || rep.ByStatus[routematrix.StatusCovered] != 3 || rep.ByRoute[routematrix.RouteBashView][routematrix.StatusCovered] != 1 {
		t.Fatalf("bash view only: %+v %v", rep, err)
	}
	// A refused artifact says why and covers nothing.
	refused := guestRouteArtifacts{Errors: map[string]string{guestevidence.KindRouteBashView: "the control exited 1"}}
	rep, err = buildRouteReportWithGuest(m, outcomesOf(t, passAll(allRouteRefs...)), refused)
	if err != nil || rep.Complete || !strings.Contains(strings.Join(rep.Reasons, "\n"), "the control exited 1") {
		t.Fatalf("refused artifact: %+v %v", rep, err)
	}
	// The prototype tests still have to pass: an artifact does not replace them.
	rep, err = buildRouteReportWithGuest(m, outcomesOf(t, routeTests(allRouteRefs, map[string]string{refBV2: "fail"})), art)
	if err != nil || rep.Complete || rep.ByRoute[routematrix.RouteBashView][routematrix.StatusMissing] != 1 {
		t.Fatalf("failed prototype test: %+v %v", rep, err)
	}
}

// guestRouteEnv is a route-report run whose matrix has the two guest cells.
func guestRouteEnv(t *testing.T) *routeEnv {
	t.Helper()
	e := newRouteEnv(t, guestRouteDoc(), passingRoutePkgs(allRouteRefs...))
	pins := `{"freeze_commit":"` + commit + `","v_image_digest":"` + guestPins.image + `","pins":{"profile_hash":"` + sha256Of([]byte("pilot profile")) + `","containment_profile_digest":"` + sha256Of([]byte("pilot template")) + `","base_template_digest":"` + guestPins.base + `","frozen_source_digest":"` + guestPins.source + `","admin_pubkey_digest":"` + guestPins.admin + `","runtime_bundle_digest":"` + guestPins.bundle + `","runner_digest":"` + guestPins.runner + `"}}`
	if err := os.WriteFile(e.config, []byte(pins), 0o600); err != nil {
		t.Fatal(err)
	}
	return e
}

// guestPins are the pins the guest artifacts are bound to; profile is the run's own profile (never the pilot's).
var guestPins = struct{ profile, source, binary, admin, base, image, bundle, runner string }{
	"sha256:" + strings.Repeat("a", 64), fixtureDigest("tree"), "sha256:" + strings.Repeat("c", 64), "sha256:" + strings.Repeat("e", 64),
	"sha256:" + strings.Repeat("f", 64), "sha256:" + strings.Repeat("9", 64),
	"sha256:" + strings.Repeat("7", 64), "sha256:" + strings.Repeat("6", 64)}

// writeRouteArtifact writes a good artifact of kind, bound to the run's pins and the commit.
func writeRouteArtifact(t *testing.T, dir, kind string, mutate func(*guestevidence.RouteDenied)) string {
	t.Helper()
	cell := guestevidence.RouteCellCommand
	if kind == guestevidence.KindRouteDirectRuntime {
		cell = guestevidence.RouteCellPiRuntime
	}
	attempt := func(method, outcome string, exit int, seq uint64) guestevidence.RouteAttempt {
		return guestevidence.RouteAttempt{Method: method, Argv: []string{"/bin/bash", "-lc", "cat /home/node/.profile"}, ExitCode: exit, Outcome: outcome,
			ResultDigest: sha256Of([]byte("result " + method)), ReceiptDigest: sha256Of([]byte("receipt " + method)), ContainerID: "c-" + method, JournalSequence: seq}
	}
	d := guestevidence.RouteDenied{RunID: "run-1", ProfileHash: guestPins.profile, FrozenSourceDigest: guestPins.source, Target: "/home/node/.profile",
		Binding: guestevidence.ProfileBinding{ProfileDigest: guestPins.profile, TemplateDigest: sha256Of([]byte("t")), BaseTemplateDigest: guestPins.base,
			RegistrationDigest: sha256Of([]byte("registration")), ImageDigest: guestPins.image, SourceDigest: guestPins.source, AdminPubkeyDigest: guestPins.admin,
			RuntimeBundleDigest: guestPins.bundle, RunnerDigest: guestPins.runner},
		Cell:    guestevidence.RouteCell{Kind: cell, ImageDigest: guestPins.image, TemplateDigest: sha256Of([]byte("t")), LandlockRulesetDigest: sha256Of([]byte("l"))},
		Journal: guestevidence.RouteJournal{Records: 9, HeadHash: strings.Repeat("d", 64)},
		Control: attempt("control", "read", 0, 2),
		Attempts: []guestevidence.RouteAttempt{attempt("absolute", "denied", 1, 3), attempt("traversal", "denied", 1, 4),
			attempt("symlink", "denied", 1, 5)}}
	if kind == guestevidence.KindRouteDirectRuntime {
		// The Pi runtime cell artifact: one proof, open/stat/list by each name, the target shown from outside.
		var observed []guestevidence.RouteDirectObservation
		for _, method := range guestevidence.RouteMethods {
			for _, op := range guestevidence.RouteDirectOps {
				errno := 13
				if op == guestevidence.RouteOpStat {
					errno = 0
				}
				observed = append(observed, guestevidence.RouteDirectObservation{Method: method, Op: op, Errno: errno})
			}
		}
		var err error
		d.Control, d.Attempts, err = guestevidence.DirectRuntimeAttempts(d.Target, "/var/lib/tbound/pi-worker/src/release.txt", "tbound-d09-cell-1", sha256Of([]byte("proof")), 2, 0, observed)
		if err != nil {
			t.Fatal(err)
		}
		d.Cell.ProductionTemplateDigest = sha256Of([]byte("production template"))
		d.Cell.CellUID, d.Cell.CellGID = 100731, 100731
		d.Limits = guestevidence.DirectRuntimeLimits
		d.View = &cellview.View{EUID: 100731, EGID: 100731, OverflowRead: true, OverflowUID: 65534, OverflowGID: 65534, Facts: []cellview.Fact{
			{Path: "/", Kind: cellview.KindDir, Mode: 0o755}, {Path: "/home", Kind: cellview.KindDir, Mode: 0o755},
			{Path: "/home/node", Kind: cellview.KindDir, Mode: 0o755, UID: 1000, GID: 1000},
			{Path: "/home/node/.profile", Kind: cellview.KindFile, Mode: 0o644, UID: 1000, GID: 1000, Size: 100}}}
	}
	if mutate != nil {
		mutate(&d)
	}
	data, err := guestevidence.Encode(kind, commit, guestPins.binary, d)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, kind+".json")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRouteReportCommandCoversGuestCellsOnlyWithValidArtifacts(t *testing.T) {
	e := guestRouteEnv(t)
	status := func(out string) routematrix.Report {
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		var rep routematrix.Report
		if err := json.Unmarshal(data, &rep); err != nil {
			t.Fatal(err)
		}
		return rep
	}
	with := func(out string, extra ...string) []string {
		return append(e.base(out), append([]string{"--guest-binary-sha256", guestPins.binary}, extra...)...)
	}

	// No artifact: incomplete, and the guest cells are Pending_Guest.
	none := filepath.Join(e.dir, "none.json")
	if err := e.run(e.base(none)...); err != nil {
		t.Fatal(err)
	}
	if rep := status(none); rep.Complete || rep.ByStatus[routematrix.StatusPendingGuest] != 2 {
		t.Fatalf("no artifacts: %+v", rep)
	}

	bash := writeRouteArtifact(t, e.dir, guestevidence.KindRouteBashView, nil)
	rt := writeRouteArtifact(t, e.dir, guestevidence.KindRouteDirectRuntime, nil)
	both := filepath.Join(e.dir, "both.json")
	if err := e.run(with(both, "--artifact", guestevidence.KindRouteBashView+"="+bash, "--artifact", guestevidence.KindRouteDirectRuntime+"="+rt)...); err != nil {
		t.Fatal(err)
	}
	rep := status(both)
	if !rep.Complete || rep.ByStatus[routematrix.StatusCovered] != 4 {
		t.Fatalf("both artifacts: %+v", rep)
	}
	for _, kind := range []string{guestevidence.KindRouteBashView, guestevidence.KindRouteDirectRuntime} {
		if !strings.Contains(rep.Binding, "artifact:"+kind+"=sha256:") {
			t.Errorf("binding %q does not carry the digest of %s", rep.Binding, kind)
		}
	}
	if _, err := loadRoute(both, commit); err != nil {
		t.Fatalf("the run refuses the report: %v", err)
	}

	// An artifact that fails its validator covers nothing, and the report says why.
	denied := writeRouteArtifact(t, t.TempDir(), guestevidence.KindRouteBashView, func(d *guestevidence.RouteDenied) { d.Attempts[0].ExitCode = 0 })
	refused := filepath.Join(e.dir, "refused.json")
	e.out.Reset()
	if err := e.run(with(refused, "--artifact", guestevidence.KindRouteBashView+"="+denied, "--artifact", guestevidence.KindRouteDirectRuntime+"="+rt)...); err != nil {
		t.Fatal(err)
	}
	if rep := status(refused); rep.Complete || rep.ByRoute[routematrix.RouteBashView][routematrix.StatusPendingGuest] != 1 {
		t.Fatalf("refused artifact: %+v", rep)
	}
	if !strings.Contains(e.out.String(), "exit code 0 for a denied read") {
		t.Errorf("the output does not say why:\n%s", e.out.String())
	}

	// An artifact of another profile is not this run's.
	other := writeRouteArtifact(t, t.TempDir(), guestevidence.KindRouteBashView, func(d *guestevidence.RouteDenied) { d.ProfileHash = sha256Of([]byte("other")) })
	foreign := filepath.Join(e.dir, "foreign.json")
	if err := e.run(with(foreign, "--artifact", guestevidence.KindRouteBashView+"="+other)...); err != nil {
		t.Fatal(err)
	}
	if rep := status(foreign); rep.Complete || rep.ByRoute[routematrix.RouteBashView][routematrix.StatusPendingGuest] != 1 {
		t.Fatalf("foreign artifact: %+v", rep)
	}

	// A run of another service build or of another runtime bundle than run.json pins covers nothing.
	for name, mutate := range map[string]func(*guestevidence.RouteDenied){
		"another runner":         func(d *guestevidence.RouteDenied) { d.Binding.RunnerDigest = sha256Of([]byte("another runner")) },
		"another runtime bundle": func(d *guestevidence.RouteDenied) { d.Binding.RuntimeBundleDigest = sha256Of([]byte("another bundle")) },
	} {
		art := writeRouteArtifact(t, t.TempDir(), guestevidence.KindRouteBashView, mutate)
		out := filepath.Join(e.dir, strings.ReplaceAll(name, " ", "-")+".json")
		if err := e.run(with(out, "--artifact", guestevidence.KindRouteBashView+"="+art)...); err != nil {
			t.Fatal(err)
		}
		if rep := status(out); rep.Complete || rep.ByRoute[routematrix.RouteBashView][routematrix.StatusPendingGuest] != 1 {
			t.Fatalf("%s: %+v", name, rep)
		}
	}
}

func TestRouteReportCommandArtifactRefusals(t *testing.T) {
	e := guestRouteEnv(t)
	bash := writeRouteArtifact(t, e.dir, guestevidence.KindRouteBashView, nil)
	n := 0
	out := func() string { n++; return filepath.Join(e.dir, "r"+string(rune('a'+n))+".json") }
	for name, args := range map[string][]string{
		"no guest binary digest":        append(e.base(out()), "--artifact", guestevidence.KindRouteBashView+"="+bash),
		"bad guest binary digest":       append(e.base(out()), "--guest-binary-sha256", "deadbeef", "--artifact", guestevidence.KindRouteBashView+"="+bash),
		"unknown kind":                  append(e.base(out()), "--guest-binary-sha256", guestPins.binary, "--artifact", "typo="+bash),
		"kind the matrix does not name": append(e.base(out()), "--guest-binary-sha256", guestPins.binary, "--artifact", guestevidence.KindNetSink+"="+bash),
		"repeated kind": append(e.base(out()), "--guest-binary-sha256", guestPins.binary,
			"--artifact", guestevidence.KindRouteBashView+"="+bash, "--artifact", guestevidence.KindRouteBashView+"="+bash),
		"no equals": append(e.base(out()), "--guest-binary-sha256", guestPins.binary, "--artifact", guestevidence.KindRouteBashView),
	} {
		var buf bytes.Buffer
		a := &app{out: &buf, errOut: &bytes.Buffer{}}
		if err := a.run(append([]string{"route-report"}, args...)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The pinned matrix declares the production routes as needing the guest.
func TestPinnedRouteMatrixDeclaresTheProductionRoutesPendingGuest(t *testing.T) {
	m, err := parseRouteMatrix(readRepoFile(t, routeMatrixPath))
	if err != nil {
		t.Fatal(err)
	}
	want := map[routematrix.Route]string{
		routematrix.RouteBashView:      guestevidence.KindRouteBashView,
		routematrix.RouteDirectRuntime: guestevidence.KindRouteDirectRuntime,
	}
	for _, c := range m.Cells {
		kind, guest := want[c.Route]
		switch {
		case guest && (c.Status != routematrix.StatusPendingGuest || c.GuestArtifact != kind):
			t.Errorf("%s: status %s artifact %q, want Pending_Guest needing %s", c.Route, c.Status, c.GuestArtifact, kind)
		case !guest && (c.Status != routematrix.StatusCovered || c.GuestArtifact != ""):
			t.Errorf("%s: status %s artifact %q, want Covered with no artifact", c.Route, c.Status, c.GuestArtifact)
		}
	}
}
