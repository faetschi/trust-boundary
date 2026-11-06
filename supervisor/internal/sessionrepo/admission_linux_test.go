//go:build linux

package sessionrepo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
)

func TestAdmissionPersistsCandidateAndUsesExactScope(t *testing.T) {
	for _, tc := range []struct {
		name    string
		scope   []string
		outcome string
	}{
		{name: "allow exact path", scope: []string{"task.txt"}, outcome: "allow"},
		{name: "deny parent or wrong path", scope: []string{"tasks"}, outcome: "deny"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			journal, request, intent, policy, metadata, input, output, candidate :=
				admissionFixture(t, tc.scope)
			defer journal.Close()

			reference, err := AppendAdmission(
				journal, request, intent, policy, metadata, input, output, candidate,
			)
			if err != nil {
				t.Fatal(err)
			}
			if reference.Outcome != tc.outcome {
				t.Fatalf("outcome = %q, want %q", reference.Outcome, tc.outcome)
			}
			if err := VerifyAdmission(journal, reference, intent, policy, metadata); err != nil {
				t.Fatalf("verify admission: %v", err)
			}

			trace, err := journal.Trace()
			if err != nil {
				t.Fatal(err)
			}
			record, err := decodeAdmissionRecord(trace.Records[0].Event.Data)
			if err != nil {
				t.Fatal(err)
			}
			if record.Outcome != tc.outcome || record.Candidate.ID != candidate.ID ||
				!sameChanges(record.Candidate.Changes, candidate.Changes) ||
				record.Input.Generation != input.Generation ||
				record.Output.Generation != output.Generation {
				t.Fatalf("persisted record lost admission evidence: %+v", record)
			}
		})
	}
}

