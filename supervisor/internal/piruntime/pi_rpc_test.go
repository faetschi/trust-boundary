package piruntime

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestPiRPCJSONLPromptAbortAndAgentSettled(t *testing.T) {
	var input bytes.Buffer
	output := strings.NewReader("{\"id\":\"p1\",\"type\":\"response\",\"command\":\"prompt\",\"success\":true}\n" +
		"{\"type\":\"agent_end\"}\n{\"type\":\"agent_settled\"}\r\n")
	client, err := NewPiRPCClient(output, &input)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SendPrompt("p1", "hello\u2028world"); err != nil {
		t.Fatal(err)
	}
	if err := client.Abort("p2"); err != nil {
		t.Fatal(err)
	}
	if got, want := input.String(), "{\"id\":\"p1\",\"type\":\"prompt\",\"message\":\"hello\\u2028world\"}\n{\"id\":\"p2\",\"type\":\"abort\"}\n"; got != want {
		t.Fatalf("commands = %q, want %q", got, want)
	}
	for _, want := range []string{"response", "agent_end", "agent_settled"} {
		record, err := client.ReadRecord()
		if err != nil {
			t.Fatalf("read %s: %v", want, err)
		}
		if record.Type != want {
			t.Fatalf("type = %q, want %q", record.Type, want)
		}
	}
	if _, err := client.ReadRecord(); err != io.EOF {
		t.Fatalf("terminal read error = %v, want EOF", err)
	}
}

func TestPiRPCRejectsMalformedOrUnboundedRecords(t *testing.T) {
	for _, input := range []string{"not json\n", "{\"type\":\"response\",\"id\":\"x\"}\n", strings.Repeat("x", PiRPCMaxRecordBytes+2) + "\n"} {
		client, err := NewPiRPCClient(strings.NewReader(input), io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.ReadRecord(); err == nil {
			t.Fatalf("accepted invalid RPC record %q", input[:min(len(input), 32)])
		}
	}
}
