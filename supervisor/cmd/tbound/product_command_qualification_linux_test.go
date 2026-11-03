//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
)

// passingQualificationSteps passes every qualification step and records each phase it runs. failAt is the
// phase that fails; the empty phase passes all of them.
func passingQualificationSteps(ran *[]productQualificationPhase, failAt productQualificationPhase) productCommandQualificationSteps {
	return productCommandQualificationSteps{
		loadHost: func(context.Context, productServiceConfig, *productTurnEnv) (*piruntime.D09ProductCommandHost, error) {
			return nil, nil
		},
		prepareRuntime:  func() error { return nil },
		reconcileScopes: func(context.Context, *piruntime.D09ProductCommandHost, []string) error { return nil },
		openCell: func(context.Context, *piruntime.D09ProductCommandHost, *productTurnEnv, productCommandRunnerFactory) (productQualificationCell, error) {
			return fakeQualificationCell{ran: ran, failAt: failAt}, nil
		},
		newRunner: func(context.Context, *piruntime.D09ProductCommandHost, productTurnCommandContext) (productTurnCommandRunner, error) {
			return nil, errProductBashRunnerUnavailable
		},
	}
}

// fakeQualificationCell passes every phase, records each one it ran, and fails at failAt.
type fakeQualificationCell struct {
	ran    *[]productQualificationPhase
	failAt productQualificationPhase
}

func (c fakeQualificationCell) runPhase(_ context.Context, phase productQualificationPhase) error {
	*c.ran = append(*c.ran, phase)
	if phase == c.failAt {
		return errors.New("injected failure")
	}
	return nil
}

func (fakeQualificationCell) finish(context.Context, error) error { return nil }

func withProductCommandQualification(t *testing.T, steps productCommandQualificationSteps) {
	t.Helper()
	original := productCommandQualification
	productCommandQualification = steps
	t.Cleanup(func() { productCommandQualification = original })
}

// startWithHostCapture starts the service through the production host and keeps the environment that host
// built, so a refused start can be checked for the hooks and conversations it never installed.
func startWithHostCapture(f *productServiceFixture) (*productService, *productTurnEnv, error) {
	var captured *productTurnEnv
	opts := f.options()
	opts.Host = func(env *productTurnEnv) error {
		captured = env
		return newProductServiceHostProfile(f.cfg)(env)
	}
	svc, err := startProductService(f.cfg, opts)
	return svc, captured, err
}

func TestProductBashStartupRequiresQualificationBeforeListen(t *testing.T) {
	for _, failing := range productQualificationPhases {
		t.Run("fails at "+string(failing), func(t *testing.T) {
			f := newProductHostFixture(t, true)
			var ran []productQualificationPhase
			withProductCommandQualification(t, passingQualificationSteps(&ran, failing))
			svc, env, err := startWithHostCapture(f)
			if svc != nil {
				_ = svc.Shutdown()
			}
			if err == nil || !strings.Contains(err.Error(), string(failing)) {
				t.Fatalf("a failed %s phase started the service: %v", failing, err)
			}
			if _, statErr := os.Stat(f.socketPath); !os.IsNotExist(statErr) {
				t.Fatal("a failed qualification opened the socket")
			}
			if env.NewCommandRunner != nil || env.OpenCommandHistory != nil || env.NewConversation != nil {
				t.Fatal("a failed qualification installed a command hook or a provider conversation")
			}
			want := productQualificationPhases[:slices.Index(productQualificationPhases, failing)+1]
			if !reflect.DeepEqual(ran, want) {
				t.Fatalf("phases ran %v, want %v: a later phase ran after a failure", ran, want)
			}
		})
	}
	t.Run("passing phases install both hooks before the socket opens", func(t *testing.T) {
		f := newProductHostFixture(t, true)
		var ran []productQualificationPhase
		withProductCommandQualification(t, passingQualificationSteps(&ran, ""))
		svc, env, err := startWithHostCapture(f)
		if err != nil {
			t.Fatalf("a qualified bash host was refused: %v", err)
		}
		defer func() { _ = svc.Shutdown() }()
		if env.NewCommandRunner == nil || env.OpenCommandHistory == nil {
			t.Fatal("qualification did not install both command hooks")
		}
		if !reflect.DeepEqual(ran, productQualificationPhases) {
			t.Fatalf("phases ran %v, want every phase in order %v", ran, productQualificationPhases)
		}
		if _, err := os.Stat(f.socketPath); err != nil {
			t.Fatalf("the qualified service did not open its socket: %v", err)
		}
	})
}

