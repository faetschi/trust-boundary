//go:build linux

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Each row is one eligible decoder site. The cause text names the site, so a refusal from another site with the
// same tool cannot pass.
func TestNoEffectDurableArgumentRefusalTagsEachEligibleDecoderSite(t *testing.T) {
	for _, tc := range []struct {
		name, tool, arguments, cause string
	}{
		{"write extra member", "write", `{"path":"a.txt","content":"x","extra":1}`, "write arguments"},
		{"write case-variant member", "write", `{"path":"a.txt","Path":"b.txt","content":"x"}`, "write arguments"},
		{"write wrong value type", "write", `{"path":1,"content":"x"}`, "write arguments"},
		{"write trailing JSON", "write", `{"path":"a.txt","content":"x"} {}`, "write arguments"},
		{"write empty path", "write", `{"path":"","content":"x"}`, "write arguments require a path"},
		{"edit extra member", "edit", `{"path":"a.txt","edits":[],"extra":1}`, "edit arguments"},
		{"edit empty edits", "edit", `{"path":"a.txt","edits":[]}`, "edit arguments require a path and at least one replacement"},
		{"edit empty path", "edit", `{"path":"","edits":[{"oldText":"a","newText":"b"}]}`, "edit arguments require a path and at least one replacement"},
		{"edit nested extra member", "edit", `{"path":"a.txt","edits":[{"oldText":"a","newText":"b","extra":1}]}`, "edits[0]"},
		{"edit nested wrong value type", "edit", `{"path":"a.txt","edits":[{"oldText":1,"newText":"b"}]}`, "edits[0]"},
		{"bash extra member", "bash", `{"command":"true","extra":1}`, "bash arguments"},
		{"bash trailing JSON", "bash", `{"command":"true"} {}`, "bash arguments"},
		{"bash empty command", "bash", `{"command":""}`, "bash arguments require a command"},
		{"read extra member", "read", `{"path":"a.txt","extra":1}`, "read arguments"},
		{"read empty path", "read", `{"path":""}`, "read arguments require a path"},
		{"read zero offset", "read", `{"path":"a.txt","offset":0}`, "read offset must be a positive integer"},
		{"read negative offset", "read", `{"path":"a.txt","offset":-3}`, "read offset must be a positive integer"},
		{"read fractional limit", "read", `{"path":"a.txt","limit":1.5}`, "read limit must be a positive integer"},
		{"write path outside the workspace", "write", `{"path":"/etc/passwd","content":"x"}`, "path is outside the workspace"},
		{"edit path outside the workspace", "edit", `{"path":"../secret","edits":[{"oldText":"a","newText":"b"}]}`, "path is outside the workspace"},
		{"read path outside the workspace", "read", `{"path":"../secret"}`, "path is outside the workspace"},
		{"read absolute path under no workspace root", "read", `{"path":"/workspace/a.txt"}`, "path is outside the workspace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refusal := durableArgumentRefusal(tc.tool, json.RawMessage(tc.arguments))
			if refusal == nil || refusal.Tool != tc.tool || !strings.Contains(refusal.Err.Error(), tc.cause) {
				t.Fatalf("eligible argument refusal is not *protocol.NoEffectError for %s with cause %q: %+v", tc.tool, tc.cause, refusal)
			}
		})
	}
}

// Valid arguments, workspace paths and paths only the Store refuses never become a no-effect refusal here: their
// outcome is decided by the Store or by execution, which the marker must not claim.
func TestNoEffectDurableArgumentRefusalLeavesPathAndValidArgumentsUntyped(t *testing.T) {
	for _, tc := range []struct {
		name, tool, arguments string
	}{
		{"absolute path under the command workdir", "read", `{"path":"/work/a.txt"}`},
		{"path the Store refuses", "write", `{"path":"src\\a.txt","content":"x"}`},
		{"empty old text the Store refuses", "edit", `{"path":"a.txt","edits":[{"oldText":"","newText":"b"}]}`},
		{"valid write", "write", `{"path":"a.txt","content":"x"}`},
		{"valid read window", "read", `{"path":"a.txt","offset":2,"limit":3}`},
		{"read limit beyond the file bound", "read", `{"path":"a.txt","limit":1e300}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if refusal := durableArgumentRefusal(tc.tool, json.RawMessage(tc.arguments)); refusal != nil {
				t.Fatalf("a path or valid argument was refused as no-effect: %v", refusal)
			}
		})
	}
}