func TestVerifyAdmissionRejectsIntentReferenceAndRecordTampering(t *testing.T) {
	journal, request, intent, policy, metadata, input, output, candidate :=
		admissionFixture(t, []string{"task.txt"})
	defer journal.Close()

	reference, err := AppendAdmission(
		journal, request, intent, policy, metadata, input, output, candidate,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyAdmission(journal, reference, []byte("different intent"), policy, metadata); err == nil {
		t.Fatal("accepted a different primary intent")
	}
	wrongReference := reference
	wrongReference.Outcome = "deny"
	if err := VerifyAdmission(journal, wrongReference, intent, policy, metadata); err == nil {
		t.Fatal("accepted a modified reference")
	}
	if err := VerifyAdmission(journal, reference, intent, digestBytes([]byte("other policy")), metadata); err == nil {
		t.Fatal("accepted a different pinned policy")
	}

	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	record, err := decodeAdmissionRecord(trace.Records[0].Event.Data)
	if err != nil {
		t.Fatal(err)
	}
	record.Outcome = "deny"
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	tampered, err := journal.Append(audit.Event{Kind: admissionEventKind, ID: trace.Records[0].Event.ID, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyAdmission(journal, referenceFor(tampered, record), intent, policy, metadata); err == nil {
		t.Fatal("accepted a hash-valid record with a false policy outcome")
	}
}

func admissionFixture(
	t *testing.T,
	scope []string,
) (
	*audit.Journal,
	OperationRequest,
	[]byte,
	string,
	string,
	delta.TreeManifest,
	delta.TreeManifest,
	delta.Transition,
) {
	t.Helper()

	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := audit.Open(filepath.Join(directory, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	policy := digestBytes([]byte("pinned policy"))
	metadata := digestBytes([]byte("pinned metadata policy"))
	contentBefore := digestBytes([]byte("before"))
	contentAfter := digestBytes([]byte("after"))
	metadataIdentity := digestBytes([]byte("file metadata"))
	input := delta.TreeManifest{
		Generation: "g1", MetadataPolicyDigest: metadata,
		Objects: []delta.ManifestObject{{
			Path: "task.txt",
			State: delta.ObjectState{
				Exists: true, Type: delta.ObjectRegular,
				ContentDigest: contentBefore, MetadataIdentity: metadataIdentity,
			},
		}},
	}
	output := delta.TreeManifest{
		Generation: "g2", MetadataPolicyDigest: metadata,
		Objects: []delta.ManifestObject{{
			Path: "task.txt",
			State: delta.ObjectState{
				Exists: true, Type: delta.ObjectRegular,
				ContentDigest: contentAfter, MetadataIdentity: metadataIdentity,
			},
		}},
	}
	decision := delta.PolicyDecision{
		ID: "decision-1", Outcome: delta.PolicyAllow,
		PolicyDigest: policy, MetadataPolicyDigest: metadata,
	}
	request := OperationRequest{
		Tool: "write",
		Operation: delta.OperationIdentity{
			Kind: delta.OperationProposalCall, ProposalID: "proposal-1", CallIssuer: "test-calls/v1", CallID: "call-1",
		},
		Decision: decision, InputGeneration: input.Generation,
		InputTreeDigest:  mustTreeDigest(t, input),
		ArgumentDigest:   digestBytes([]byte("arguments")),
		EffectID:         "sessionrepo-effect-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		OutputScopePaths: cloneOutputScopePaths(scope),
	}
	candidate := delta.Transition{
		ID: "transition-000001", Sequence: 1,
		Tool: request.Tool, ArgumentDigest: request.ArgumentDigest,
		InputGeneration: input.Generation, OutputGeneration: output.Generation,
		InputTreeDigest:  request.InputTreeDigest,
		OutputTreeDigest: mustTreeDigest(t, output),
		Operation:        request.Operation,
		Changes:          diffManifests(input, output), Decision: decision,
	}
	intent, err := json.Marshal(readIntent{SchemaVersion: OperationIntentSchemaVersion, Request: request, Path: scope[0]})
	if err != nil {
		t.Fatal(err)
	}
	return journal, request, intent, policy, metadata, input, output, candidate
}

func bashAdmissionFixture(t *testing.T) (*audit.Journal, OperationRequest, []byte, string, string, delta.TreeManifest, delta.TreeManifest, delta.Transition) {
	t.Helper()
	journal, request, _, policy, metadata, input, output, candidate := admissionFixture(t, []string{"task.txt"})
	request.Tool = "bash"
	request.Operation = delta.OperationIdentity{Kind: delta.OperationLease, LeaseID: "lease-1"}
	request.ViewID = "view-" + strings.Repeat("a", 48)
	var err error
	request.ExecutionContextDigest, err = commandExecutionContextDigest(request.ViewID, request.InputTreeDigest, request.Operation.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	command := CommandSpec{Executable: "/bin/bash", Args: []string{"-c", "printf after > task.txt"}, DeclaredOutputPaths: []string{"task.txt"}}
	request.ArgumentDigest, err = CommandArgumentsDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Tool, candidate.ArgumentDigest = request.Tool, request.ArgumentDigest
	candidate.ViewID, candidate.ExecutionContextDigest = request.ViewID, request.ExecutionContextDigest
	candidate.Operation = request.Operation
	intent, err := json.Marshal(readIntent{SchemaVersion: OperationIntentSchemaVersion, Request: request, Command: command, RunnerProfile: "admission-test"})
	if err != nil {
		t.Fatal(err)
	}
	return journal, request, intent, policy, metadata, input, output, candidate
}

func TestAdmissionRejectsInconsistentBashBindings(t *testing.T) {
	for _, field := range []string{"argument", "context", "same-generation"} {
		t.Run(field, func(t *testing.T) {
			journal, request, intent, policy, metadata, input, output, candidate := bashAdmissionFixture(t)
			defer journal.Close()
			reference, err := AppendAdmission(journal, request, intent, policy, metadata, input, output, candidate)
			if err != nil {
				t.Fatal(err)
			}
			if err := VerifyAdmission(journal, reference, intent, policy, metadata); err != nil {
				t.Fatal(err)
			}
			trace, err := journal.Trace()
			if err != nil {
				t.Fatal(err)
			}
			record, err := decodeAdmissionRecord(trace.Records[0].Event.Data)
			if err != nil {
				t.Fatal(err)
			}
			_, envelope, err := decodeOperationIntent(intent)
			if err != nil {
				t.Fatal(err)
			}
			switch field {
			case "argument":
				request.ArgumentDigest = digestBytes([]byte("different arguments"))
				candidate.ArgumentDigest = request.ArgumentDigest
			case "context":
				request.ExecutionContextDigest = digestBytes([]byte("different context"))
				candidate.ExecutionContextDigest = request.ExecutionContextDigest
			case "same-generation":
				output.Generation = input.Generation
				candidate.OutputGeneration = output.Generation
			}
			if field != "same-generation" {
				envelope.Request = request
				intent, err = json.Marshal(envelope)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := AppendAdmission(journal, request, intent, policy, metadata, input, output, candidate); err == nil {
				t.Fatal("accepted inconsistent Bash admission")
			}
			if field == "same-generation" {
				return
			}
			record.Request, record.Candidate, record.Output = request, candidate, output
			record.IntentDigest = digestBytes(intent)
			data, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			appended, err := journal.Append(audit.Event{Kind: admissionEventKind, ID: "admission-" + record.IntentDigest, Data: data})
			if err != nil {
				t.Fatal(err)
			}
			if err := VerifyAdmission(journal, referenceFor(appended, record), intent, policy, metadata); err == nil {
				t.Fatal("accepted hash-valid record with a mismatched Bash digest")
			}
		})
	}
}

func mustTreeDigest(t *testing.T, manifest delta.TreeManifest) string {
	t.Helper()
	digest, err := delta.ComputeTreeDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

// AppendAdmission validates and durably appends the complete admission
// evidence. primaryIntent is the exact byte sequence whose digest is recorded.
// The Store must authenticate the pre-operation decision and validate the
// candidate against its accepted chain before calling this function. This API
// does not independently prove that the supplied intent is a durable effect.
func AppendAdmission(
	journal *audit.Journal,
	request OperationRequest,
	primaryIntent []byte,
	policyDigest, metadataPolicyDigest string,
	input, output delta.TreeManifest,
	candidate delta.Transition,
) (AdmissionReference, error) {
	return AppendAdmissionScoped(journal, request, primaryIntent, policyDigest, metadataPolicyDigest,
		input, output, candidate, AdmissionScope{})
}

// VerifyAdmission checks the journal chain, persisted record, supplied
// reference, exact primary intent, pinned policy identities, and recomputed
// scope outcome.
func VerifyAdmission(
	journal *audit.Journal,
	reference AdmissionReference,
	primaryIntent []byte,
	policyDigest, metadataPolicyDigest string,
) error {
	return VerifyAdmissionScoped(journal, reference, primaryIntent, policyDigest, metadataPolicyDigest, AdmissionScope{})
}

func (s *Store) testPath() string { return s.rootPath }

func (g *Generation) testPath() string {
	return filepath.Join(g.store.rootPath, "generations", g.id)
}

func (view *CommandView) testPath() string { return view.path }
