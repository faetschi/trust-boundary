package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestReadOnlySourceFlagValidation(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"reject remote bind", []string{"--addr", "0.0.0.0:8787", "--journal", "journal.jsonl"}, "loopback"},
		{"test stream requires manifest", []string{"--test-json", "events.jsonl"}, "--run-manifest"},
		{"manifest requires test stream", []string{"--run-manifest", "run.json"}, "--test-json"},
		{"history cannot alias source", []string{"--history", "journal.jsonl", "--journal", "journal.jsonl"}, "must not name"},
		{"requires source", nil, "at least one"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			err := run(test.args, &stderr)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("run error = %v; want %q", err, test.want)
			}
		})
	}
}

func TestLoopbackAddressRequiresLiteralLoopbackIP(t *testing.T) {
	for address, want := range map[string]bool{
		"127.0.0.1:8787":  true,
		"[::1]:8787":      true,
		"localhost:8787":  false,
		"0.0.0.0:8787":    false,
		"192.0.2.10:8787": false,
		"127.0.0.1":       false,
	} {
		if got := loopbackAddress(address); got != want {
			t.Errorf("loopbackAddress(%q) = %v; want %v", address, got, want)
		}
	}
}
