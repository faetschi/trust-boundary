//go:build linux

package sessionrepo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/delta"
)

func productScope(writable, readOnly, protected []string) AdmissionScope {
	return AdmissionScope{Rule: AdmissionScopeRuleProduct, Writable: writable, ReadOnly: readOnly, Protected: protected}
}

func objectOf(typ delta.ObjectType, content, metadata string) delta.ObjectState {
	return delta.ObjectState{Exists: true, Type: typ, ContentDigest: content, MetadataIdentity: metadata}
}

func createdFile(path string) delta.ObjectChange {
	return delta.ObjectChange{Path: path, After: objectOf(delta.ObjectRegular, "sha256:c", "m")}
}

func deletedFile(path string) delta.ObjectChange {
	return delta.ObjectChange{Path: path, Before: objectOf(delta.ObjectRegular, "sha256:c", "m")}
}

func createdDirectory(path string) delta.ObjectChange {
	return delta.ObjectChange{Path: path, After: objectOf(delta.ObjectDirectory, "", "m")}
}

// metadataOnly changes only the policy-selected metadata identity of an existing file.
func metadataOnly(path string) delta.ObjectChange {
	return delta.ObjectChange{Path: path, Before: objectOf(delta.ObjectRegular, "sha256:c", "m1"), After: objectOf(delta.ObjectRegular, "sha256:c", "m2")}
}

func TestProductScopeDecide(t *testing.T) {
	scope := productScope(
		[]string{"docs/readme.md", "notes", "src"},
		[]string{"src/vendor"},
		[]string{"notes/private", "src/secret.txt"})
	for _, tc := range []struct {
		name    string
		scope   AdmissionScope
		request []string
		changes []delta.ObjectChange
		outcome string
		reason  string
	}{
		{"a writable subtree root itself", scope, nil, changesAt("src"), "allow", ""},
		{"below a writable subtree", scope, nil, changesAt("src/a/b.go"), "allow", ""},
		{"a sibling that shares the prefix is not covered", scope, nil, changesAt("srcx/a"), "deny", DenyReasonUncoveredPath},
		{"a path above a writable entry is not covered", scope, nil, changesAt("docs"), "deny", DenyReasonUncoveredPath},
		{"an exact file entry", scope, nil, changesAt("docs/readme.md"), "allow", ""},
		{"a name that extends an exact file entry", scope, nil, changesAt("docs/readme.md.bak"), "deny", DenyReasonUncoveredPath},
		{"another file beside an exact file entry", scope, nil, changesAt("docs/other.md"), "deny", DenyReasonUncoveredPath},
		{"protected file inside a writable subtree", scope, nil, changesAt("src/secret.txt"), "deny", DenyReasonProtectedPath},
		{"below a protected directory inside a writable subtree", scope, nil, changesAt("notes/private/x"), "deny", DenyReasonProtectedPath},
		{"a sibling that shares a protected prefix", scope, nil, changesAt("notes/privatex"), "allow", ""},
		{"read-only directory inside a writable subtree", scope, nil, changesAt("src/vendor"), "deny", DenyReasonProtectedPath},
		{"below a read-only directory", scope, nil, changesAt("src/vendor/lib.go"), "deny", DenyReasonProtectedPath},
		{"a sibling that shares a read-only prefix", scope, nil, changesAt("src/vendors"), "allow", ""},
		{"a new directory in a writable subtree", scope, nil, []delta.ObjectChange{createdDirectory("notes/new")}, "allow", ""},
		{"a new directory outside every writable entry", scope, nil, []delta.ObjectChange{createdDirectory("newdir")}, "deny", DenyReasonUncoveredPath},
		{"a delete in a writable subtree", scope, nil, []delta.ObjectChange{deletedFile("src/a.go")}, "allow", ""},
		{"a delete outside every writable entry", scope, nil, []delta.ObjectChange{deletedFile("other.txt")}, "deny", DenyReasonUncoveredPath},
		{"a delete of a protected file", scope, nil, []delta.ObjectChange{deletedFile("src/secret.txt")}, "deny", DenyReasonProtectedPath},
		{"a rename inside a writable subtree", scope, nil, []delta.ObjectChange{deletedFile("src/a.go"), createdFile("src/b.go")}, "allow", ""},
		{"a rename out of the writable tree", scope, nil, []delta.ObjectChange{createdFile("elsewhere/a.go"), deletedFile("src/a.go")}, "deny", DenyReasonUncoveredPath},
		{"a rename into the writable tree from outside", scope, nil, []delta.ObjectChange{deletedFile("elsewhere/a.go"), createdFile("src/a.go")}, "deny", DenyReasonUncoveredPath},
		{"a rename onto a protected path", scope, nil, []delta.ObjectChange{deletedFile("src/a.go"), createdFile("src/secret.txt")}, "deny", DenyReasonProtectedPath},
		{"a rename away from a protected path", scope, nil, []delta.ObjectChange{createdFile("src/moved.txt"), deletedFile("src/secret.txt")}, "deny", DenyReasonProtectedPath},
		{"a metadata-only change in a writable subtree", scope, nil, []delta.ObjectChange{metadataOnly("src/a.go")}, "allow", ""},
		{"a metadata-only change of a read-only path", scope, nil, []delta.ObjectChange{metadataOnly("src/vendor/lib.go")}, "deny", DenyReasonProtectedPath},
		{"a metadata-only change outside every writable entry", scope, nil, []delta.ObjectChange{metadataOnly("top.txt")}, "deny", DenyReasonUncoveredPath},
		{"one uncovered change among covered ones denies the candidate", scope, nil, changesAt("src/a.go", "src/b.go", "zzz"), "deny", DenyReasonUncoveredPath},
		{"protected is reported before uncovered", scope, nil, changesAt("other.txt", "src/secret.txt"), "deny", DenyReasonProtectedPath},
		{"the operation's own scope cannot widen the rule", scope, []string{"other.txt"}, changesAt("other.txt"), "deny", DenyReasonUncoveredPath},
		{"the operation's own scope cannot narrow the rule", scope, []string{}, changesAt("src/a.go"), "allow", ""},
		{"an absent operation scope does not matter", scope, nil, changesAt("src/a.go"), "allow", ""},
		{"no change is always admitted", scope, nil, nil, "allow", ""},
		{"an empty writable list denies everything", productScope(nil, nil, nil), nil, changesAt("a"), "deny", DenyReasonUncoveredPath},
		{"an empty writable list still admits no change", productScope(nil, nil, nil), nil, nil, "allow", ""},
		{"an empty writable list names a read-only path as protected", productScope(nil, []string{"a"}, nil), nil, changesAt("a"), "deny", DenyReasonProtectedPath},
	} {
		outcome, reason := tc.scope.Decide(tc.request, tc.changes)
		if outcome != tc.outcome || reason != tc.reason {
			t.Errorf("%s: got %s/%q, want %s/%q", tc.name, outcome, reason, tc.outcome, tc.reason)
		}
	}
}

