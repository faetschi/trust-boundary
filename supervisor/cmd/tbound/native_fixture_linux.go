//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/ipc"
	"tbound/supervisor/internal/publication"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflow"
	"tbound/supervisor/internal/workspace"
)

const (
	nativeFixtureCallIssuer     = "fixture/native-pi-call/v1"
	nativeFixtureResponseIssuer = "fixture/native-pi-response/v1"
	nativeFixturePolicyDigest   = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	nativeFixtureMetadataDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	nativeFixtureXattrDigest    = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
)

// runNativeFixture is intentionally explicit and non-claim-bearing. It uses
// the real supervised IPC loop, DurableExecutor, session repository, and
// publication finalizer, but does not launch Pi, contact a provider, or assert
// host containment. It exists to exercise the native host composition seam
// without making --pi silently fall back to a direct Pi process.
func runNativeFixture(ctx context.Context, output io.Writer) error {
	if ctx == nil {
		return errors.New("native fixture context is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	base, err := os.MkdirTemp("", "tbound-native-fixture-")
	if err != nil {
		return fmt.Errorf("create native fixture root: %w", err)
	}
	defer os.RemoveAll(base)
	if err := os.Chmod(base, 0o700); err != nil {
		return err
	}
	for _, name := range []string{"source", "session", "session-audit", "live", "publication-state"} {
		if err := os.Mkdir(filepath.Join(base, name), 0o700); err != nil {
			return err
		}
	}
	if err := writeNativeFixtureWorkspace(filepath.Join(base, "source"), "baseline\n"); err != nil {
		return err
	}
	if err := writeNativeFixtureWorkspace(filepath.Join(base, "live"), "baseline\n"); err != nil {
		return err
	}

	storeJournal, err := audit.Open(filepath.Join(base, "session-audit", "journal.jsonl"))
	if err != nil {
		return fmt.Errorf("open native fixture session journal: %w", err)
	}
	defer storeJournal.Close()
	publicationJournal, err := audit.Open(filepath.Join(base, "publication-state", "journal.jsonl"))
	if err != nil {
		return fmt.Errorf("open native fixture publication journal: %w", err)
	}
	defer publicationJournal.Close()

	auth := nativeFixtureAuthority{}
	storeRoot, err := nativeFixtureOpenRoot(filepath.Join(base, "session"))
	if err != nil {
		return err
	}
	store, err := sessionrepo.Create(storeRoot, sessionrepo.Options{
		PolicyDigest: nativeFixturePolicyDigest, MetadataPolicyDigest: nativeFixtureMetadataDigest,
		Limits:          workspace.DefaultLimits(),
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: nativeFixtureXattrDigest, Complete: true},
		Journal:         storeJournal, AuthorizeOperation: auth.authorize,
		VerifyDecision: auth.verifyDecision, VerifySettlement: auth.verifySettlement,
	})
	_ = storeRoot.Close()
	if err != nil {
		return fmt.Errorf("create native fixture session repository: %w", err)
	}
	defer store.Close()
	source, err := nativeFixtureOpenRoot(filepath.Join(base, "source"))
	if err != nil {
		return err
	}
	baseline, err := store.Seed(source, sessionrepo.RootAttestation{Quiescent: true})
	_ = source.Close()
	if err != nil {
		return fmt.Errorf("seed native fixture generation: %w", err)
	}
	executor, err := NewDurableExecutor(DurableExecutorConfig{
		Store: store, Tip: baseline, CallIssuer: nativeFixtureCallIssuer,
		PolicyDigest: nativeFixturePolicyDigest, MetadataPolicyDigest: nativeFixtureMetadataDigest,
	})
	if err != nil {
		return fmt.Errorf("construct native fixture durable executor: %w", err)
	}
	recorder, err := NewAuditDecisionRecorder(storeJournal)
	if err != nil {
		return err
	}

	calls := []nativeFixtureCall{
		{responseID: "native-response-read-1", callID: "native-call-read-1", tool: "read", generation: "g0", outputGeneration: "g0", expectedContent: "baseline\n", arguments: json.RawMessage(`{"path":"task.txt"}`)},
		{responseID: "native-response-write-1", callID: "native-call-write-1", tool: "write", generation: "g0", outputGeneration: "g1", expectedContent: "native-fixture-final\n", expectedTransition: "transition-000001", arguments: json.RawMessage(`{"path":"task.txt","content":"native-fixture-final\n"}`)},
		{responseID: "native-response-read-2", callID: "native-call-read-2", tool: "read", generation: "g1", outputGeneration: "g1", expectedContent: "native-fixture-final\n", arguments: json.RawMessage(`{"path":"task.txt"}`)},
	}
	stream, err := protocol.NewStream(nativeFixtureCallIssuer, nativeFixtureResponseIssuer)
	if err != nil {
		return err
	}
	for sequence, call := range calls {
		digest, digestErr := protocol.CanonicalArgumentsDigest(call.tool, call.arguments)
		if digestErr != nil {
			return digestErr
		}
		decision := stream.Capture(protocol.TrustedCapture{
			ResponseID: correlation.Identifier{Issuer: nativeFixtureResponseIssuer, Opaque: call.responseID},
			ToolCallID: correlation.Identifier{Issuer: nativeFixtureCallIssuer, Opaque: call.callID},
			ToolName:   call.tool, RawArguments: append([]byte(nil), call.arguments...),
			CanonicalizationProfile: protocol.CanonicalizationProfile, CanonicalArgumentsDigest: digest,
			Sequence: uint64(sequence + 1), Generation: call.generation,
		})
		if !decision.Accepted {
			return fmt.Errorf("register independent fixture provider capture %d: %s", sequence+1, decision.ReasonCode)
		}
	}

	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{
		{Tool: "read", Effect: gate.EffectAllow}, {Tool: "write", Effect: gate.EffectAllow},
	}})
	if err != nil {
		return err
	}
	client, server, err := ipc.NewPipe(nativeFixtureBindingToken)
	if err != nil {
		return err
	}
	defer client.Close()
	defer server.Close()

	publicationState, err := nativeFixtureOpenRoot(filepath.Join(base, "publication-state"))
	if err != nil {
		return err
	}
	defer publicationState.Close()
	liveRoot, err := nativeFixtureOpenRoot(filepath.Join(base, "live"))
	if err != nil {
		return err
	}
	defer liveRoot.Close()
	workspaceOptions := workspace.Options{Generation: "g0", MetadataPolicyDigest: nativeFixtureMetadataDigest,
		Limits: workspace.DefaultLimits(), QuiescentRoot: true,
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: nativeFixtureXattrDigest, Complete: true}}
	ledger, err := workflow.NewRootLedger(publicationJournal)
	if err != nil {
		return err
	}
	openPublication := func() (*publication.Repository, error) {
		return publication.Acquire(publication.Options{WorkflowID: "native-fixture-workflow", OwnerEpoch: 1,
			StateRoot: publicationState, Journal: publicationJournal, Workspace: workspaceOptions})
	}
	finalizerOptions := workflow.FinalizerOptions{
		WorkflowID: "native-fixture-workflow", OwnerEpoch: 1, PublicationID: "native-fixture-publication",
		LiveRoot: liveRoot, Store: store, Baseline: baseline, Workspace: workspaceOptions,
		Journal: publicationJournal, Ledger: ledger, OpenPublication: openPublication,
		OpenRecovery: func(string) (workflow.RecoveryRepository, error) { return openPublication() },
		Trust:        nativeFixtureTrust{},
	}
	runtime, err := NewPublicationRuntime(executor, finalizerOptions)
	if err != nil {
		return err
	}
	defer runtime.Close()
	supervisor := &Supervisor{IPC: server, Broker: &nativeFixtureBroker{stream: stream}, Policy: policy, Decisions: recorder, ProposalLimit: uint64(len(calls))}
	serveDone := make(chan error, 1)
	go func() { serveDone <- runtime.Serve(ctx, supervisor) }()
	for sequence, call := range calls {
		proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: call.callID, Tool: call.tool, Arguments: call.arguments}
		if err := client.SendProposal(proposal); err != nil {
			return fmt.Errorf("send native fixture proposal: %w", err)
		}
		result, err := client.ReceiveResult()
		if err != nil {
			return fmt.Errorf("receive native fixture result: %w", err)
		}
		if err := validateNativeFixtureResult(result, call, policy, uint64(sequence+1), executor.Tip()); err != nil {
			return err
		}
	}
	if err := client.Close(); err != nil {
		return err
	}
	if err := <-serveDone; err != nil {
		return fmt.Errorf("native fixture supervised session: %w", err)
	}
	result, err := runtime.Finalize(ctx)
	if err != nil {
		return fmt.Errorf("native fixture publication: %w", err)
	}
	if err := requireNativeFixturePublicationSuccess(result); err != nil {
		return err
	}
	if err := writeNativeFixtureReceipt(output, result.Status); err != nil {
		return err
	}
	return nil
}

