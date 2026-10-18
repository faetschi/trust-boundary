//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/runner"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

// corpusCaseWithDelta returns an adversarial case of the manifest that registers delta.
func corpusCaseWithDelta(t *testing.T, manifest evalcorpus.Manifest, delta string) string {
	t.Helper()
	for _, c := range manifest.Cases {
		if c.Kind == evalcorpus.KindAdversarial && c.Trial.EffectivePDelta() == delta {
			return c.CaseID
		}
	}
	t.Fatalf("no adversarial case registers %s", delta)
	return ""
}

func scriptedTestCorpus(t *testing.T, mutate func(*evalcorpus.Manifest)) (*productionCorpus, evalcorpus.Manifest) {
	t.Helper()
	manifest := corpusTestManifest(t)
	if mutate != nil {
		mutate(&manifest)
	}
	config, _ := installCorpusTestManifest(t, manifest)
	corpus, err := loadProductionCorpus(config)
	if err != nil {
		t.Fatal(err)
	}
	return corpus, manifest
}

func TestCorpusRoleMatrixOfThePPairRoles(t *testing.T) {
	for _, role := range []string{corpusRolePStaticReplay, corpusRolePAdaptiveReplay} {
		if !validCorpusRole(role) {
			t.Errorf("%s is not a valid role", role)
		}
	}
	corpus, manifest := scriptedTestCorpus(t, nil)
	for _, c := range manifest.Cases {
		for _, role := range []string{corpusRolePStaticReplay, corpusRolePAdaptiveReplay} {
			if got, want := corpus.roleAllowed(c.CaseID, role), c.Kind == evalcorpus.KindAdversarial; got != want {
				t.Errorf("%s on a %s case: allowed=%t, want %t", role, c.Kind, got, want)
			}
		}
	}
	if corpus.roleAllowed("not-a-case", corpusRolePStaticReplay) {
		t.Error("a role was allowed for an unknown case")
	}
}

func TestCorpusServesOnlyWhatThisBuildImplements(t *testing.T) {
	corpus, manifest := scriptedTestCorpus(t, nil)
	admitAll := corpusCaseWithDelta(t, manifest, evalcorpus.PDeltaAdmitAll)
	none := corpusCaseWithDelta(t, manifest, evalcorpus.PDeltaNone)
	for _, tc := range []struct {
		caseID, role string
		want         bool
	}{
		{admitAll, corpusRolePStaticReplay, true}, {none, corpusRolePStaticReplay, true},
		{admitAll, corpusRoleStaticReplay, true}, {admitAll, corpusRoleTrajectory, true},
		{admitAll, corpusRolePAdaptiveReplay, false}, // this manifest carries no pivots and pins no attacker
		{admitAll, corpusRoleAdaptiveReplay, false},  // C adaptive must never fall back to the model
	} {
		if got := corpus.roleServable(tc.caseID, tc.role); got != tc.want {
			t.Errorf("%s/%s servable=%t, want %t", tc.caseID, tc.role, got, tc.want)
		}
	}
	// net-sink/v1 and no-reap/v1 are served for the P half (the derived command-cell templates), and the C
	// static replay of the same case stays on the signed base cell. A missing script is refused for the
	// roles that need one.
	for _, delta := range []string{evalcorpus.PDeltaNetSink, evalcorpus.PDeltaNoReap} {
		c2, m2 := scriptedTestCorpus(t, func(m *evalcorpus.Manifest) {
			for i := range m.Cases {
				if m.Cases[i].CaseID == admitAll {
					m.Cases[i].Trial.PDelta = delta
					m.Cases[i].Adversarial.CalibrationPayloadDigest, _ = evalcorpus.CalibrationPayloadDigest(m.Cases[i].Trial.Script, delta)
				}
			}
		})
		_ = m2
		if !c2.roleServable(admitAll, corpusRolePStaticReplay) {
			t.Errorf("p-static-replay is not served under the implemented delta %s", delta)
		}
		if !c2.roleServable(admitAll, corpusRoleStaticReplay) {
			t.Errorf("the C static replay of a case with delta %s must still be served", delta)
		}
	}
	c3, _ := scriptedTestCorpus(t, func(m *evalcorpus.Manifest) {
		for i := range m.Cases {
			if m.Cases[i].CaseID == none {
				m.Cases[i].Trial.Script, m.Cases[i].Trial.PDelta, m.Cases[i].Trial.PDeltaJustification = nil, "", ""
			}
		}
	})
	for _, role := range []string{corpusRoleStaticReplay, corpusRolePStaticReplay} {
		if c3.roleServable(none, role) {
			t.Errorf("%s is served for a case without a frozen script", role)
		}
	}
}