func TestProductScopeValidate(t *testing.T) {
	tooMany := make([]string, 129)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("d%03d", i)
	}
	for name, scope := range map[string]AdmissionScope{
		"unsorted writable":                productScope([]string{"b", "a"}, nil, nil),
		"unsorted read-only":               productScope(nil, []string{"b", "a"}, nil),
		"unsorted protected":               productScope(nil, nil, []string{"b", "a"}),
		"duplicate in writable":            productScope([]string{"a", "a"}, nil, nil),
		"in writable and read-only":        productScope([]string{"a"}, []string{"a"}, nil),
		"in writable and protected":        productScope([]string{"a", "b"}, nil, []string{"b"}),
		"in read-only and protected":       productScope(nil, []string{"a"}, []string{"a"}),
		"more than 128 writable entries":   productScope(tooMany, nil, nil),
		"absolute path":                    productScope([]string{"/a"}, nil, nil),
		"dot-dot path":                     productScope([]string{"a/../b"}, nil, nil),
		"empty path":                       productScope([]string{""}, nil, nil),
		"trailing slash":                   productScope([]string{"a/"}, nil, nil),
		"exact-path protected paths":       {Rule: AdmissionScopeRuleProduct, ProtectedPaths: []string{"a"}},
		"product lists under exact path":   {Writable: []string{"a"}},
		"product lists under promote-all":  {Rule: AdmissionScopeRulePromoteAll, Protected: []string{"a"}},
		"read-only list under exact path":  {Rule: AdmissionScopeRuleExactPath, ReadOnly: []string{"a"}},
		"unknown rule beside product data": {Rule: "product-scope/v2", Writable: []string{"a"}},
	} {
		if err := scope.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for name, scope := range map[string]AdmissionScope{
		"empty product scope":      productScope(nil, nil, nil),
		"writable only":            productScope([]string{"a", "b/c"}, nil, nil),
		"nested across lists":      productScope([]string{"src"}, []string{"src/vendor"}, []string{"src/vendor/secret"}),
		"exactly 128 entries":      productScope(tooMany[:128], nil, nil),
		"empty but non-nil lists":  productScope([]string{}, []string{}, []string{}),
		"read-only and protected":  productScope(nil, []string{"a"}, []string{"b"}),
		"names with prefix in use": productScope([]string{"src", "srcx"}, nil, nil),
	} {
		if err := scope.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if productScope(nil, nil, nil).IsZero() {
		t.Error("a product scope is never the default scope")
	}
}

// The digests below were computed with the code before product-scope/v1 existed. They pin the research
// rules, so existing admission records and pins keep verifying.
func TestAdmissionPolicyDigestGoldenForTheResearchRules(t *testing.T) {
	policy, metadata := digestBytes([]byte("p")), digestBytes([]byte("m"))
	if policy != "sha256:148de9c5a7a44d19e56cd9ae1a554bf67847afb0c58f6e12fa29ac7ddfca9940" ||
		metadata != "sha256:62c66a7a5dd70c3146618063c344e531e6d4b59e379808443ce962b3abd63c5a" {
		t.Fatalf("golden inputs changed: %s %s", policy, metadata)
	}
	for name, tc := range map[string]struct {
		scope AdmissionScope
		want  string
	}{
		"zero":         {AdmissionScope{}, "sha256:47299da6c5fc9123a9e413f0efb4436520f4ddf783444483371a9e9c17a7fc12"},
		"default rule": {AdmissionScope{Rule: AdmissionScopeRuleExactPath}, "sha256:47299da6c5fc9123a9e413f0efb4436520f4ddf783444483371a9e9c17a7fc12"},
		"promote-all":  {AdmissionScope{Rule: AdmissionScopeRulePromoteAll}, "sha256:2e80df6c533698b562dd69264da1c40208e508012063d55ccd8bb74f9fb1dcdd"},
		"protected":    {AdmissionScope{ProtectedPaths: []string{"a", "b/c"}}, "sha256:bcd8926f342ff5463a0ab1063b3989100e1f3f50aac1e57b27051cf2012dc6dd"},
	} {
		got, err := AdmissionPolicyDigest(policy, metadata, tc.scope)
		if err != nil || got != tc.want {
			t.Errorf("%s: digest %q, %v; want the pre-product digest %q", name, got, err, tc.want)
		}
	}
}

func TestProductScopeDigestCommitsToTheRuleAndAllThreeLists(t *testing.T) {
	policy, metadata := digestBytes([]byte("p")), digestBytes([]byte("m"))
	seen := map[string]string{}
	for name, scope := range map[string]AdmissionScope{
		"default":               {},
		"promote-all":           {Rule: AdmissionScopeRulePromoteAll},
		"exact protected a":     {ProtectedPaths: []string{"a"}},
		"empty product":         productScope(nil, nil, nil),
		"writable a":            productScope([]string{"a"}, nil, nil),
		"read-only a":           productScope(nil, []string{"a"}, nil),
		"protected a":           productScope(nil, nil, []string{"a"}),
		"writable a, b":         productScope([]string{"a", "b"}, nil, nil),
		"writable a, read-only": productScope([]string{"a"}, []string{"b"}, nil),
		"writable a, protected": productScope([]string{"a"}, nil, []string{"b"}),
		"writable b, protected": productScope([]string{"b"}, nil, []string{"a"}),
		"all three":             productScope([]string{"a"}, []string{"b"}, []string{"c"}),
	} {
		digest, err := AdmissionPolicyDigest(policy, metadata, scope)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if other, dup := seen[digest]; dup {
			t.Errorf("%s shares an admission policy digest with %s", name, other)
		}
		seen[digest] = name
	}
	if got, _ := AdmissionPolicyDigest(policy, metadata, productScope([]string{}, []string{}, []string{})); got != mustDigest(t, policy, metadata, productScope(nil, nil, nil)) {
		t.Error("an empty list and an absent list are the same scope and must share a digest")
	}
	if _, err := AdmissionPolicyDigest(policy, metadata, productScope([]string{"b", "a"}, nil, nil)); err == nil {
		t.Fatal("an invalid product scope has no digest")
	}
}

func mustDigest(t *testing.T, policy, metadata string, scope AdmissionScope) string {
	t.Helper()
	digest, err := AdmissionPolicyDigest(policy, metadata, scope)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestOptionsRefuseAnInvalidProductScope(t *testing.T) {
	store, _, _, _ := newE2EStore(t)
	options := store.options
	options.AdmissionScope = productScope([]string{"a"}, []string{"a"}, nil)
	if err := validateOptions(options); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("a path in two lists: %v", err)
	}
	options.AdmissionScope = productScope([]string{"a"}, nil, nil)
	if err := validateOptions(options); err != nil {
		t.Fatalf("a valid product scope: %v", err)
	}
}

// productWrite performs one typed Write whose declared output scope is its own path, as every product
// turn does, and returns the result. changed lists the whole expected output tree.
func productWrite(t *testing.T, store *Store, input *Generation, authority *e2eReceiptAuthority, id, path string, content []byte,
	changed []writeFixtureFileSpec, generation string, sequence uint64) (MutationResult, error) {
	t.Helper()
	operation, decision := proposal(id), allow(id)
	request := OperationRequest{Tool: "write", Operation: operation, Decision: decision, InputGeneration: input.ID(),
		InputTreeDigest: input.TreeDigest(), ArgumentDigest: writeArgumentsDigestForTest(path, content), OutputScopePaths: []string{path}}
	authority.grant(request)
	authority.grantBinding(decision.ID, exactBinding(input.snapshot, expectedWriteSnapshot(t, generation, changed), request, sequence))
	return store.Write(context.Background(), input, operation, decision, path, content)
}

func withContent(files []writeFixtureFileSpec, path, content string) []writeFixtureFileSpec {
	changed := cloneWriteFixtureFiles(files)
	for i := range changed {
		if changed[i].path == path {
			changed[i].content = content
			return changed
		}
	}
	return append(changed, writeFixtureFileSpec{path: path, content: content, mode: 0o644})
}

func TestStoreProductScopeAdmitsWritableAndDeniesTheRest(t *testing.T) {
	scope := productScope([]string{"README.md", "build"}, nil, []string{"build/result.txt"})
	store, g0, authority := scopedE2EStore(t, scope)
	files := writeFixtureFiles()
	for _, tc := range []struct{ id, path, reason string }{
		{"outside", "plain.txt", DenyReasonUncoveredPath},
		{"protected", "build/result.txt", DenyReasonProtectedPath},
	} {
		content := []byte("tampered\n")
		// The operation declared exactly this path, so the exact-path rule would admit it: the product rule decides.
		result, err := productWrite(t, store, g0, authority, tc.id, tc.path, content, withContent(files, tc.path, "tampered\n"), "g1", 1)
		if !errors.Is(err, ErrDeltaDenied) || result.Outcome != "denied_after_effect" || result.Generation != nil ||
			result.Admission == nil || result.Admission.Outcome != "deny" || result.DenyReason != tc.reason {
			t.Fatalf("%s: result = %+v, err = %v", tc.path, result, err)
		}
		if len(store.generations) != 1 || len(store.transitions) != 0 {
			t.Fatalf("%s: a denied change advanced the store", tc.path)
		}
		if got, err := os.ReadFile(filepath.Join(g0.testPath(), filepath.FromSlash(tc.path))); err != nil || string(got) == "tampered\n" {
			t.Fatalf("%s: sealed input changed: %q %v", tc.path, got, err)
		}
	}

	result, err := productWrite(t, store, g0, authority, "readme", "README.md", []byte("readme updated\n"),
		withContent(files, "README.md", "readme updated\n"), "g1", 1)
	if err != nil || result.Outcome != "success" || result.Generation == nil || result.DenyReason != "" {
		t.Fatalf("a write to an exact writable file: %+v, %v", result, err)
	}
	g1 := result.Generation
	grown := withContent(withContent(files, "README.md", "readme updated\n"), "build/new.txt", "new\n")
	result, err = productWrite(t, store, g1, authority, "created", "build/new.txt", []byte("new\n"), grown, "g2", 2)
	if err != nil || result.Outcome != "success" || result.Generation == nil {
		t.Fatalf("a new file in a writable subtree: %+v, %v", result, err)
	}

	bundle, err := store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	if got := bundle.AdmissionScope; got.Rule != AdmissionScopeRuleProduct || len(got.Writable) != 2 || len(got.Protected) != 1 {
		t.Fatalf("the bundle must carry the product scope it was written under: %+v", got)
	}
	disposition, err := Reconstruct(bundle)
	if err != nil || disposition.Denials != 2 || disposition.Quarantined {
		t.Fatalf("reconstruct under the bundle scope: %+v, %v", disposition, err)
	}
	for name, other := range map[string]AdmissionScope{
		"the default scope":      {},
		"a wider product scope":  productScope([]string{"README.md", "build", "plain.txt"}, nil, []string{"build/result.txt"}),
		"without the protection": productScope([]string{"README.md", "build"}, nil, nil),
	} {
		forged := bundle
		forged.AdmissionScope = other
		if _, err := Reconstruct(forged); err == nil {
			t.Errorf("Reconstruct accepted records written under the product scope as if the scope were %s", name)
		}
	}
}

// A candidate delta lists the old path and the new path of a rename as two changes, so a rename across
// a scope boundary is refused whichever end is outside it.
func TestStoreProductScopeChecksBothEndsOfARename(t *testing.T) {
	renamed := []writeFixtureFileSpec{
		{path: "README.md", content: "baseline text\n", mode: 0o644},
		{path: "script.sh", content: "#!/bin/sh\necho baseline\n", mode: 0o755},
		{path: "build/plain.txt", content: "plain baseline\n", mode: 0o644},
		{path: "build/result.txt", content: "baseline result\n", mode: 0o644},
	}
	for _, tc := range []struct {
		name   string
		scope  AdmissionScope
		reason string
	}{
		{"the old path is outside the writable tree", productScope([]string{"build"}, nil, nil), DenyReasonUncoveredPath},
		{"the new path is outside the writable tree", productScope([]string{"plain.txt"}, nil, nil), DenyReasonUncoveredPath},
		{"the new path is read-only", productScope([]string{"build", "plain.txt"}, []string{"build/plain.txt"}, nil), DenyReasonProtectedPath},
		{"the old path is protected", productScope([]string{"build"}, nil, []string{"plain.txt"}), DenyReasonProtectedPath},
		{"both ends are writable", productScope([]string{"build", "plain.txt"}, nil, nil), ""},
	} {
		store, g0, authority := scopedE2EStore(t, tc.scope)
		op, decision := lease("rename"), allow("rename")
		viewID := reserveTestViewID(t, store)
		spec := CommandSpec{Executable: "/bin/true", DeclaredOutputPaths: []string{"build/plain.txt", "plain.txt"}}
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
			OutputScopePaths: spec.DeclaredOutputPaths}
		authority.grant(request)
		authority.grantBinding(decision.ID, exactBinding(g0.snapshot, expectedWriteSnapshot(t, "g1", renamed), request, 1))
		result, err := store.RunBash(context.Background(), g0, op, decision, spec,
			CommandRunnerFunc(func(_ context.Context, view *CommandView, _ CommandSpec) (CommandResult, CommandSettlement, error) {
				if err := os.Rename(filepath.Join(view.testPath(), "plain.txt"), filepath.Join(view.testPath(), "build", "plain.txt")); err != nil {
					return CommandResult{}, CommandSettlement{}, err
				}
				return CommandResult{ExitObserved: true}, CommandSettlement{
					LeaseID: op.LeaseID, ViewID: view.ID(), ProcessScopeEmpty: true, WritersStopped: true,
					MountDetached: true, ExitObserved: true, EvidenceClass: "synthetic-test-fixture",
				}, nil
			}))
		if tc.reason == "" {
			if err != nil || result.Outcome != "success" || result.Generation == nil || result.DenyReason != "" {
				t.Errorf("%s: %+v, %v", tc.name, result, err)
			}
			continue
		}
		if !errors.Is(err, ErrDeltaDenied) || result.Generation != nil || result.DenyReason != tc.reason {
			t.Errorf("%s: %+v, %v; want a denial with %s", tc.name, result, err, tc.reason)
		}
	}
}

