//go:build linux

package sessionrepo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/delta"
)

func changesAt(paths ...string) []delta.ObjectChange {
	out := make([]delta.ObjectChange, len(paths))
	for i, p := range paths {
		out[i] = delta.ObjectChange{Path: p}
	}
	return out
}

func TestAdmissionScopeDecide(t *testing.T) {
	for _, tc := range []struct {
		name    string
		scope   AdmissionScope
		request []string
		changes []delta.ObjectChange
		outcome string
		reason  string
	}{
		{"default allows the exact path", AdmissionScope{}, []string{"a.txt"}, changesAt("a.txt"), "allow", ""},
		{"default denies an uncovered path", AdmissionScope{}, []string{"a.txt"}, changesAt("b.txt"), "deny", DenyReasonUncoveredPath},
		{"default denies a parent as scope", AdmissionScope{}, []string{"d"}, changesAt("d/x"), "deny", DenyReasonUncoveredPath},
		{"protected exact path wins over the output scope", AdmissionScope{ProtectedPaths: []string{"a.txt"}}, []string{"a.txt"},
			changesAt("a.txt"), "deny", DenyReasonProtectedPath},
		{"protected directory covers what is under it", AdmissionScope{ProtectedPaths: []string{"d"}}, []string{"d/x"},
			changesAt("d/x"), "deny", DenyReasonProtectedPath},
		{"protected directory covers the directory itself", AdmissionScope{ProtectedPaths: []string{"d"}}, []string{"d"},
			changesAt("d"), "deny", DenyReasonProtectedPath},
		{"a sibling with the same prefix is not protected", AdmissionScope{ProtectedPaths: []string{"d"}}, []string{"dd/x"},
			changesAt("dd/x"), "allow", ""},
		{"a protected change among allowed ones denies", AdmissionScope{ProtectedPaths: []string{"b"}}, []string{"a", "b"},
			changesAt("a", "b"), "deny", DenyReasonProtectedPath},
		{"protected paths do not hide an uncovered one", AdmissionScope{ProtectedPaths: []string{"p"}}, []string{"a"},
			changesAt("a", "z"), "deny", DenyReasonUncoveredPath},
		{"promote-all allows an uncovered path", AdmissionScope{Rule: AdmissionScopeRulePromoteAll}, []string{"a"},
			changesAt("z"), "allow", ""},
		{"no change is always covered", AdmissionScope{ProtectedPaths: []string{"a"}}, []string{}, nil, "allow", ""},
	} {
		outcome, reason := tc.scope.Decide(tc.request, tc.changes)
		if outcome != tc.outcome || reason != tc.reason {
			t.Errorf("%s: got %s/%q, want %s/%q", tc.name, outcome, reason, tc.outcome, tc.reason)
		}
	}
}

