//go:build linux

package sessionrepo

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

// The runner of a product-scoped Store learns which protected and read-only entries exist in its
// view, outermost first, so it can mount them read-only. Entries absent from the view and entries
// below another listed one are left out. Under a research rule the list is empty.
func TestCommandViewListsReadOnlyMountPaths(t *testing.T) {
	product := productScope([]string{"src"}, []string{"README.md", "docs", "missing.txt"}, []string{"docs/secret.txt"})
	for name, tc := range map[string]struct {
		scope AdmissionScope
		want  []string
	}{
		"product scope": {product, []string{"README.md", "docs"}},
		"research rule": {AdmissionScope{}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			store, _, _, authority := newScopedE2EStore(t, tc.scope)
			source := filepath.Join(t.TempDir(), "source")
			writeFixtureDir(t, source, 0o700)
			writeFixtureDir(t, filepath.Join(source, "docs"), 0o755)
			writeFixtureDir(t, filepath.Join(source, "src"), 0o755)
			writeFixtureFile(t, filepath.Join(source, "README.md"), "readme\n", 0o644)
			writeFixtureFile(t, filepath.Join(source, "docs", "secret.txt"), "secret\n", 0o644)
			writeFixtureFile(t, filepath.Join(source, "src", "main.go"), "package main\n", 0o644)
			g0, err := store.Seed(openFixtureRoot(t, source), RootAttestation{Quiescent: true})
			if err != nil {
				t.Fatalf("seed: %v", err)
			}
			op, decision := lease("ro-mounts"), allow("ro-mounts")
			spec := CommandSpec{Executable: "/bin/bash", Args: []string{"-c", "true"}, DeclaredOutputPaths: []string{"src/main.go"}}
			viewID := reserveTestViewID(t, store)
			argumentDigest, _ := commandArgumentsDigest(spec)
			contextDigest, _ := commandExecutionContextDigest(viewID, g0.TreeDigest(), op.LeaseID)
			authority.grant(OperationRequest{Tool: "bash", Operation: op, Decision: decision, InputGeneration: g0.ID(),
				InputTreeDigest: g0.TreeDigest(), ArgumentDigest: argumentDigest, ViewID: viewID,
				OutputScopePaths: spec.DeclaredOutputPaths, ExecutionContextDigest: contextDigest})

			var got []string
			var listErr error
			stop := errors.New("stop after listing")
			_, err = store.RunBash(context.Background(), g0, op, decision, spec, CommandRunnerFunc(
				func(_ context.Context, view *CommandView, _ CommandSpec) (CommandResult, CommandSettlement, error) {
					got, listErr = view.ReadOnlyMountPaths()
					return CommandResult{}, CommandSettlement{}, stop
				}))
			if !errors.Is(err, stop) {
				t.Fatalf("runner was not reached: %v", err)
			}
			if listErr != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("read-only mount paths = %q, %v; want %q", got, listErr, tc.want)
			}
		})
	}
}
