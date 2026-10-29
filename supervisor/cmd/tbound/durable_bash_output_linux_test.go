//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
)

var durableEffectIDPattern = regexp.MustCompile(`"effect_id":"[^"]*"`)

// replacementRune is U+FFFD, which invalid UTF-8 becomes in the summary a configured executor releases.
const replacementRune = "\xef\xbf\xbd"

// The thesis summary bytes are frozen at 02f9fb7, including the 4 KiB stream tails and raw byte counts.
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
		`"command_runner_profile":"synthetic-durable-bash-runner",` +
		`"stdout":"bash output\n","stdout_bytes":12,"stdout_truncated":false,` +
		`"stderr":"warning on stderr\n","stderr_bytes":18,"stderr_truncated":false}}`
	if got != want {
		t.Fatalf("research Bash summary bytes changed:\n got %s\nwant %s", got, want)
	}
}

func TestBashOutputThesisProjectionBytesAreUnchanged(t *testing.T) {
	for _, c := range []struct {
		name, stream, want string
	}{
		{"empty", "", `"stdout":"","stdout_bytes":0,"stdout_truncated":false`},
		{"tail", strings.Repeat("x", 4096) + "z", `"stdout":"` + strings.Repeat("x", 4095) + `z","stdout_bytes":4097,"stdout_truncated":true`},
		{"partial rune", "\xc3\xa9" + strings.Repeat("x", 4095), `"stdout":"` + strings.Repeat("x", 4095) + `","stdout_bytes":4097,"stdout_truncated":true`},
		{"invalid UTF-8", "a\xff\xfeb", `"stdout":"a` + replacementRune + `b","stdout_bytes":4,"stdout_truncated":false`},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := json.Marshal(newDurableCommandSummary(sessionrepo.CommandResult{Stdout: []byte(c.stream)}, "test", 0))
			if err != nil {
				t.Fatal(err)
			}
			want := `{"exit_code":0,"exit_observed":false,"command_containment_status":"not-established","command_runner_profile":"test",` +
				c.want + `,"stderr":"","stderr_bytes":0,"stderr_truncated":false}`
			if string(got) != want {
				t.Fatalf("thesis command summary bytes changed:\n got %s\nwant %s", got, want)
			}
		})
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
	return bashReplayFixtureFor(t, bound, `i=0; while [ $i -lt 40 ]; do printf 'out-line-%02d\n' $i; i=$((i+1)); done; `+
		`printf 'bad:\377\376:end\n' >&2; printf 'FINAL-LINE\n'`)
}

// bashReplayFixtureFor runs command as a Bash turn, then the turns that follow it, through a Store whose
// executor shows the provider at most bound bytes of each stream. Every result is released, so a following
// turn's request carries the Bash result.
func bashReplayFixtureFor(t *testing.T, bound int, command string, then ...providerbridge.DevelopmentProviderTurn) *governedReplayFixture {
	t.Helper()
	arguments, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	turns := append([]providerbridge.DevelopmentProviderTurn{
		{ResponseID: "replay-response-bash", CallID: "replay-call-bash", Tool: "bash", Arguments: arguments},
	}, then...)
	fixture := newGovernedReplayFixtureWithOutput(t, turns, true, governedReplayAllowAllPolicy(t), bound)
	for range turns {
		executeGovernedReplayTurn(t, fixture, true)
	}
	return fixture
}

func releasedBashSummary(t *testing.T, journal *audit.Journal) durableMutationSummaryPayload {
	t.Helper()
	var summary durableMutationSummaryPayload
	if err := decodeGovernedResult(releasedBashOutput(t, journal), &summary); err != nil {
		t.Fatal(err)
	}
	return summary
}

// releasedBashOutput is the exact summary the provider journal released for its first tool result.
func releasedBashOutput(t *testing.T, journal *audit.Journal) json.RawMessage {
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
		return result.Output
	}
	t.Fatal("the provider journal holds no tool result")
	return nil
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

// providerRequestBodies are the bodies of the requests the provider journal holds, in order: the bytes the
// provider was really sent.
func providerRequestBodies(t *testing.T, journal *audit.Journal) [][]byte {
	t.Helper()
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	var bodies [][]byte
	for _, record := range trace.Records {
		if record.Event.Kind != "provider_exchange" {
			continue
		}
		var exchange struct {
			Request struct {
				Body []byte `json:"body"`
			} `json:"request"`
		}
		if err := json.Unmarshal(record.Event.Data, &exchange); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, exchange.Request.Body)
	}
	return bodies
}

