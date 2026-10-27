//go:build linux

package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/providerbridge"
)

var durableEffectIDPattern = regexp.MustCompile(`"effect_id":"[^"]*"`)

// replacementRune is U+FFFD, which invalid UTF-8 becomes in the summary a configured executor releases.
const replacementRune = "\xef\xbf\xbd"

// The research Bash summary is part of the provider-result evidence the thesis replays, so its bytes are
// frozen: no command output, whatever the runner returned. The golden string was computed from the
// summary encoder before the product output option existed.
func TestBashOutputResearchSummaryBytesAreUnchanged(t *testing.T) {
	store, _, _, g0 := durableBashFixture(t)
	proposal, decision := durableBashProposal(`{"command":"printf bash-output","timeout":5}`)
	leaseID, err := durableTrustedIdentity("lease", proposal, decision)
	if err != nil {
		t.Fatal(err)
	}
	runner := durableConsistentBashRunner(leaseID)
	runner.stderr = "warning on stderr\n"
	executor, err := NewDurableExecutor(DurableExecutorConfig{
		Store: store, Tip: g0, CallIssuer: durableCallIssuer,
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
		CommandRunner: runner, BashOutputPaths: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := executor.Execute(context.Background(), proposal, decision)
	if err != nil {
		t.Fatal(err)
	}
	got := durableEffectIDPattern.ReplaceAllString(string(output), `"effect_id":"EFFECT"`)
	want := `{"tool":"bash","generation":{"id":"g1","tree_digest":"` + g0.TreeDigest() + `"},` +
		`"transition":{"id":"transition-000001","sequence":1},"effect_id":"EFFECT","outcome":"success",` +
		`"command":{"exit_code":7,"exit_observed":true,"command_containment_status":"not-established",` +
		`"command_runner_profile":"synthetic-durable-bash-runner"}}`
	if got != want {
		t.Fatalf("research Bash summary bytes changed:\n got %s\nwant %s", got, want)
	}
}

func TestBashOutputBoundedCommandText(t *testing.T) {
	const e = "\xc3\xa9" // a two-byte rune
	for _, c := range []struct {
		name  string
		data  string
		limit int
		want  string
		cut   bool
	}{
		{"empty", "", 4, "", false},
		{"exact fit", "abc", 3, "abc", false},
		{"tail kept", "abcdef", 3, "def", true},
		{"cut never splits a rune", "x" + e + e + e + "y", 4, e + "y", true},
		{"invalid run replaced once", "a\xff\xfeb", 8, "a" + replacementRune + "b", false},
		{"bound holds for the replaced text", "\xff\xff\xffz", 3, "z", true},
		{"smaller than one rune", e, 1, "", true},
	} {
		got, cut := boundedCommandText([]byte(c.data), c.limit)
		if got != c.want || cut != c.cut || len(got) > c.limit {
			t.Errorf("%s: boundedCommandText(%q, %d) = %q, %t; want %q, %t", c.name, c.data, c.limit, got, cut, c.want, c.cut)
		}
	}
}

func TestBashOutputExecutorRefusesAnOutOfRangeBound(t *testing.T) {
	store, _, _, g0 := durableBashFixture(t)
	for _, bound := range []int{-1, durableCommandOutputMaxBytes + 1} {
		_, err := NewDurableExecutor(DurableExecutorConfig{
			Store: store, Tip: g0, CallIssuer: durableCallIssuer,
			PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest, CommandOutputBytes: bound,
		})
		if err == nil {
			t.Errorf("an executor accepted the command output bound %d", bound)
		}
	}
}

// A configured executor shows the provider each stream as bounded valid UTF-8 with its truncated flag.
func TestBashOutputExecutorShowsBoundedOutputWhenConfigured(t *testing.T) {
	store, _, _, g0 := durableBashFixture(t)
	proposal, decision := durableBashProposal(`{"command":"true"}`)
	leaseID, err := durableTrustedIdentity("lease", proposal, decision)
	if err != nil {
		t.Fatal(err)
	}
	runner := durableConsistentBashRunner(leaseID)
	runner.stdout = "0123456789abcdefTAIL"
	runner.stderr = "bad:\xff\xfe"
	executor, err := NewDurableExecutor(DurableExecutorConfig{
		Store: store, Tip: g0, CallIssuer: durableCallIssuer,
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
		CommandRunner: runner, BashOutputPaths: []string{}, CommandOutputBytes: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := executor.Execute(context.Background(), proposal, decision)
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.ValidateStrictJSON(output); err != nil {
		t.Fatal(err)
	}
	var summary durableMutationSummaryPayload
	if err := json.Unmarshal(output, &summary); err != nil {
		t.Fatal(err)
	}
	command := summary.Command
	if command == nil || command.Stdout == nil || *command.Stdout != "456789abcdefTAIL" || !*command.StdoutTruncated ||
		*command.Stderr != "bad:"+replacementRune || *command.StderrTruncated {
		t.Fatalf("unexpected bounded output: %s", output)
	}
	if !strings.Contains(string(output), `"stdout_truncated":true`) || !strings.Contains(string(output), `"stderr_truncated":false`) {
		t.Fatalf("the flags must be explicit: %s", output)
	}
}

// boundedBashReplayFixture runs one Bash command through a Store whose executor shows the provider at most
// bound bytes of each stream: a long stdout, an stderr with invalid UTF-8, and the result released.
func boundedBashReplayFixture(t *testing.T, bound int) *governedReplayFixture {
	t.Helper()
	command := `i=0; while [ $i -lt 40 ]; do printf 'out-line-%02d\n' $i; i=$((i+1)); done; ` +
		`printf 'bad:\377\376:end\n' >&2; printf 'FINAL-LINE\n'`
	arguments, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	fixture := newGovernedReplayFixtureWithOutput(t, []providerbridge.DevelopmentProviderTurn{
		{ResponseID: "replay-response-bash", CallID: "replay-call-bash", Tool: "bash", Arguments: arguments},
	}, true, governedReplayAllowAllPolicy(t), bound)
	executeGovernedReplayTurn(t, fixture, true)
	return fixture
}

func releasedBashSummary(t *testing.T, journal *audit.Journal) durableMutationSummaryPayload {
	t.Helper()
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range trace.Records {
		if record.Event.Kind != "provider_tool_result" {
			continue
		}
		var evidence providerResultEvidence
		if err := unmarshalGovernedJSON(record.Event.Data, &evidence); err != nil {
			t.Fatal(err)
		}
		result, err := protocol.DecodeResult(evidence.Result)
		if err != nil {
			t.Fatal(err)
		}
		var summary durableMutationSummaryPayload
		if err := decodeGovernedResult(result.Output, &summary); err != nil {
			t.Fatal(err)
		}
		return summary
	}
	t.Fatal("the provider journal holds no tool result")
	return durableMutationSummaryPayload{}
}

// The model-visible output is rebuilt on replay from the command result the Store retained, with the
// bound the turn pinned. A replay that pins another bound, or none, does not verify the same evidence.
func TestBashOutputReplayRebuildsFromTheStore(t *testing.T) {
	fixture := boundedBashReplayFixture(t, 64)
	command := releasedBashSummary(t, fixture.providerJournal).Command
	if command == nil || command.Stdout == nil || !*command.StdoutTruncated || len(*command.Stdout) > 64 ||
		!strings.HasSuffix(*command.Stdout, "FINAL-LINE\n") || strings.Contains(*command.Stdout, "out-line-00") ||
		*command.Stderr != "bad:"+replacementRune+":end\n" || *command.StderrTruncated {
		t.Fatalf("the released summary is not the bounded tail: %+v", command)
	}
	if _, err := ReplayGovernedTranscript(fixture.providerJournal, fixture.storeJournal, fixture.store, fixture.pins); err != nil {
		t.Fatalf("replay of the bounded Bash output: %v", err)
	}
	for _, bound := range []int{0, 32, 128} {
		pins := fixture.pins
		pins.CommandOutputBytes = bound
		if _, err := ReplayGovernedTranscript(fixture.providerJournal, fixture.storeJournal, fixture.store, pins); err == nil {
			t.Errorf("replay verified the bounded output under the pinned bound %d", bound)
		}
	}
	pins := fixture.pins
	pins.CommandOutputBytes = durableCommandOutputMaxBytes + 1
	if _, err := ReplayGovernedTranscript(fixture.providerJournal, fixture.storeJournal, fixture.store, pins); err == nil {
		t.Error("replay accepted a pinned bound outside the permitted range")
	}
}

// A provider result whose Bash output was edited, and whose journal was validly rehashed, does not
// verify against the command result the Store retained.
func TestBashOutputReplayRejectsAForgedOutput(t *testing.T) {
	for name, forge := range map[string]func(*durableCommandSummary){
		"stdout": func(c *durableCommandSummary) {
			*c.Stdout = strings.Replace(*c.Stdout, "FINAL-LINE", "ALL-TESTS-OK", 1)
		},
		"stderr":    func(c *durableCommandSummary) { *c.Stderr = "" },
		"truncated": func(c *durableCommandSummary) { *c.StdoutTruncated = false },
		"output dropped": func(c *durableCommandSummary) {
			c.Stdout, c.StdoutTruncated, c.Stderr, c.StderrTruncated = nil, nil, nil, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := boundedBashReplayFixture(t, 64)
			original, err := fixture.providerJournal.Trace()
			if err != nil {
				t.Fatal(err)
			}
			tamperedJournal, err := audit.Open(filepath.Join(privateGovernedTestRoot(t), "forged-bash-output.audit"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = tamperedJournal.Close() })
			forged := false
			for _, record := range original.Records {
				event := record.Event
				if event.Kind == "provider_tool_result" {
					var evidence providerResultEvidence
					if err := unmarshalGovernedJSON(event.Data, &evidence); err != nil {
						t.Fatal(err)
					}
					result, err := protocol.DecodeResult(evidence.Result)
					if err != nil {
						t.Fatal(err)
					}
					var summary durableMutationSummaryPayload
					if err := decodeGovernedResult(result.Output, &summary); err != nil {
						t.Fatal(err)
					}
					forge(summary.Command)
					if result.Output, err = json.Marshal(summary); err != nil {
						t.Fatal(err)
					}
					if evidence.Result, err = protocol.MarshalResult(result); err != nil {
						t.Fatal(err)
					}
					evidence.ResultDigest = digestBytes(evidence.Result)
					if event.Data, err = json.Marshal(evidence); err != nil {
						t.Fatal(err)
					}
					forged = true
				}
				if _, err := tamperedJournal.Append(event); err != nil {
					t.Fatalf("append rehashed event %d: %v", record.Sequence, err)
				}
			}
			if !forged {
				t.Fatal("the fixture holds no provider_tool_result to forge")
			}
			if _, err := providerbridge.ReplayTranscript(tamperedJournal, fixture.pins.Provider, acceptingProviderReplayVerifier{}); err != nil {
				t.Fatalf("the forged output is not self-consistent provider evidence: %v", err)
			}
			if _, err := ReplayGovernedTranscript(tamperedJournal, fixture.storeJournal, fixture.store, fixture.pins); err == nil {
				t.Fatal("governed replay accepted a rehashed provider Bash output that differs from the Store's command result")
			}
		})
	}
}

// firstRequestBashDescription is the description of the bash tool in the first request the provider
// journal holds: the bytes the provider was really told.
func firstRequestBashDescription(t *testing.T, journal *audit.Journal) string {
	t.Helper()
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range trace.Records {
		if record.Event.Kind != "provider_exchange" {
			continue
		}
		var exchange struct {
			Request struct {
				Body []byte `json:"body"`
			} `json:"request"`
		}
		var body struct {
			Tools []struct {
				Function struct {
					Name        string `json:"name"`
					Description string `json:"description"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(record.Event.Data, &exchange); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(exchange.Request.Body, &body); err != nil {
			t.Fatal(err)
		}
		for _, tool := range body.Tools {
			if tool.Function.Name == "bash" {
				return tool.Function.Description
			}
		}
	}
	t.Fatal("the provider journal holds no request that advertises bash")
	return ""
}

// The bash description a provider is told must match what the executor then releases: a session whose
// executor returns only the exit code (serve --pi) must not be told it gets stdout and stderr, and a
// product turn, whose executor releases bounded output, must be.
func TestBashOutputAdvertisedDescriptionMatchesTheExecutorResult(t *testing.T) {
	for _, c := range []struct {
		name   string
		bound  int
		output bool
	}{
		{"serve --pi executor, exit code only", 0, false},
		{"product turn executor, bounded output", productCommandOutputBytes, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			fixture := boundedBashReplayFixture(t, c.bound)
			command := releasedBashSummary(t, fixture.providerJournal).Command
			if command == nil {
				t.Fatal("the released summary has no command result")
			}
			released := command.Stdout != nil && command.Stderr != nil
			description := firstRequestBashDescription(t, fixture.providerJournal)
			if released != c.output {
				t.Fatalf("the executor released output = %t, want %t", released, c.output)
			}
			if advertised := strings.Contains(description, "stdout"); advertised != released {
				t.Errorf("the provider is told %q, but the executor released output = %t", description, released)
			}
		})
	}
}
