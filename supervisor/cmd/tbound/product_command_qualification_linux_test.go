//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
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
		runPhase: func(_ context.Context, _ *piruntime.D09ProductCommandHost, phase productQualificationPhase) error {
			*ran = append(*ran, phase)
			if phase == failAt {
				return errors.New("injected failure")
			}
			return nil
		},
		newRunner: func(context.Context, *piruntime.D09ProductCommandHost, productTurnCommandContext) (productTurnCommandRunner, error) {
			return nil, errProductBashRunnerUnavailable
		},
	}
}

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
		ScopeIdentity: digestBytes([]byte("scope"))}
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
	t.Run("an opened owner record without its scope blocks startup before listening", func(t *testing.T) {
		f := newProductHostFixture(t, false)
		plantOwnerJournal(t, f.stateRoot, "pwf-orphan", orphan)
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
	t.Run("an unrecorded command cell blocks startup", func(t *testing.T) {
		f := newProductHostFixture(t, false)
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
			report, err := productNetworkProbe(ctx, "deny-all", listener, tc.run, tc.control)
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

// productDeniedNetworkOutput is the structured result of a cell with no route, no resolver and no reachable
// host, as the probe program prints it. mutate changes one field so a test can break exactly one check.
func productDeniedNetworkOutput(t *testing.T, listener string, mutate func(*productNetworkReport)) []byte {
	t.Helper()
	report := productNetworkReport{Node: "v22.12.0", Routes: []productNetworkRoute{{Family: "ipv4", Iface: "lo"}},
		Checks: []productNetworkCheck{
			{ID: "name-resolution", Destination: "probe.invalid", Outcome: "error", Errno: "EAI_AGAIN"},
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