// firstRequestBashDescription is the description of the bash tool in the first request the provider
// journal holds.
func firstRequestBashDescription(t *testing.T, journal *audit.Journal) string {
	t.Helper()
	bodies := providerRequestBodies(t, journal)
	if len(bodies) == 0 {
		t.Fatal("the provider journal holds no request")
	}
	var body struct {
		Tools []struct {
			Function struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(bodies[0], &body); err != nil {
		t.Fatal(err)
	}
	for _, tool := range body.Tools {
		if tool.Function.Name == "bash" {
			return tool.Function.Description
		}
	}
	t.Fatal("the first request advertises no bash tool")
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

// replayTwiceEqual replays the governed transcript twice and requires the same projection both times.
func replayTwiceEqual(t *testing.T, fixture *governedReplayFixture) {
	t.Helper()
	first, err := ReplayGovernedTranscript(fixture.providerJournal, fixture.storeJournal, fixture.store, fixture.pins)
	if err != nil {
		t.Fatalf("replay of the released Bash output: %v", err)
	}
	second, err := ReplayGovernedTranscript(fixture.providerJournal, fixture.storeJournal, fixture.store, fixture.pins)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("a second replay of the same evidence differs: %v", err)
	}
}

// The released tail with the widest encoding is a run of control bytes, which JSON writes in six bytes
// each, and then again in the next request, where the summary is itself a string. A 16 KiB tail of NULs on
// stdout and of terminal escape sequences with HTML characters on stderr must still fit the protocol frame
// and the request bound, release exactly the tails, and replay equal.
func TestBashOutputControlBytesStayWithinTheBoundsAndReplay(t *testing.T) {
	const bound = productCommandOutputBytes
	const sequence = "\x1b[31;1mERR\x1b[0m\x1b]0;title\a\x01\x02\x03\x7f<>&\n"
	if bound%len(sequence) != 0 {
		t.Fatalf("the escape sequence of %d bytes does not divide the %d byte bound", len(sequence), bound)
	}
	fixture := bashReplayFixtureFor(t, bound,
		`printf '\0%.0s' {1..24000}; `+
			`printf '\033[31;1mERR\033[0m\033]0;title\007\001\002\003\177<>&\n%.0s' {1..1000} >&2`,
		providerbridge.DevelopmentProviderTurn{ResponseID: "replay-response-read", CallID: "replay-call-read",
			Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)})
	raw := releasedBashOutput(t, fixture.providerJournal)
	var summary durableMutationSummaryPayload
	if err := decodeGovernedResult(raw, &summary); err != nil {
		t.Fatal(err)
	}
	command := summary.Command
	if command == nil || command.Stdout == nil || command.Stderr == nil ||
		*command.Stdout != strings.Repeat("\x00", bound) || !*command.StdoutTruncated ||
		*command.Stderr != strings.Repeat(sequence, bound/len(sequence)) || !*command.StderrTruncated {
		t.Fatalf("the released streams are not the control byte tails: %+v", command)
	}
	if got := bytes.Count(raw, []byte(`\u0000`)); got != bound {
		t.Errorf("the summary holds %d encoded NULs, want %d", got, bound)
	}
	if limit := 2*bound*6 + 1024; len(raw) > limit || len(raw) > protocol.MaxFrameBytes {
		t.Errorf("the summary is %d bytes, over %d bytes or the %d byte frame", len(raw), limit, protocol.MaxFrameBytes)
	}
	bodies := providerRequestBodies(t, fixture.providerJournal)
	if len(bodies) != 2 {
		t.Fatalf("the provider journal holds %d requests, want 2", len(bodies))
	}
	if next, doubly := bodies[1], []byte(`\\u0000`); len(next) > openrouter.MaxRequestBytes || bytes.Count(next, doubly) != bound {
		t.Errorf("the request that carries the result is %d bytes with %d doubly encoded NULs, want at most %d bytes and %d",
			len(next), bytes.Count(next, doubly), openrouter.MaxRequestBytes, bound)
	}
	replayTwiceEqual(t, fixture)
}

// A command that prints exactly the 1 MiB the Store retains per stream, on both streams, releases exactly
// the last 16 KiB of each, and the replay rebuilds the same bytes from the retained result.
func TestBashOutputAtTheRetainedBoundaryReleasesTheTailsAndReplays(t *testing.T) {
	const bound = productCommandOutputBytes
	const lines = 1 << 16 // 16-byte lines, so 1 MiB on each stream
	fixture := bashReplayFixtureFor(t, bound, `printf '%015d\n' {0..65535}; printf 'e%014d\n' {0..65535} >&2`)
	var stdout, stderr strings.Builder
	for line := lines - bound/16; line < lines; line++ {
		fmt.Fprintf(&stdout, "%015d\n", line)
		fmt.Fprintf(&stderr, "e%014d\n", line)
	}
	command := releasedBashSummary(t, fixture.providerJournal).Command
	if command == nil || command.Stdout == nil || command.Stderr == nil ||
		*command.Stdout != stdout.String() || !*command.StdoutTruncated ||
		*command.Stderr != stderr.String() || !*command.StderrTruncated ||
		len(*command.Stdout) != bound || len(*command.Stderr) != bound {
		t.Fatalf("the streams at the 1 MiB boundary did not release exactly their last %d bytes: %+v", bound, command)
	}
	replayTwiceEqual(t, fixture)
}