func requireNativeFixturePublicationSuccess(result publication.Result) error {
	if result.Status != publication.StatusSucceeded {
		return fmt.Errorf("native fixture publication did not succeed: status=%s", result.Status)
	}
	return nil
}

func writeNativeFixtureReceipt(output io.Writer, status publication.Status) error {
	if err := requireNativeFixturePublicationSuccess(publication.Result{Status: status}); err != nil {
		return err
	}
	if output == nil {
		return nil
	}
	if _, err := fmt.Fprintf(output, "{\"mode\":\"native-fixture\",\"claim_bearing\":false,\"provider_exchange\":false,\"containment\":\"not-established\",\"publication_status\":%q}\n", status); err != nil {
		return fmt.Errorf("write native fixture receipt: %w", err)
	}
	return nil
}

func validateNativeFixtureResult(result protocol.Result, call nativeFixtureCall, policy gate.Policy, sequence uint64, expectedGeneration *sessionrepo.Generation) error {
	digest, err := protocol.CanonicalArgumentsDigest(call.tool, call.arguments)
	if err != nil {
		return fmt.Errorf("digest native fixture call %s: %w", call.callID, err)
	}
	if result.SchemaVersion != protocol.ResultSchemaVersion || result.ToolCallID != call.callID ||
		result.Tool != call.tool || result.Sequence != sequence || result.CanonicalArgumentsDigest != digest ||
		result.Verdict != string(gate.Allow) || result.ReasonCode != "policy_rule_allow" ||
		result.PolicyDigest != policy.Digest() || result.ResponseID == nil ||
		result.ResponseID.Issuer != nativeFixtureResponseIssuer || result.ResponseID.Opaque != call.responseID {
		return fmt.Errorf("native fixture result does not preserve trusted capture semantics: %+v", result)
	}
	if len(result.Output) == 0 || expectedGeneration == nil {
		return fmt.Errorf("native fixture ALLOW result for %s has no durable output", call.callID)
	}
	if err := protocol.ValidateStrictJSON(result.Output); err != nil {
		return fmt.Errorf("native fixture result for %s is not strict JSON: %w", call.callID, err)
	}
	if expectedGeneration.ID() != call.outputGeneration {
		return fmt.Errorf("native fixture call %s expected output generation %q, got %q", call.callID, call.outputGeneration, expectedGeneration.ID())
	}
	switch call.tool {
	case "read":
		var payload durableReadSummaryPayload
		if err := decodeNativeFixtureOutput(result.Output, &payload); err != nil {
			return fmt.Errorf("decode native fixture read result for %s: %w", call.callID, err)
		}
		if payload.Tool != "read" || payload.Path != "task.txt" || payload.Generation.ID != expectedGeneration.ID() ||
			payload.Generation.TreeDigest != expectedGeneration.TreeDigest() || payload.Size != len(call.expectedContent) ||
			payload.ContentDigest != durableContentDigest([]byte(call.expectedContent)) || payload.Content != call.expectedContent ||
			payload.Outcome != "success" {
			return fmt.Errorf("native fixture read result lost sealed generation/content evidence for %s: %+v", call.callID, payload)
		}
	case "write":
		var payload durableMutationSummaryPayload
		if err := decodeNativeFixtureOutput(result.Output, &payload); err != nil {
			return fmt.Errorf("decode native fixture write result for %s: %w", call.callID, err)
		}
		if payload.Tool != "write" || payload.Generation.ID != expectedGeneration.ID() ||
			payload.Generation.TreeDigest != expectedGeneration.TreeDigest() || payload.Transition.ID != call.expectedTransition ||
			payload.Transition.Sequence != 1 || payload.EffectID == "" || payload.Outcome != "success" {
			return fmt.Errorf("native fixture write result lost committed generation/transition evidence for %s: %+v", call.callID, payload)
		}
	default:
		return fmt.Errorf("native fixture has no durable result schema for tool %q", call.tool)
	}
	return nil
}