func TestAdmissionScopeValidate(t *testing.T) {
	for name, scope := range map[string]AdmissionScope{
		"unknown rule":                   {Rule: "promote-some/v1"},
		"protected with promote-all":     {Rule: AdmissionScopeRulePromoteAll, ProtectedPaths: []string{"a"}},
		"absolute protected path":        {ProtectedPaths: []string{"/a"}},
		"dotdot protected path":          {ProtectedPaths: []string{"a/../b"}},
		"unsorted protected paths":       {ProtectedPaths: []string{"b", "a"}},
		"duplicate protected paths":      {ProtectedPaths: []string{"a", "a"}},
		"empty protected path":           {ProtectedPaths: []string{""}},
		"empty but non-nil list as rule": {Rule: "x"},
	} {
		if err := scope.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for name, scope := range map[string]AdmissionScope{
		"zero":             {},
		"explicit default": {Rule: AdmissionScopeRuleExactPath},
		"protected":        {ProtectedPaths: []string{"a", "b/c"}},
		"promote-all":      {Rule: AdmissionScopeRulePromoteAll},
	} {
		if err := scope.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if !(AdmissionScope{}).IsZero() || !(AdmissionScope{Rule: AdmissionScopeRuleExactPath}).IsZero() ||
		(AdmissionScope{ProtectedPaths: []string{"a"}}).IsZero() || (AdmissionScope{Rule: AdmissionScopeRulePromoteAll}).IsZero() {
		t.Error("IsZero must be true exactly for the default rule without protected paths")
	}
}

func TestAdmissionPolicyDigestKeepsTheDefaultAndSeparatesScopes(t *testing.T) {
	policy, metadata := digestBytes([]byte("p")), digestBytes([]byte("m"))
	// The digest of the default scope is the one that existed before scopes: its payload is unchanged.
	legacy, err := json.Marshal(struct {
		SchemaVersion        string `json:"schema_version"`
		PolicyDigest         string `json:"policy_digest"`
		MetadataPolicyDigest string `json:"metadata_policy_digest"`
		ScopeRule            string `json:"scope_rule"`
	}{AdmissionSchemaVersion, policy, metadata, "exact-path-output-scope/v1"})
	if err != nil {
		t.Fatal(err)
	}
	def, err := AdmissionPolicyDigest(policy, metadata, AdmissionScope{})
	if err != nil || def != digestBytes(legacy) {
		t.Fatalf("default digest = %q, %v; want the pre-scope digest %q", def, err, digestBytes(legacy))
	}
	explicit, _ := AdmissionPolicyDigest(policy, metadata, AdmissionScope{Rule: AdmissionScopeRuleExactPath})
	if explicit != def {
		t.Fatal("naming the default rule must not change the digest")
	}
	seen := map[string]string{def: "default"}
	for name, scope := range map[string]AdmissionScope{
		"promote-all":  {Rule: AdmissionScopeRulePromoteAll},
		"protected-a":  {ProtectedPaths: []string{"a"}},
		"protected-b":  {ProtectedPaths: []string{"b"}},
		"protected-ab": {ProtectedPaths: []string{"a", "b"}},
	} {
		digest, err := AdmissionPolicyDigest(policy, metadata, scope)
		if err != nil {
			t.Fatal(err)
		}
		if other, dup := seen[digest]; dup {
			t.Errorf("%s shares an admission policy digest with %s", name, other)
		}
		seen[digest] = name
	}
	if _, err := AdmissionPolicyDigest(policy, metadata, AdmissionScope{Rule: "x"}); err == nil {
		t.Fatal("an invalid scope has no digest")
	}
}

func TestAppendAdmissionScopedBindsTheScopeToTheRecord(t *testing.T) {
	protected := AdmissionScope{ProtectedPaths: []string{"task.txt"}}
	journal, request, intent, policy, metadata, input, output, candidate := admissionFixture(t, []string{"task.txt"})
	defer journal.Close()
	reference, err := AppendAdmissionScoped(journal, request, intent, policy, metadata, input, output, candidate, protected)
	if err != nil {
		t.Fatal(err)
	}
	if reference.Outcome != "deny" {
		t.Fatalf("a protected path covered by the output scope must be denied, got %q", reference.Outcome)
	}
	wantDigest, _ := AdmissionPolicyDigest(policy, metadata, protected)
	if reference.AdmissionPolicyDigest != wantDigest {
		t.Fatalf("reference digest %q, want %q", reference.AdmissionPolicyDigest, wantDigest)
	}
	if err := VerifyAdmissionScoped(journal, reference, intent, policy, metadata, protected); err != nil {
		t.Fatalf("verify under the scope that produced it: %v", err)
	}
	if err := VerifyAdmission(journal, reference, intent, policy, metadata); err == nil {
		t.Fatal("a record written under protected paths verified under the default scope")
	}
	if err := VerifyAdmissionScoped(journal, reference, intent, policy, metadata, AdmissionScope{ProtectedPaths: []string{"other"}}); err == nil {
		t.Fatal("a record verified under another scope")
	}
}

func TestAppendAdmissionPromoteAllAllowsAnUncoveredChange(t *testing.T) {
	promote := AdmissionScope{Rule: AdmissionScopeRulePromoteAll}
	journal, request, intent, policy, metadata, input, output, candidate := admissionFixture(t, []string{"tasks"})
	defer journal.Close()
	reference, err := AppendAdmission(journal, request, intent, policy, metadata, input, output, candidate)
	if err != nil || reference.Outcome != "deny" {
		t.Fatalf("default scope must deny the uncovered change: %+v, %v", reference, err)
	}
	// a fresh journal so the one-use intent id is not repeated
	journal2, request, intent, policy, metadata, input, output, candidate := admissionFixture(t, []string{"tasks"})
	defer journal2.Close()
	reference, err = AppendAdmissionScoped(journal2, request, intent, policy, metadata, input, output, candidate, promote)
	if err != nil || reference.Outcome != "allow" {
		t.Fatalf("promote-all must allow: %+v, %v", reference, err)
	}
	if err := VerifyAdmissionScoped(journal2, reference, intent, policy, metadata, promote); err != nil {
		t.Fatal(err)
	}
	if reference.AdmissionPolicyDigest == "" {
		t.Fatal("no admission policy digest")
	}
}

func scopedE2EStore(t *testing.T, scope AdmissionScope) (*Store, *Generation, *e2eReceiptAuthority) {
	t.Helper()
	store, _, _, authority := newE2EStore(t)
	store.options.AdmissionScope = scope
	sourcePath := filepath.Join(t.TempDir(), "source")
	buildWriteFixtureTree(t, sourcePath, writeFixtureFiles())
	g0, err := store.Seed(openFixtureRoot(t, sourcePath), RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatal(err)
	}
	return store, g0, authority
}

func TestStoreWriteToAProtectedPathIsDeniedWithoutPromoting(t *testing.T) {
	store, g0, authority := scopedE2EStore(t, AdmissionScope{ProtectedPaths: []string{"build", "plain.txt"}})
	files := writeFixtureFiles()
	for _, tc := range []struct{ id, path string }{{"w1", "plain.txt"}, {"w2", "build/result.txt"}} {
		changed := cloneWriteFixtureFiles(files)
		content := []byte("tampered\n")
		for i := range changed {
			if changed[i].path == tc.path {
				changed[i].content = string(content)
			}
		}
		operation, decision := proposal(tc.id), allow(tc.id)
		request := OperationRequest{Tool: "write", Operation: operation, Decision: decision, InputGeneration: g0.ID(),
			InputTreeDigest: g0.TreeDigest(), ArgumentDigest: writeArgumentsDigestForTest(tc.path, content), OutputScopePaths: []string{tc.path}}
		authority.grant(request)
		authority.grantBinding(decision.ID, exactBinding(g0.snapshot, expectedWriteSnapshot(t, "g1", changed), request, 1))
		result, err := store.Write(context.Background(), g0, operation, decision, tc.path, content)
		if !errors.Is(err, ErrDeltaDenied) || result.Outcome != "denied_after_effect" || result.Generation != nil ||
			result.Admission == nil || result.Admission.Outcome != "deny" || result.DenyReason != DenyReasonProtectedPath {
			t.Fatalf("%s: result = %+v, err = %v", tc.path, result, err)
		}
		if len(store.generations) != 1 || len(store.transitions) != 0 {
			t.Fatalf("%s: a denied change advanced the store", tc.path)
		}
		if got, err := os.ReadFile(filepath.Join(g0.testPath(), filepath.FromSlash(tc.path))); err != nil || string(got) == "tampered\n" {
			t.Fatalf("%s: sealed input changed: %q %v", tc.path, got, err)
		}
	}
	// The store stays usable: a change outside the protected paths is promoted.
	changed := cloneWriteFixtureFiles(files)
	changed[0].content = "readme updated\n"
	operation, decision := proposal("ok"), allow("ok")
	request := OperationRequest{Tool: "write", Operation: operation, Decision: decision, InputGeneration: g0.ID(),
		InputTreeDigest: g0.TreeDigest(), ArgumentDigest: writeArgumentsDigestForTest("README.md", []byte("readme updated\n")),
		OutputScopePaths: []string{"README.md"}}
	authority.grant(request)
	authority.grantBinding(decision.ID, exactBinding(g0.snapshot, expectedWriteSnapshot(t, "g1", changed), request, 1))
	result, err := store.Write(context.Background(), g0, operation, decision, "README.md", []byte("readme updated\n"))
	if err != nil || result.Outcome != "success" || result.Generation == nil || result.DenyReason != "" {
		t.Fatalf("an unprotected write after a denial: %+v, %v", result, err)
	}
	// The denial and the promotion are reconstructable from the retained evidence under the same scope only.
	bundle, err := store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	if bundle.AdmissionScope.IsZero() {
		t.Fatal("the bundle must carry the admission scope it was written under")
	}
	disposition, err := Reconstruct(bundle)
	if err != nil || disposition.Denials != 2 {
		t.Fatalf("reconstruct under the bundle scope: denials=%d, %v", disposition.Denials, err)
	}
	bundle.AdmissionScope = AdmissionScope{}
	if _, err := Reconstruct(bundle); err == nil {
		t.Fatal("Reconstruct accepted records written under protected paths as if the scope were the default")
	}
}

func TestStoreEditUnderAProtectedDirectoryIsDenied(t *testing.T) {
	store, g0, authority := scopedE2EStore(t, AdmissionScope{ProtectedPaths: []string{"build"}})
	changed := cloneWriteFixtureFiles(writeFixtureFiles())
	changed[3].content = "baseline replaced\n"
	edits := []Replacement{{OldText: "baseline result", NewText: "baseline replaced"}}
	argumentDigest, err := argumentsDigest(struct {
		Path  string        `json:"path"`
		Edits []Replacement `json:"edits"`
	}{"build/result.txt", edits})
	if err != nil {
		t.Fatal(err)
	}
	operation, decision := proposal("e1"), allow("e1")
	request := OperationRequest{Tool: "edit", Operation: operation, Decision: decision, InputGeneration: g0.ID(),
		InputTreeDigest: g0.TreeDigest(), ArgumentDigest: argumentDigest, OutputScopePaths: []string{"build/result.txt"}}
	authority.grant(request)
	authority.grantBinding(decision.ID, exactBinding(g0.snapshot, expectedWriteSnapshot(t, "g1", changed), request, 1))
	result, err := store.Edit(context.Background(), g0, operation, decision, "build/result.txt", edits)
	if !errors.Is(err, ErrDeltaDenied) || result.Generation != nil || result.DenyReason != DenyReasonProtectedPath {
		t.Fatalf("edit result = %+v, err = %v", result, err)
	}
	if len(store.generations) != 1 {
		t.Fatal("a denied edit advanced the store")
	}
}

func bashChange(t *testing.T, store *Store, g0 *Generation, authority *e2eReceiptAuthority, id string, declared []string, changedFile string) (MutationResult, error) {
	t.Helper()
	files := writeFixtureFiles()
	changed := cloneWriteFixtureFiles(files)
	for i := range changed {
		if changed[i].path == changedFile {
			changed[i].content = "by bash\n"
		}
	}
	expected := expectedWriteSnapshot(t, "g1", changed)
	op, decision := lease(id), allow(id)
	viewID := reserveTestViewID(t, store)
	spec := CommandSpec{Executable: "/bin/true", DeclaredOutputPaths: declared}
	argumentDigest, err := CommandArgumentsDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	contextDigest, err := commandExecutionContextDigest(viewID, g0.TreeDigest(), op.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	request := OperationRequest{Tool: "bash", Operation: op, Decision: decision, InputGeneration: g0.ID(),
		InputTreeDigest: g0.TreeDigest(), ArgumentDigest: argumentDigest, ViewID: viewID, ExecutionContextDigest: contextDigest,
		OutputScopePaths: declared}
	authority.grant(request)
	authority.grantBinding(decision.ID, exactBinding(g0.snapshot, expected, request, 1))
	return store.RunBash(context.Background(), g0, op, decision, spec,
		CommandRunnerFunc(func(_ context.Context, view *CommandView, _ CommandSpec) (CommandResult, CommandSettlement, error) {
			if err := os.WriteFile(filepath.Join(view.testPath(), filepath.FromSlash(changedFile)), []byte("by bash\n"), 0o644); err != nil {
				return CommandResult{}, CommandSettlement{}, err
			}
			return CommandResult{ExitObserved: true}, CommandSettlement{
				LeaseID: op.LeaseID, ViewID: view.ID(), ProcessScopeEmpty: true, WritersStopped: true,
				MountDetached: true, ExitObserved: true, EvidenceClass: "synthetic-test-fixture",
			}, nil
		}))
}

func TestStoreBashChangeOfAProtectedPathIsDeniedEvenWhenDeclared(t *testing.T) {
	store, g0, authority := scopedE2EStore(t, AdmissionScope{ProtectedPaths: []string{"plain.txt"}})
	result, err := bashChange(t, store, g0, authority, "protected", []string{"plain.txt"}, "plain.txt")
	if !errors.Is(err, ErrDeltaDenied) || result.Outcome != "denied_after_effect" || result.Generation != nil ||
		result.DenyReason != DenyReasonProtectedPath {
		t.Fatalf("bash result = %+v, err = %v", result, err)
	}
	if len(store.generations) != 1 || len(store.transitions) != 0 {
		t.Fatal("a denied command advanced the store")
	}
}

func TestStoreBashPromoteAllPromotesAChangeOutsideTheDeclaredScope(t *testing.T) {
	// default: denied as uncovered
	store, g0, authority := scopedE2EStore(t, AdmissionScope{})
	result, err := bashChange(t, store, g0, authority, "default", []string{"README.md"}, "plain.txt")
	if !errors.Is(err, ErrDeltaDenied) || result.DenyReason != DenyReasonUncoveredPath {
		t.Fatalf("default scope: %+v, %v", result, err)
	}
	// promote-all: promoted
	store, g0, authority = scopedE2EStore(t, AdmissionScope{Rule: AdmissionScopeRulePromoteAll})
	result, err = bashChange(t, store, g0, authority, "promote", []string{"README.md"}, "plain.txt")
	if err != nil || result.Outcome != "success" || result.Generation == nil || result.DenyReason != "" {
		t.Fatalf("promote-all: %+v, %v", result, err)
	}
	if got, err := os.ReadFile(filepath.Join(result.Generation.testPath(), "plain.txt")); err != nil || string(got) != "by bash\n" {
		t.Fatalf("the promoted generation lacks the change: %q %v", got, err)
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Reconstruct(bundle); err != nil {
		t.Fatalf("reconstruct a promote-all bundle: %v", err)
	}
	bundle.AdmissionScope = AdmissionScope{}
	if _, err := Reconstruct(bundle); err == nil {
		t.Fatal("a promote-all bundle reconstructed under the default scope")
	}
}

func TestOptionsRejectAnInvalidAdmissionScope(t *testing.T) {
	store, _, _, _ := newE2EStore(t)
	options := store.options
	if err := validateOptions(options); err != nil {
		t.Fatalf("baseline options: %v", err)
	}
	options.AdmissionScope = AdmissionScope{Rule: "nope"}
	if err := validateOptions(options); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("validateOptions accepted an unknown admission rule: %v", err)
	}
	options.AdmissionScope = AdmissionScope{ProtectedPaths: []string{"/abs"}}
	if err := validateOptions(options); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("validateOptions accepted a non-canonical protected path: %v", err)
	}
}