func TestCorpusServiceRefusesWhatItCannotServeWithoutConsumingTheRequest(t *testing.T) {
	h := newCorpusServiceHarness(t)
	admitAll := corpusCaseWithDelta(t, h.manifest, evalcorpus.PDeltaAdmitAll)
	compliant := corpusCaseForRole(t, h.manifest, corpusRoleCompliant)
	for name, tc := range map[string]struct {
		request piServiceRequest
		code    string
	}{
		"p-adaptive-replay (no pivots, no attacker pin)": {corpusRequest("u1", admitAll, corpusRolePAdaptiveReplay, 1), "role-unavailable"},
		"adaptive-replay (no pivots, no attacker pin)":   {corpusRequest("u4", admitAll, corpusRoleAdaptiveReplay, 1), "role-unavailable"},
		"p-static-replay on a compliant case":            {corpusRequest("u2", compliant, corpusRolePStaticReplay, 1), "role-kind-mismatch"},
	} {
		if response := roundTripPiServiceRequest(t, h.server.path, tc.request); response.Status != "refused" || response.ErrorCode != tc.code {
			t.Errorf("%s: %+v", name, response)
		}
	}
	if h.server.calls.Load() != 0 {
		t.Fatal("a refused role reached the runner")
	}
	if response := roundTripPiServiceRequest(t, h.server.path, corpusRequest("u3", admitAll, corpusRolePStaticReplay, 1)); response.Status != "completed" {
		t.Fatalf("p-static-replay of a case with admit-all/v1: %+v", response)
	}
}