func TestProductScopeDigestGolden(t *testing.T) {
	policy, metadata := digestBytes([]byte("p")), digestBytes([]byte("m"))
	got := mustDigest(t, policy, metadata, productScope([]string{"a"}, []string{"b"}, []string{"c"}))
	if want := "sha256:01ff7cd4f8fbf9f3d702e7a3858070cd37662a3bb083ac5ee3bc19354d57e6da"; got != want {
		t.Fatalf("product scope digest %s, want %s", got, want)
	}
}

// Creating a file leaves its parent directory's identity unchanged, so an exact-file writable entry
// admits the new file without listing the directory.
func TestStoreProductScopeAdmitsANewFileNamedExactly(t *testing.T) {
	store, g0, authority := scopedE2EStore(t, productScope([]string{"build/new.txt"}, nil, nil))
	files := writeFixtureFiles()
	result, err := productWrite(t, store, g0, authority, "exact", "build/new.txt", []byte("new\n"), withContent(files, "build/new.txt", "new\n"), "g1", 1)
	if err != nil || result.Outcome != "success" || result.Generation == nil {
		t.Fatalf("a new file named by an exact writable entry: %+v, %v", result, err)
	}
	result, err = productWrite(t, store, result.Generation, authority, "sibling", "build/sibling.txt", []byte("x\n"),
		withContent(withContent(files, "build/new.txt", "new\n"), "build/sibling.txt", "x\n"), "g2", 2)
	if !errors.Is(err, ErrDeltaDenied) || result.DenyReason != DenyReasonUncoveredPath {
		t.Fatalf("a sibling of an exact writable entry: %+v, %v", result, err)
	}
}

