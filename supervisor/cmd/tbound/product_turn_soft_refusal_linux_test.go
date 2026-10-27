//go:build linux

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
)

func TestProductTurnSoftRefusalContinuesWithoutAnEffect(t *testing.T) {
	for _, c := range []struct{ name, tool, arguments, code string }{
		{"missing read", "read", `{"path":"missing.txt"}`, "read_missing"},
		{"directory write", "write", `{"path":"dir","content":"x"}`, "target_not_regular"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newScopedProductDriverFixture(t, productsession.PublicationAuto, nil, false, func(root string) {
				if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(filepath.Join(root, "dir"), 0o755); err != nil {
					t.Fatal(err)
				}
			})
			script := &productDriverScript{turns: []providerbridge.ScriptTurn{
				{ResponseID: "soft-response", CallID: "soft-call", Tool: c.tool, Arguments: json.RawMessage(c.arguments)},
				{ResponseID: "soft-final", Text: "Done."},
			}}
			receipt, turn, err := f.run(t, f.submit(t, "soft", "Try the tool."), script)
			if err != nil || turn.Outcome != productsession.OutcomeNoDiff || script.requests() != 2 {
				t.Fatalf("soft refusal must continue: outcome=%s requests=%d err=%v", turn.Outcome, script.requests(), err)
			}
			encoded, _ := json.Marshal(script.histories[1])
			if !strings.Contains(string(encoded), c.code) || !strings.Contains(string(encoded), "Hint:") {
				t.Fatalf("missing error and hint: %s", encoded)
			}
			paths, err := productTurnPathsFromLink(*turn.Link)
			if err != nil {
				t.Fatal(err)
			}
			retained, err := openRetainedProductTurn(paths, f.env.storeOptions(turn), f.env.OpenCommandHistory)
			if err != nil {
				t.Fatal(err)
			}
			defer retained.Close()
			bundle, err := retained.Store.EvidenceBundle()
			disposition, reconstructErr := sessionrepo.Reconstruct(bundle)
			if err != nil || reconstructErr != nil || disposition.Quarantined || disposition.Attempts != 0 {
				t.Fatalf("refusal affected Store: %+v %v %v", disposition, err, reconstructErr)
			}
			host, tokens := productDriverExpectedUsage(t, paths.ProviderJournalPath, 0)
			if receipt.Usage.HostCalls != host || receipt.Usage.Tokens != tokens {
				t.Fatalf("refusal charged an effect: %+v want %d/%d", receipt.Usage, host, tokens)
			}
			f.followUp(t, "after-soft", productsession.OutcomeNoDiff)
		})
	}
}