func TestBindingSelectsProviderDeltaAndAdmissionRuleFromTheRoleAndManifestOnly(t *testing.T) {
	corpus, manifest := scriptedTestCorpus(t, nil)
	admitAll := corpusCaseWithDelta(t, manifest, evalcorpus.PDeltaAdmitAll)
	none := corpusCaseWithDelta(t, manifest, evalcorpus.PDeltaNone)
	protected := sessionrepo.AdmissionScope{ProtectedPaths: []string{"protected", "secret.txt"}}
	for _, tc := range []struct {
		caseID, role string
		mode, delta  string
		scope        sessionrepo.AdmissionScope
	}{
		{admitAll, corpusRoleTrajectory, "model", "none", protected},
		{admitAll, corpusRoleStaticReplay, "scripted", "none", protected}, // C: the control package, never the relaxation
		{admitAll, corpusRoleVariance1, "model", "none", protected},
		{admitAll, corpusRolePStaticReplay, "scripted", "admit-all/v1", sessionrepo.AdmissionScope{Rule: sessionrepo.AdmissionScopeRulePromoteAll}},
		{none, corpusRolePStaticReplay, "scripted", "none", protected}, // P under no delta keeps the full control package
	} {
		if tc.role == corpusRoleVariance1 && !corpus.roleAllowed(tc.caseID, tc.role) {
			continue // not every case is in the variance subset
		}
		b, err := corpus.bindingFor(tc.caseID, tc.role)
		if err != nil {
			t.Fatalf("%s/%s: %v", tc.caseID, tc.role, err)
		}
		if b.providerMode() != tc.mode || b.delta != tc.delta || b.scope.Rule != tc.scope.Rule ||
			strings.Join(b.scope.ProtectedPaths, ",") != strings.Join(tc.scope.ProtectedPaths, ",") {
			t.Errorf("%s/%s: mode=%s delta=%s scope=%+v", tc.caseID, tc.role, b.providerMode(), b.delta, b.scope)
		}
		if (b.scriptedFactory() != nil) != (tc.mode == "scripted") {
			t.Errorf("%s/%s: factory presence does not follow the role", tc.caseID, tc.role)
		}
		if (b.scriptDigest() != "") != (tc.mode == "scripted") {
			t.Errorf("%s/%s: script digest %q", tc.caseID, tc.role, b.scriptDigest())
		}
	}
	// Control and P digests differ, and the default policy digest is the one that existed before scopes.
	c, _ := corpus.bindingFor(admitAll, corpusRoleStaticReplay)
	p, _ := corpus.bindingFor(admitAll, corpusRolePStaticReplay)
	policy, metadata := corpusTestDigest("policy"), corpusTestDigest("metadata")
	cd, err1 := c.admissionPolicyDigest(policy, metadata)
	pd, err2 := p.admissionPolicyDigest(policy, metadata)
	if err1 != nil || err2 != nil || cd == pd {
		t.Fatalf("admission policy digests: C %q (%v), P %q (%v)", cd, err1, pd, err2)
	}
	compliantCase := corpusCaseForRole(t, manifest, corpusRoleCompliant)
	cb, err := corpus.bindingFor(compliantCase, corpusRoleCompliant)
	if err != nil || !cb.scope.IsZero() {
		t.Fatalf("a compliant case has no protected paths: %+v %v", cb.scope, err)
	}
	if _, err := corpus.bindingFor(admitAll, corpusRolePAdaptiveReplay); err == nil {
		t.Fatal("a binding was derived for a role the build cannot serve")
	}
}

