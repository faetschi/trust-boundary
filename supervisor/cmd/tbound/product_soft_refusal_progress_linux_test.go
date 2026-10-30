//go:build linux

package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/providerbridge"
)

func TestProductSoftRefusalLiveProgressRecomputesOnlyNewRefusals(t *testing.T) {
	const refusals = 4
	var turns []providerbridge.ScriptTurn
	for i := 0; i < refusals; i++ {
		turns = append(turns, providerbridge.ScriptTurn{ResponseID: fmt.Sprintf("response-%d", i), CallID: fmt.Sprintf("call-%d", i), Tool: "read", Arguments: json.RawMessage(`{"path":"missing.txt"}`)})
	}
	f := startSoftRefusalWorkflow(t, turns)
	fd, err := syscall.InotifyInit1(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	path := filepath.Join(f.storePath, "generations", f.durable.Tip().ID(), "task.txt")
	if _, err := syscall.InotifyAddWatch(fd, path, syscall.IN_OPEN|syscall.IN_CLOSE_NOWRITE); err != nil {
		t.Fatal(err)
	}
	// Close events prevent open-event coalescing; calibrate descriptor opens per preflight.
	countOpens := func() int {
		t.Helper()
		opens := 0
		var buf [65536]byte
		n, err := syscall.Read(fd, buf[:])
		if err != nil && !errors.Is(err, syscall.EAGAIN) {
			t.Fatal(err)
		}
		for offset := 0; offset < n; {
			mask := binary.NativeEndian.Uint32(buf[offset+4:])
			if mask&syscall.IN_Q_OVERFLOW != 0 {
				t.Fatal("generation scan observation overflowed")
			}
			if mask&syscall.IN_OPEN != 0 {
				opens++
			}
			offset += syscall.SizeofInotifyEvent + int(binary.NativeEndian.Uint32(buf[offset+12:]))
		}
		return opens
	}
	if _, err := f.durable.productPreflight(protocol.Proposal{Tool: "read", Arguments: turns[0].Arguments}); err != nil {
		t.Fatal(err)
	}
	opensPerPreflight := countOpens()
	if opensPerPreflight == 0 {
		t.Fatal("preflight did not scan the watched generation file")
	}
	opens := 0
	for i := 0; i < refusals; i++ {
		recordSoftRefusal(t, f)
		opens += countOpens()
	}
	if opens%opensPerPreflight != 0 {
		t.Fatalf("partial generation scan: %d opens, %d per preflight", opens, opensPerPreflight)
	}
	scans := opens / opensPerPreflight
	// Each cycle snapshots Store evidence twelve times and preflights the new refusal three times.
	recomputes := scans - 13*refusals
	t.Logf("%d refusals: %d generation scans, %d replay recomputes", refusals, scans, recomputes)
	if recomputes != 2*refusals {
		t.Fatalf("preflight recomputes grew with historical refusals: got %d, want %d", recomputes, 2*refusals)
	}
}

func TestProductSoftRefusalLiveProgressRejectsTamperedSuffix(t *testing.T) {
	turns := []providerbridge.ScriptTurn{
		{ResponseID: "response-1", CallID: "call-1", Tool: "read", Arguments: json.RawMessage(`{"path":"missing.txt"}`)},
		{ResponseID: "response-2", CallID: "call-2", Tool: "read", Arguments: json.RawMessage(`{"path":"missing.txt"}`)},
	}
	f := startSoftRefusalWorkflow(t, turns)
	recordSoftRefusal(t, f)
	prefix := f.authority.currentLedgerPins().ProviderTranscripts[0].JournalCount
	turn, err := f.conversation.NextTurn(f.ctx)
	if err != nil || turn.ToolCall == nil {
		t.Fatalf("capture second refusal: %+v %v", turn, err)
	}
	proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: turn.ToolCall.ID, Tool: turn.ToolCall.Name, Arguments: turn.ToolCall.Arguments}
	if matched, err := f.conversation.Correlate(f.ctx, proposal); err != nil || !matched.Accepted {
		t.Fatalf("correlate second refusal: %+v %v", matched, err)
	}
	refusal, err := f.durable.productPreflight(proposal)
	if err != nil || refusal == nil {
		t.Fatalf("preflight second refusal: %+v %v", refusal, err)
	}
	err = f.conversation.RecordSoftRefusal(f.ctx, proposal, refusal.ArgumentDigest, "edit_old_text_absent", refusal.Hint, nil)
	if err == nil || !strings.Contains(err.Error(), "soft refusal differs from recomputed sealed-generation preflight") {
		t.Fatalf("tampered suffix accepted live: %v", err)
	}
	if got := f.authority.currentLedgerPins().ProviderTranscripts[0].JournalCount; got <= prefix {
		t.Fatal("second capture did not advance beyond the first refusal prefix")
	}
}