func TestProductBashQualificationHasNoSetupOrDoctorBypass(t *testing.T) {
	t.Run("an assigned runner is not a qualification", func(t *testing.T) {
		f := newProductHostFixture(t, true)
		opts := f.options()
		opts.Host = func(env *productTurnEnv) error {
			env.NewCommandRunner = func(productTurnCommandContext) (productTurnCommandRunner, error) {
				return nil, errProductBashRunnerUnavailable
			}
			return newProductServiceHostProfile(f.cfg)(env)
		}
		if _, err := startProductService(f.cfg, opts); err == nil || !strings.Contains(err.Error(), "command qualification") {
			t.Fatalf("a bash policy started on an assigned runner: %v", err)
		}
	})
	t.Run("a stale passed record does not qualify a new start", func(t *testing.T) {
		f := newProductHostFixture(t, true)
		stale := filepath.Join(f.stateRoot, "qualification", "previous-run")
		if err := os.MkdirAll(stale, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stale, "qualified.json"), []byte(`{"status":"passed"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		var ran []productQualificationPhase
		withProductCommandQualification(t, passingQualificationSteps(&ran, productPhaseNetworkProbe))
		if _, _, err := startWithHostCapture(f); err == nil || !strings.Contains(err.Error(), string(productPhaseNetworkProbe)) {
			t.Fatalf("a stale passed record admitted bash: %v", err)
		}
	})
}

// plantOwnerJournal writes one retained command owner journal under the state root's turns, as a Bash turn
// does before its cell launches. An empty record list leaves an empty journal, the shape of a failed allocation.
func plantOwnerJournal(t *testing.T, stateRoot, workflowID string, owners ...productD09ScopeOwner) {
	t.Helper()
	dir := filepath.Join(stateRoot, "turns", workflowID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := audit.Open(filepath.Join(dir, productCommandOwnerJournal))
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range owners {
		if err := persistProductCommandScopeOwner(journal, owner); err != nil {
			t.Fatal(err)
		}
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProductD09RestartFindsPreReceiptAndQualificationOrphans(t *testing.T) {
	orphan := productD09ScopeOwner{Schema: "tbound-product-d09-scope-owner/v1", State: "opened", WorkflowID: "pwf-orphan", RunID: "prun-orphan",
		RegistrationDigest: digestBytes([]byte("registration")), ProfileDigest: digestBytes([]byte("profile")),
		RootDigest: digestBytes([]byte("root")), ScopePath: "/sys/fs/cgroup/system.slice/tbound-product.service/cells/turn-orphan",
		ScopeIdentity: digestBytes([]byte("scope")), CommandOwnershipVersion: 1}
	// startWithCells starts a deny-only service whose cells root is cells, with the real retained-scope reconciler.
	startWithCells := func(t *testing.T, f *productServiceFixture, cells string) (*productService, *productTurnEnv, error) {
		var ran []productQualificationPhase
		steps := passingQualificationSteps(&ran, "")
		steps.reconcileScopes = func(ctx context.Context, _ *piruntime.D09ProductCommandHost, roots []string) error {
			root, err := os.Open(cells)
			if err != nil {
				return err
			}
			defer root.Close()
			return reconcileProductCommandScopesUnder(ctx, root, roots...)
		}
		withProductCommandQualification(t, steps)
		return startWithHostCapture(f)
	}
	t.Run("a clean deny-only start needs no host and no owner", func(t *testing.T) {
		f := newProductHostFixture(t, false)
		svc, env, err := startWithCells(t, f, t.TempDir())
		if err != nil {
			t.Fatalf("a clean deny-only service was refused: %v", err)
		}
		defer func() { _ = svc.Shutdown() }()
		if env.NewCommandRunner != nil {
			t.Fatal("a deny-only service installed a launch factory")
		}
	})
	t.Run("an empty owner journal from a failed allocation blocks nothing", func(t *testing.T) {
		f := newProductHostFixture(t, false)
		plantOwnerJournal(t, f.stateRoot, "pwf-failed-allocation")
		svc, _, err := startWithCells(t, f, t.TempDir())
		if err != nil {
			t.Fatalf("an empty owner journal blocked startup: %v", err)
		}
		_ = svc.Shutdown()
	})
	withProcessesInScope := func(t *testing.T, writers int) {
		original := productProcessesInScope
		productProcessesInScope = func(context.Context, string) (int, error) { return writers, nil }
		t.Cleanup(func() { productProcessesInScope = original })
	}
	t.Run("an opened owner record whose scope a process still reports blocks startup before listening", func(t *testing.T) {
		f := newProductHostFixture(t, false)
		plantOwnerJournal(t, f.stateRoot, "pwf-orphan", orphan)
		withProcessesInScope(t, 1)
		svc, _, err := startWithCells(t, f, t.TempDir())
		if svc != nil {
			_ = svc.Shutdown()
		}
		if err == nil || !strings.Contains(err.Error(), "retained command scopes") {
			t.Fatalf("an unproved orphan scope did not block startup: %v", err)
		}
		if _, statErr := os.Stat(f.socketPath); !os.IsNotExist(statErr) {
			t.Fatal("an unproved orphan scope opened the socket")
		}
	})
	t.Run("an opened owner record whose scope is gone with no process left is drained before listening", func(t *testing.T) {
		f := newProductHostFixture(t, false)
		plantOwnerJournal(t, f.stateRoot, "pwf-orphan", orphan)
		withProcessesInScope(t, 0)
		svc, _, err := startWithCells(t, f, t.TempDir())
		if err != nil {
			t.Fatalf("a gone scope with no writer blocked startup: %v", err)
		}
		_ = svc.Shutdown()
		if kinds := ownerJournalKinds(t, filepath.Join(f.stateRoot, "turns", "pwf-orphan", productCommandOwnerJournal)); !slices.Equal(kinds, []string{"product_command_scope_opened", "product_command_scope_drained"}) {
			t.Fatalf("owner journal %v", kinds)
		}
	})
	t.Run("an unrecorded command cell blocks a bash-enabled startup", func(t *testing.T) {
		// A deny-only start without any journal never opens the cells root: it has no verified host to do so.
		f := newProductHostFixture(t, true)
		cells := t.TempDir()
		if err := os.Mkdir(filepath.Join(cells, "turn-stray"), 0o700); err != nil {
			t.Fatal(err)
		}
		svc, _, err := startWithCells(t, f, cells)
		if svc != nil {
			_ = svc.Shutdown()
		}
		if err == nil || !strings.Contains(err.Error(), "unrecorded product command scope") {
			t.Fatalf("an unrecorded cell did not block startup: %v", err)
		}
	})
}

func TestProductNetworkProbeFailsOnTimeoutMissingNodeOrDeadControl(t *testing.T) {
	const listener = "127.0.0.1:41234"
	denied := productDeniedNetworkOutput(t, listener, nil)
	live := func(context.Context, string) error { return nil }
	routed := func(context.Context, string, string) error { return nil }
	cellOutput := func(output []byte) productNetworkCellRun {
		return func(context.Context, string) ([]byte, error) { return output, nil }
	}
	cases := map[string]struct {
		run     productNetworkCellRun
		control productNetworkControl
		want    string
	}{
		"a denied probe passes":     {cellOutput(denied), live, ""},
		"a cell that never answers": {func(ctx context.Context, _ string) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() }, live, "did not run"},
		"a missing node executable": {func(context.Context, string) ([]byte, error) {
			return nil, errors.New(`exec: "node": executable file not found`)
		}, live, "did not run"},
		"a node syntax error":           {func(context.Context, string) ([]byte, error) { return nil, errors.New("exit status 1: SyntaxError") }, live, "did not run"},
		"output without a node version": {cellOutput(productDeniedNetworkOutput(t, listener, func(r *productNetworkReport) { r.Node = "" })), live, "Node version"},
		"output that is not structured": {cellOutput([]byte("not json")), live, "structured result"},
		"a dead positive control":       {cellOutput(denied), func(context.Context, string) error { return errors.New("connection refused") }, "positive control"},
		"a host check that timed out": {cellOutput(productDeniedNetworkOutput(t, listener, func(r *productNetworkReport) {
			r.Checks[3].Outcome, r.Checks[3].Errno = "timeout", "ETIMEDOUT"
		})), live, "host-listener"},
		"a host listener the cell reached": {cellOutput(productDeniedNetworkOutput(t, listener, func(r *productNetworkReport) {
			r.Checks[3].Outcome, r.Checks[3].Errno = "reachable", ""
		})), live, "host-listener"},
		"a route through a real interface": {cellOutput(productDeniedNetworkOutput(t, listener, func(r *productNetworkReport) {
			r.Routes = append(r.Routes, productNetworkRoute{Family: "ipv4", Iface: "eth0"})
		})), live, "eth0"},
		"a check that never ran": {cellOutput(productDeniedNetworkOutput(t, listener, func(r *productNetworkReport) {
			r.Checks = r.Checks[:3]
		})), live, "recorded"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			report, err := productNetworkProbe(ctx, "deny-all", listener, "provider.test", tc.run, tc.control, routed, productTestResolve)
			if tc.want == "" {
				if err != nil || len(report.Checks) != 4 {
					t.Fatalf("a denied probe failed: %v (%d checks)", err, len(report.Checks))
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the probe passed or failed for the wrong reason: %v, want %q", err, tc.want)
			}
		})
	}
}

// The positive controls come from the service context, never from the cell. A host with no IPv4 route to the numeric
// destination cannot tell containment from a dead network; one with no IPv6 route is recorded and does not refuse.
func TestProductNetworkProbeControlsAreServiceSideAndRecorded(t *testing.T) {
	const listener = "127.0.0.1:41234"
	ctx := context.Background()
	live := func(context.Context, string) error { return nil }
	t.Run("a cell cannot supply its own controls", func(t *testing.T) {
		forged := productDeniedNetworkOutput(t, listener, func(r *productNetworkReport) {
			r.Controls = []productNetworkCheck{{ID: "ipv4-route", Destination: "forged", Outcome: "connect"}}
		})
		report, err := productNetworkProbe(ctx, "deny-all", listener, "provider.test", func(context.Context, string) ([]byte, error) { return forged, nil }, live,
			func(context.Context, string, string) error { return nil }, productTestResolve)
		if err != nil {
			t.Fatal(err)
		}
		for _, control := range report.Controls {
			if control.Destination == "forged" {
				t.Fatalf("the cell's controls were kept: %+v", report.Controls)
			}
		}
	})
	t.Run("no IPv4 route refuses before the cell runs", func(t *testing.T) {
		ran := false
		_, err := productNetworkProbe(ctx, "deny-all", listener, "provider.test", func(context.Context, string) ([]byte, error) { ran = true; return nil, nil }, live,
			func(_ context.Context, network, _ string) error {
				if network == "tcp4" {
					return syscall.ENETUNREACH
				}
				return nil
			}, productTestResolve)
		if err == nil || !strings.Contains(err.Error(), "dead network") || ran {
			t.Fatalf("a host without a route passed: %v (cell ran %v)", err, ran)
		}
	})
	t.Run("no IPv6 route is recorded and does not refuse", func(t *testing.T) {
		report, err := productNetworkProbe(ctx, "deny-all", listener, "provider.test", func(ctx context.Context, program string) ([]byte, error) {
			return productTestNetworkOutputForProgram(t, listener, program), nil
		}, live, func(_ context.Context, network, _ string) error {
			if network == "tcp6" {
				return syscall.ENETUNREACH
			}
			return nil
		}, productTestResolve)
		if err != nil || report.Controls[len(report.Controls)-1].Outcome != "not available on host" {
			t.Fatalf("%v %+v", err, report.Controls)
		}
	})
}

// productDeniedNetworkOutput is the structured result of a cell with no route, no resolver and no reachable
// host, as the probe program prints it. mutate changes one field so a test can break exactly one check.
func productDeniedNetworkOutput(t *testing.T, listener string, mutate func(*productNetworkReport)) []byte {
	t.Helper()
	report := productNetworkReport{Node: "v22.12.0", Interfaces: []string{"lo"}, Controls: productTestNetworkControls(listener), Routes: []productNetworkRoute{{Family: "ipv4", Iface: "lo"}},
		Checks: []productNetworkCheck{
			{ID: "name-resolution", Destination: "provider.test", Outcome: "error", Errno: "ECONNREFUSED"},
			{ID: "ipv4-connect", Destination: "192.0.2.1:443", Outcome: "error", Errno: "ENETUNREACH"},
			{ID: "ipv6-connect", Destination: "[2001:db8::1]:443", Outcome: "error", Errno: "ENETUNREACH"},
			{ID: "host-listener", Destination: listener, Outcome: "error", Errno: "ECONNREFUSED"},
		}}
	if mutate != nil {
		mutate(&report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func productTestResolve(context.Context, string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP("192.0.2.1")}, {IP: net.ParseIP("2001:db8::1")}}, nil
}
func productTestNetworkControls(listener string) []productNetworkCheck {
	return []productNetworkCheck{
		{ID: "host-listener", Destination: listener, Outcome: "reachable"},
		{ID: "name-resolution", Destination: "provider.test", Outcome: "resolved"},
		{ID: "ipv4-connect", Destination: "192.0.2.1:443", Outcome: "reachable"},
		{ID: "ipv6-connect", Destination: "[2001:db8::1]:443", Outcome: "reachable"},
	}
}
func productTestNetworkOutputForProgram(t *testing.T, listener, program string) []byte {
	return productDeniedNetworkOutput(t, listener, func(report *productNetworkReport) {
		if strings.Contains(program, `"IPv6":""`) {
			report.Checks = append(report.Checks[:2], report.Checks[3:]...)
		}
	})
}
