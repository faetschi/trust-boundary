//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

const recoveryPreviewRootEnv = "TBOUND_GOVERNED_PREVIEW_ROOT"
const recoveryPreviewVariantEnv = "TBOUND_GOVERNED_PREVIEW_VARIANT"

func TestRecoveryPreviewOpensAGovernedStoreWithAnEffect(t *testing.T) {
	if root := os.Getenv(recoveryPreviewRootEnv); root != "" {
		recoveryPreviewChild(t, root, os.Getenv(recoveryPreviewVariantEnv))
		return
	}
	f := newWorkflowVerticalFixture(t, false, 0, []providerbridge.DevelopmentProviderTurn{
		{ResponseID: "preview-read", CallID: "preview-call", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)},
	})
	proposal, decision := ledgerDenyTurn(t, f, "preview-call", gate.Allow)
	executor, err := providerbridge.NewExecutor(f.conversation, f.durable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(f.ctx, proposal, decision); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.storeJournal.Close(); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(f.providerJournalPath)
	for _, variant := range []string{"production", "old-kind", "bare-policy-digest"} {
		t.Run(variant, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestRecoveryPreviewOpensAGovernedStoreWithAnEffect$", "-test.count=1")
			cmd.Env = []string{recoveryPreviewRootEnv + "=" + root, recoveryPreviewVariantEnv + "=" + variant, "TMPDIR=" + os.TempDir()}
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("fresh-process recovery preview: %v\n%s", err, output)
			}
		})
	}
}

func recoveryPreviewChild(t *testing.T, rootPath, variant string) {
	t.Helper()
	journal, err := openExistingRecoveryJournal(filepath.Join(rootPath, "store-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	effects, authorizations := 0, 0
	for _, effect := range trace.Effects {
		if strings.HasPrefix(effect.ID, "sessionrepo-effect-") {
			effects++
		}
	}
	for _, record := range trace.Records {
		if record.Event.Kind == "governed_operation_authorized" {
			authorizations++
		}
	}
	if effects == 0 || authorizations != 1 {
		t.Fatalf("retained Store effects=%d authorizations=%d, want one production authorization and retained effects", effects, authorizations)
	}
	if variant != "production" {
		// Rechain altered evidence so refusal tests the authorization, not audit hash integrity.
		var encoded bytes.Buffer
		head := ""
		for _, record := range trace.Records {
			event := record.Event
			if event.Kind == "governed_operation_authorized" {
				switch variant {
				case "old-kind":
					event.Kind = "governed_operation_authorization"
				case "bare-policy-digest":
					var admission governedOperationAdmissionRecord
					if err := json.Unmarshal(event.Data, &admission); err != nil {
						t.Fatal(err)
					}
					admission.GatePolicyDigest = strings.TrimPrefix(admission.GatePolicyDigest, gate.ProfileVersion+":")
					admission.GateDecision.PolicyDigest = admission.GatePolicyDigest
					event.Data, err = json.Marshal(admission)
					if err != nil {
						t.Fatal(err)
					}
				default:
					t.Fatalf("unknown preview variant %q", variant)
				}
			}
			record.Event, record.PreviousHash = event, head
			unsigned, err := json.Marshal(struct {
				Version      int         `json:"version"`
				Sequence     uint64      `json:"sequence"`
				PreviousHash string      `json:"previous_hash"`
				Event        audit.Event `json:"event"`
			}{record.Version, record.Sequence, record.PreviousHash, record.Event})
			if err != nil {
				t.Fatal(err)
			}
			record.Hash = bareSHA256Digest(digestBytes(unsigned))
			if err := json.NewEncoder(&encoded).Encode(record); err != nil {
				t.Fatal(err)
			}
			head = record.Hash
		}
		dir := t.TempDir()
		// The audit layer requires a 0700 parent, and t.TempDir creates its directory with the process umask.
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "modified-audit.jsonl")
		if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		modified, err := openExistingRecoveryJournal(path)
		if err != nil {
			t.Fatal(err)
		}
		defer modified.Close()
		journal = modified
	}
	root, err := openExistingPrivateRecoveryDirectory(filepath.Join(rootPath, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	options := sessionrepo.Options{Journal: journal, PolicyDigest: governedReplayStorePolicy,
		MetadataPolicyDigest: governedReplayMetaPolicy, Limits: workspace.DefaultLimits(),
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: governedReplayXattrPolicy, Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted}}
	managed, err := sessionlaunch.OpenRecoveryPreview(root, options)
	if variant == "production" {
		if err != nil {
			t.Fatalf("recovery preview of a governed Store with %d effects: %v", effects, err)
		}
		if err := managed.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err == nil {
		_ = managed.Close()
		t.Fatalf("recovery preview accepted %s authorization evidence", variant)
	}
	want := "retained operation effect has no governed Store authorization"
	if variant == "bare-policy-digest" {
		want = "malformed retained governed operation authorization"
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("wrong refusal for %s: %v, want %q", variant, err, want)
	}
}