func TestAuthorityFactoryIsSelectedByTheBoundRoleOnly(t *testing.T) {
	corpus, manifest := scriptedTestCorpus(t, nil)
	admitAll := corpusCaseWithDelta(t, manifest, evalcorpus.PDeltaAdmitAll)
	newAuthority := func() *SessionOperationAuthority {
		dir := privateGovernedTestRoot(t)
		storeJ, err := audit.Open(filepath.Join(dir, "store.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		providerJ, err := audit.Open(filepath.Join(dir, "provider.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = storeJ.Close(); _ = providerJ.Close() })
		policy, err := productionGatePolicy()
		if err != nil {
			t.Fatal(err)
		}
		a, err := NewSessionOperationAuthority(storeJ, providerJ, "wf", "conv", "profile", policy,
			corpusTestDigest("sp"), corpusTestDigest("mp"), nil, []string{})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	for _, tc := range []struct {
		role     string
		scripted bool
	}{{corpusRoleTrajectory, false}, {corpusRoleStaticReplay, true}, {corpusRolePStaticReplay, true}, {corpusRoleCompliant, false}} {
		caseID := admitAll
		if tc.role == corpusRoleCompliant {
			caseID = corpusCaseForRole(t, manifest, corpusRoleCompliant)
		}
		binding, err := corpus.bindingFor(caseID, tc.role)
		if err != nil {
			t.Fatal(err)
		}
		a := newAuthority()
		if err := a.BindCorpusBinding(binding); err != nil {
			t.Fatal(err)
		}
		if (a.providerFactory != nil) != tc.scripted {
			t.Errorf("%s: factory installed=%t, want %t", tc.role, a.providerFactory != nil, tc.scripted)
		}
		if a.admissionScope.Rule != binding.scope.Rule || len(a.admissionScope.ProtectedPaths) != len(binding.scope.ProtectedPaths) {
			t.Errorf("%s: authority scope %+v, binding %+v", tc.role, a.admissionScope, binding.scope)
		}
	}
	// A binding cannot be applied after the Store or the conversation exists, nor can it be replaced.
	binding, _ := corpus.bindingFor(admitAll, corpusRoleTrajectory)
	a := newAuthority()
	a.conversation = &providerbridge.Conversation{}
	if err := a.BindCorpusBinding(binding); err == nil {
		t.Fatal("a binding was applied after the provider conversation existed")
	}
}

func TestScriptedFactoryReplaysTheManifestScriptThroughTheRealConversation(t *testing.T) {
	corpus, manifest := scriptedTestCorpus(t, nil)
	caseID := corpusCaseWithDelta(t, manifest, evalcorpus.PDeltaAdmitAll)
	binding, err := corpus.bindingFor(caseID, corpusRolePStaticReplay)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := audit.Open(filepath.Join(privateGovernedTestRoot(t), "provider.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	conversation, err := binding.scriptedFactory()(providerbridge.Config{
		ConversationID: "conv", WorkflowID: "wf", ProfileID: "profile", InitialGenerationID: "g0", InitialTreeDigest: "tree-g0",
		SystemPrompt: "system", DeveloperPrompt: "developer",
	}, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt(caseID, "Host-approved prompt of "+caseID); err != nil {
		t.Fatal(err)
	}
	turn, err := conversation.NextTurn(context.Background())
	if err != nil || turn.ToolCall == nil || turn.ToolCall.Name != "write" ||
		string(turn.ToolCall.Arguments) != `{"content":"x","path":"note.txt"}` {
		t.Fatalf("scripted turn = %+v, %v", turn.ToolCall, err)
	}
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, r := range trace.Records {
		kinds[r.Event.Kind]++
	}
	if kinds["provider_exchange"] != 1 || kinds["provider_user_admitted"] != 1 {
		t.Fatalf("the scripted turn did not go through the journaled exchange path: %v", kinds)
	}
	if digest := binding.scriptDigest(); !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 {
		t.Fatalf("script digest %q", digest)
	}
}

func TestIntervalRefOfThePRolesNamesConfigurationP(t *testing.T) {
	const caseID = "case-1"
	for role, want := range map[string]string{
		corpusRolePStaticReplay: caseID + "|static-replay|P|0#1", corpusRolePAdaptiveReplay: caseID + "|adaptive-replay|P|0#1",
		corpusRoleStaticReplay: caseID + "|static-replay|C|0#1", corpusRoleVariance2: caseID + "|variance-repeat|C|2#1",
	} {
		ref, ok := corpusIntervalRef(corpusAdmissionID(caseID, role, 1), caseID)
		if !ok || ref != want {
			t.Errorf("%s: %q ok=%v, want %q", role, ref, ok, want)
		}
	}
	step := runner.Step{CaseID: caseID, Configuration: runner.CfgP, Role: runner.RoleStaticReplay, Attempt: 1}
	if ref, _ := corpusIntervalRef(corpusAdmissionID(caseID, corpusRolePStaticReplay, 1), caseID); ref != runner.IntervalRef(step) {
		t.Fatalf("service %q differs from the runner %q", ref, runner.IntervalRef(step))
	}
}

func TestAScriptedWorkflowIsNeverRecovered(t *testing.T) {
	corpus, manifest := scriptedTestCorpus(t, nil)
	caseID := corpusCaseWithDelta(t, manifest, evalcorpus.PDeltaAdmitAll)
	catalog := &piServiceWorkflowCatalog{corpus: corpus}
	entry := func(role string) piServiceWorkflowEntry {
		return piServiceWorkflowEntry{HostAdmissionID: corpusAdmissionID(caseID, role, 1), TaskID: caseID, CorpusManifestDigest: corpus.digest}
	}
	for _, role := range []string{corpusRoleStaticReplay, corpusRolePStaticReplay} {
		admission := &productionWorkflowAdmission{Mode: "resume", Catalog: catalog, Entry: entry(role)}
		if err := scriptedRecoveryRefusal(admission); !errors.Is(err, ErrScriptedRoleNotRecoverable) {
			t.Errorf("%s: recovery refusal = %v", role, err)
		}
		if _, err := bindProductionPiRecoverySession(context.Background(), productionServeConfig{}, piruntime.HostProfile{}, piruntime.D09CellTemplate{},
			productionOperatorArtifacts{}, admission); !errors.Is(err, ErrScriptedRoleNotRecoverable) {
			t.Errorf("%s: the recovery binder did not refuse first: %v", role, err)
		}
	}
	for _, role := range []string{corpusRoleTrajectory, corpusRoleCompliant, corpusRoleVariance1} {
		if err := scriptedRecoveryRefusal(&productionWorkflowAdmission{Mode: "resume", Catalog: catalog, Entry: entry(role)}); err != nil &&
			errors.Is(err, ErrScriptedRoleNotRecoverable) {
			t.Errorf("%s: a model role was refused as scripted", role)
		}
	}
	if err := scriptedRecoveryRefusal(&productionWorkflowAdmission{Mode: "resume", Catalog: catalog,
		Entry: piServiceWorkflowEntry{HostAdmissionID: "single-task"}}); err != nil {
		t.Errorf("a non-corpus admission was refused: %v", err)
	}
}

func TestReceiptCarriesProviderModeScriptDeltaPolicyAndProposals(t *testing.T) {
	corpus, manifest := scriptedTestCorpus(t, nil)
	caseID := corpusCaseWithDelta(t, manifest, evalcorpus.PDeltaAdmitAll)
	binding, _ := corpus.bindingFor(caseID, corpusRolePStaticReplay)
	journal, err := audit.Open(filepath.Join(privateGovernedTestRoot(t), "provider.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	for _, d := range []gate.Decision{
		{Verdict: gate.Allow, ReasonCode: "policy_rule_allow", Tool: "write", ToolCallID: "c1", Sequence: 1},
		{Verdict: gate.Deny, ReasonCode: "policy_rule_deny", Tool: "bash", ToolCallID: "c2", Sequence: 2},
	} {
		data, _ := json.Marshal(d)
		if _, err := journal.Append(audit.Event{Kind: "provider_gate_decision", ID: "decision", Data: data}); err != nil {
			t.Fatal(err)
		}
	}
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	var receipt productionPiReceipt
	policy, metadata := corpusTestDigest("sp"), corpusTestDigest("mp")
	if err := applyCorpusReceipt(&receipt, &binding, false, policy, metadata, trace, nil); err != nil {
		t.Fatal(err)
	}
	wantPolicy, _ := binding.admissionPolicyDigest(policy, metadata)
	if receipt.ProviderMode != "scripted" || receipt.Delta != "admit-all/v1" || receipt.ScriptDigest != binding.scriptDigest() ||
		receipt.AdmissionPolicyDigest != wantPolicy || len(receipt.Proposals) != 2 ||
		receipt.Proposals[1] != (productionProposalFact{Tool: "bash", Verdict: "DENY", ReasonCode: "policy_rule_deny"}) {
		t.Fatalf("receipt = %+v", receipt)
	}
	encoded, _ := json.Marshal(receipt)
	for _, member := range []string{`"provider_mode":"scripted"`, `"script_digest":"sha256:`, `"delta":"admit-all/v1"`,
		`"admission_policy_digest":"sha256:`, `"proposals":[{"tool":"write","verdict":"ALLOW","reason_code":"policy_rule_allow"}`} {
		if !strings.Contains(string(encoded), member) {
			t.Errorf("receipt JSON lacks %s: %s", member, encoded)
		}
	}
	// The receipt is what the runner parses: the same members round-trip into runner.ReceiptFacts.
	var facts runner.ReceiptFacts
	if err := json.Unmarshal(encoded, &facts); err != nil || facts.ProviderMode != "scripted" || facts.Delta != "admit-all/v1" ||
		facts.ScriptDigest != receipt.ScriptDigest || facts.AdmissionPolicyDigest != receipt.AdmissionPolicyDigest || len(facts.Proposals) != 2 {
		t.Fatalf("runner facts = %+v, %v", facts, err)
	}
	// A C model run reports delta none and the model provider; a model run without a binding says only "model".
	cbinding, _ := corpus.bindingFor(caseID, corpusRoleTrajectory)
	var c productionPiReceipt
	if err := applyCorpusReceipt(&c, &cbinding, true, policy, metadata, trace, nil); err != nil || c.ProviderMode != "model" || c.Delta != "none" || c.ScriptDigest != "" {
		t.Fatalf("C receipt = %+v, %v", c, err)
	}
	var plain productionPiReceipt
	if err := applyCorpusReceipt(&plain, nil, true, policy, metadata, trace, nil); err != nil || plain.ProviderMode != "model" || plain.Delta != "" {
		t.Fatalf("receipt without a binding = %+v, %v", plain, err)
	}
}

// durableDenialFixture is the write fixture of the durable integration test under a protected-path scope.
func TestDurableExecutorReportsAnAdmissionDenialWithItsReasonAndPromotesNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		scope sessionrepo.AdmissionScope
		deny  bool
	}{
		{"protected path", sessionrepo.AdmissionScope{ProtectedPaths: []string{"task.txt"}}, true},
		{"promote-all does not protect", sessionrepo.AdmissionScope{Rule: sessionrepo.AdmissionScopeRulePromoteAll}, false},
		{"another protected path", sessionrepo.AdmissionScope{ProtectedPaths: []string{"build"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := durablePrivateBase(t)
			storePath, auditDir := filepath.Join(base, "repository"), filepath.Join(base, "audit")
			durableMkdir(t, storePath, 0o700)
			durableMkdir(t, auditDir, 0o700)
			sourcePath := filepath.Join(base, "source")
			durableMkdir(t, sourcePath, 0o700)
			durableWriteFile(t, filepath.Join(sourcePath, "task.txt"), "baseline\n", 0o644)
			durableMkdir(t, filepath.Join(sourcePath, "build"), 0o755)
			durableWriteFile(t, filepath.Join(sourcePath, "build", "result.txt"), "baseline result\n", 0o644)
			journal, err := audit.Open(filepath.Join(auditDir, "journal.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			authority := &durableReceiptAuthority{callIssuer: durableCallIssuer, callID: durableCallID,
				policyDigest: durablePolicyDigest, metadataPolicyDigest: durableMetadataDigest}
			store, err := sessionrepo.Create(durableOpenRoot(t, storePath), sessionrepo.Options{
				PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest, Limits: workspace.DefaultLimits(),
				XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: durableXattrDigest, Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted},
				Journal:         journal, AuthorizeOperation: authority.authorize, VerifyDecision: authority.verifyDecision,
				VerifySettlement: func(sessionrepo.CommandSettlement, string, string) error { return errors.New("no command settlement") },
				AdmissionScope:   tc.scope,
			})
			if err != nil {
				_ = journal.Close()
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close(); _ = journal.Close() })
			g0, err := store.Seed(durableOpenRoot(t, sourcePath), sessionrepo.RootAttestation{Quiescent: true})
			if err != nil {
				t.Fatal(err)
			}
			authority.setBinding(g0.TreeDigest(), durableExpectedSnapshot(t).TreeDigest)
			executor, err := NewDurableExecutor(DurableExecutorConfig{Store: store, Tip: g0, CallIssuer: durableCallIssuer,
				PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest})
			if err != nil {
				t.Fatal(err)
			}
			proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: durableCallID, Tool: "write",
				Arguments: json.RawMessage(`{"path":"` + durableWritePath + `","content":"` + durableWriteContent + `"}`)}
			digest, err := protocol.CanonicalArgumentsDigest(proposal.Tool, proposal.Arguments)
			if err != nil {
				t.Fatal(err)
			}
			decision := gate.Decision{Verdict: gate.Allow, ReasonCode: "policy_rule_allow", PolicyDigest: "tbound-policy/v1:sha256:" + strings.Repeat("4", 64),
				CanonicalArgumentsDigest: digest, ToolCallID: durableCallID, Tool: "write", Sequence: 1,
				ResponseID: &correlation.Identifier{Issuer: durableResponseIssuer, Opaque: durableResponseID}}
			output, err := executor.Execute(context.Background(), proposal, decision)
			if !tc.deny {
				if err != nil || len(output) == 0 {
					t.Fatalf("an admitted write: %v", err)
				}
				return
			}
			var denied *protocol.AdmissionDeniedError
			if !errors.As(err, &denied) || denied.ReasonCode != sessionrepo.DenyReasonProtectedPath || denied.Tool != "write" ||
				denied.EffectID == "" || !errors.Is(err, sessionrepo.ErrDeltaDenied) {
				t.Fatalf("Execute = %q, %v; want the protected-path denial", output, err)
			}
			if tip := executor.Tip(); tip == nil || tip.ID() != "g0" {
				t.Fatalf("a denied write advanced the tip: %+v", tip)
			}
			if got, err := os.ReadFile(filepath.Join(sourcePath, "task.txt")); err != nil || string(got) != "baseline\n" {
				t.Fatalf("source changed: %q %v", got, err)
			}
		})
	}
}