func noDeleteScope(writable, noDelete []string) AdmissionScope {
	scope := productScope(writable, nil, nil)
	scope.NoDelete = noDelete
	return scope
}

func TestProductScopeNoDeleteDecide(t *testing.T) {
	scope := noDeleteScope([]string{"docs", "src"}, []string{"src"})
	scope.ReadOnly = []string{"src/vendor"}
	for _, tc := range []struct {
		name    string
		scope   AdmissionScope
		changes []delta.ObjectChange
		outcome string
		reason  string
	}{
		{"a delete under no_delete", scope, []delta.ObjectChange{deletedFile("src/a.go")}, "deny", DenyReasonProtectedPath},
		{"a delete of the no_delete entry itself", scope, []delta.ObjectChange{deletedFile("src")}, "deny", DenyReasonProtectedPath},
		{"a modify under no_delete", scope, []delta.ObjectChange{metadataOnly("src/a.go")}, "allow", ""},
		{"a create under no_delete", scope, []delta.ObjectChange{createdFile("src/new.go")}, "allow", ""},
		{"a rename inside no_delete removes the old path", scope, []delta.ObjectChange{deletedFile("src/a.go"), createdFile("src/b.go")}, "deny", DenyReasonProtectedPath},
		{"a rename out of no_delete", scope, []delta.ObjectChange{deletedFile("src/a.go"), createdFile("docs/a.go")}, "deny", DenyReasonProtectedPath},
		{"a rename into no_delete from a deletable path", scope, []delta.ObjectChange{deletedFile("docs/a.go"), createdFile("src/a.go")}, "allow", ""},
		{"a delete in writable but not no_delete", scope, []delta.ObjectChange{deletedFile("docs/a.md")}, "allow", ""},
		{"a sibling that shares the no_delete prefix", noDeleteScope([]string{"srcx", "src"}, []string{"src"}), []delta.ObjectChange{deletedFile("srcx/a")}, "allow", ""},
		{"read-only is reported before no_delete", scope, []delta.ObjectChange{deletedFile("src/vendor/x")}, "deny", DenyReasonProtectedPath},
		{"uncovered is reported before no_delete", scope, []delta.ObjectChange{deletedFile("other"), deletedFile("src/a.go")}, "deny", DenyReasonUncoveredPath},
		{"removing a parent of a deeper no_delete entry", noDeleteScope([]string{"src"}, []string{"src/keep"}),
			[]delta.ObjectChange{deletedFile("src"), deletedFile("src/keep"), deletedFile("src/keep/a")}, "deny", DenyReasonProtectedPath},
		{"no_delete outside writable is harmless", noDeleteScope([]string{"docs"}, []string{"src"}), []delta.ObjectChange{deletedFile("docs/a.md")}, "allow", ""},
	} {
		outcome, reason := tc.scope.Decide(nil, tc.changes)
		if outcome != tc.outcome || reason != tc.reason {
			t.Errorf("%s: got %s/%q, want %s/%q", tc.name, outcome, reason, tc.outcome, tc.reason)
		}
	}
}