func decodeNativeFixtureOutput(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("durable result contains trailing JSON")
	}
	return nil
}

const nativeFixtureBindingToken = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

type nativeFixtureCall struct {
	responseID         string
	callID             string
	tool               string
	generation         string
	outputGeneration   string
	expectedContent    string
	expectedTransition string
	arguments          json.RawMessage
}

type nativeFixtureBroker struct {
	mu     sync.Mutex
	stream *protocol.Stream
}

func (b *nativeFixtureBroker) Correlate(_ context.Context, proposal protocol.Proposal) (correlation.Decision, error) {
	if b == nil || b.stream == nil {
		return correlation.Decision{}, errors.New("native fixture broker is not configured")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	encoded, err := protocol.MarshalProposal(proposal)
	if err != nil {
		return correlation.Decision{}, err
	}
	return b.stream.Propose(encoded), nil
}

type nativeFixtureAuthority struct{}

func (nativeFixtureAuthority) authorize(sessionrepo.OperationRequest) error { return nil }
func (nativeFixtureAuthority) verifyDecision(delta.PolicyDecision, delta.TransitionBinding) error {
	return nil
}
func (nativeFixtureAuthority) verifySettlement(sessionrepo.CommandSettlement, string, string) error {
	return nil
}

type nativeFixtureTrust struct{}

func (nativeFixtureTrust) CheckAdmission(context.Context) error { return nil }
func (nativeFixtureTrust) CheckPublicationPrerequisites(context.Context, workflow.EvidenceRequest) error {
	return nil
}
func (nativeFixtureTrust) AttestQuiescent(context.Context, *os.File, *sessionrepo.Generation, *sessionrepo.Generation) error {
	return nil
}
func (nativeFixtureTrust) VerifyTransition(context.Context, delta.PolicyDecision, delta.TransitionBinding) error {
	return nil
}
func (nativeFixtureTrust) VerifyOrigin(context.Context, sessionrepo.EvidenceBundle, delta.Transition) error {
	return nil
}

func writeNativeFixtureWorkspace(root, content string) error {
	if err := os.WriteFile(filepath.Join(root, "task.txt"), []byte(content), 0o644); err != nil {
		return err
	}
	return os.Mkdir(filepath.Join(root, "build"), 0o755)
}

func nativeFixtureOpenRoot(path string) (*os.File, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open native fixture root %s: %w", path, err)
	}
	return file, nil
}