// A client request names a case, a role and an attempt; it has no member that could select a provider,
// a script, a delta or an admission rule, and the service decodes requests strictly.
func TestClientRequestCannotSelectTheProviderScriptOrDelta(t *testing.T) {
	typ := reflect.TypeOf(piServiceRequest{})
	var members []string
	for i := 0; i < typ.NumField(); i++ {
		members = append(members, strings.Split(typ.Field(i).Tag.Get("json"), ",")[0])
	}
	want := []string{"request_id", "task_id", "prompt", "bash_output_paths", "attempt", "role"}
	if !reflect.DeepEqual(members, want) {
		t.Fatalf("piServiceRequest members = %v, want %v: a new member must not select the provider", members, want)
	}
	for _, member := range []string{"provider_mode", "script", "p_delta", "scripted"} {
		decoder := json.NewDecoder(strings.NewReader(`{"request_id":"r","task_id":"c","role":"trajectory","attempt":1,"` + member + `":"scripted"}`))
		decoder.DisallowUnknownFields()
		var request piServiceRequest
		if err := decoder.Decode(&request); err == nil {
			t.Errorf("a request member %q was accepted", member)
		}
	}
}

// The host (tbound-run) computes the expected script and admission policy digests from the manifest
// with the portable evalcorpus functions; they must equal what the service puts in its receipt.
func TestHostExpectedDigestsEqualTheServiceReceiptDigests(t *testing.T) {
	corpus, manifest := scriptedTestCorpus(t, nil)
	policy, metadata := corpusTestDigest("sp"), corpusTestDigest("mp")
	for _, caseID := range []string{corpusCaseWithDelta(t, manifest, evalcorpus.PDeltaAdmitAll), corpusCaseWithDelta(t, manifest, evalcorpus.PDeltaNone)} {
		trial := corpus.trials[caseID]
		for _, role := range []string{corpusRoleStaticReplay, corpusRolePStaticReplay} {
			binding, err := corpus.bindingFor(caseID, role)
			if err != nil {
				t.Fatal(err)
			}
			want, err := binding.admissionPolicyDigest(policy, metadata)
			if err != nil {
				t.Fatal(err)
			}
			promoteAll := role == corpusRolePStaticReplay && trial.EffectivePDelta() == evalcorpus.PDeltaAdmitAll
			got, err := evalcorpus.AdmissionPolicyDigest(policy, metadata, promoteAll, trial.ProtectedPaths)
			if err != nil || got != want {
				t.Errorf("%s/%s: host %q (%v), service %q", caseID, role, got, err, want)
			}
			if digest, err := evalcorpus.ScriptDigest(trial.Script); err != nil || digest != binding.scriptDigest() {
				t.Errorf("%s/%s: host script digest %q (%v), service %q", caseID, role, digest, err, binding.scriptDigest())
			}
		}
	}
}