func TestProductScopeNoDeleteValidate(t *testing.T) {
	for name, scope := range map[string]AdmissionScope{
		"unsorted no_delete":       noDeleteScope([]string{"a", "b"}, []string{"b", "a"}),
		"non-canonical no_delete":  noDeleteScope([]string{"a"}, []string{"a/"}),
		"duplicate no_delete":      noDeleteScope([]string{"a"}, []string{"a", "a"}),
		"under exact path":         {NoDelete: []string{"a"}},
		"under promote-all":        {Rule: AdmissionScopeRulePromoteAll, NoDelete: []string{"a"}},
		"no_delete is not default": {Rule: AdmissionScopeRuleExactPath, NoDelete: []string{"a"}},
	} {
		if err := scope.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	tooMany := make([]string, 129)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("d%03d", i)
	}
	if err := noDeleteScope(nil, tooMany).Validate(); err == nil {
		t.Error("more than 128 no_delete entries accepted")
	}
	for name, scope := range map[string]AdmissionScope{
		"equal to a writable entry":  noDeleteScope([]string{"src"}, []string{"src"}),
		"inside a writable entry":    noDeleteScope([]string{"src"}, []string{"src/lib"}),
		"outside every writable one": noDeleteScope([]string{"src"}, []string{"docs"}),
	} {
		if err := scope.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if noDeleteScope(nil, []string{"a"}).IsZero() {
		t.Error("a product scope with no_delete is never the default scope")
	}
}

func TestProductScopeNoDeleteDigest(t *testing.T) {
	policy, metadata := digestBytes([]byte("p")), digestBytes([]byte("m"))
	plain := mustDigest(t, policy, metadata, productScope([]string{"src"}, []string{"docs"}, []string{"secret"}))
	// The payload exactly as it was before no_delete existed.
	legacy, err := json.Marshal(struct {
		SchemaVersion        string   `json:"schema_version"`
		PolicyDigest         string   `json:"policy_digest"`
		MetadataPolicyDigest string   `json:"metadata_policy_digest"`
		ScopeRule            string   `json:"scope_rule"`
		ProtectedPaths       []string `json:"protected_paths,omitempty"`
		Writable             []string `json:"writable,omitempty"`
		ReadOnly             []string `json:"read_only,omitempty"`
		Protected            []string `json:"protected,omitempty"`
	}{AdmissionSchemaVersion, policy, metadata, AdmissionScopeRuleProduct, nil, []string{"src"}, []string{"docs"}, []string{"secret"}})
	if err != nil {
		t.Fatal(err)
	}
	if plain != digestBytes(legacy) {
		t.Error("a product scope without no_delete must keep its pre-no_delete digest")
	}
	plain = mustDigest(t, policy, metadata, productScope([]string{"src"}, nil, nil))
	withNoDelete := mustDigest(t, policy, metadata, noDeleteScope([]string{"src"}, []string{"src"}))
	if withNoDelete == plain {
		t.Error("no_delete must change the admission policy digest")
	}
	if withNoDelete == mustDigest(t, policy, metadata, productScope([]string{"src"}, []string{"src"}, nil)) {
		t.Error("no_delete and read_only must not share a digest")
	}
	if got := cloneAdmissionScope(noDeleteScope([]string{"src"}, []string{"src"})); len(got.NoDelete) != 1 {
		t.Error("cloneAdmissionScope drops no_delete")
	}
}
