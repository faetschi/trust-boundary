package providerbridge

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"
)

func TestInheritedChannelServesOnlyBrokerTurnRequests(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	doer := &fixtureDoer{responses: []fixtureResponse{{responseID: "response-channel", text: "bounded response"}}}
	conversation := newFixtureConversation(t, &memoryJournal{}, doer, "g0", "tree-g0")
	if err := conversation.AdmitPrompt("task-channel", "trusted host-admitted prompt"); err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- Serve(context.Background(), serverConn, conversation) }()
	if err := writeChannelTestFrame(clientConn, map[string]any{
		"version": bridgeSchemaVersion, "sequence": 1, "kind": "next", "request_id": "request-1",
	}); err != nil {
		t.Fatal(err)
	}
	frame, err := readChannelTestFrame(clientConn)
	if err != nil {
		t.Fatal(err)
	}
	if frame["kind"] != "turn" || frame["request_id"] != "request-1" {
		t.Fatalf("channel response=%v", frame)
	}
	turn, ok := frame["turn"].(map[string]any)
	if !ok || turn["assistant_text"] != "bounded response" || turn["model"] != ApprovedModel {
		t.Fatalf("channel turn=%v", frame["turn"])
	}
	// A new sequence cannot replay an already consumed request identity.
	if err := writeChannelTestFrame(clientConn, map[string]any{
		"version": bridgeSchemaVersion, "sequence": 2, "kind": "next", "request_id": "request-1",
	}); err != nil {
		t.Fatal(err)
	}
	_ = clientConn.Close()
	select {
	case err := <-serveDone:
		if err == nil {
			t.Fatal("replayed provider request did not close the bridge")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("provider bridge did not stop after replay")
	}
}

func TestInheritedChannelCancellationClosesAndWithholdsTurn(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	started := make(chan struct{})
	doer := &fixtureDoer{
		responses: []fixtureResponse{{responseID: "response-canceled", text: "must not be released"}},
		block:     make(chan struct{}), started: started,
	}
	journal := &memoryJournal{}
	conversation := newFixtureConversation(t, journal, doer, "g0", "tree-g0")
	if err := conversation.AdmitPrompt("task-cancel", "synthetic prompt"); err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- Serve(context.Background(), serverConn, conversation) }()
	if err := writeChannelTestFrame(clientConn, map[string]any{
		"version": bridgeSchemaVersion, "sequence": 1, "kind": "next", "request_id": "request-cancel",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("fixture HTTP Doer was not called")
	}
	if err := writeChannelTestFrame(clientConn, map[string]any{
		"version": bridgeSchemaVersion, "sequence": 2, "kind": "cancel", "request_id": "request-cancel",
	}); err != nil {
		t.Fatal(err)
	}
	frame, err := readChannelTestFrame(clientConn)
	if err != nil {
		t.Fatal(err)
	}
	if frame["kind"] != "error" {
		t.Fatalf("canceled turn response=%v", frame)
	}
	_ = clientConn.Close()
	select {
	case serveErr := <-serveDone:
		if serveErr == nil {
			t.Fatal("canceled bridge unexpectedly remained open")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled provider bridge did not stop")
	}
	for _, event := range journal.snapshot() {
		if event.Kind == "provider_exchange_unknown" {
			return
		}
	}
	t.Fatal("canceled in-flight HTTP exchange lacks durable UNKNOWN evidence")
}

func writeChannelTestFrame(writer io.Writer, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(body)))
	if err := writeAll(writer, prefix[:]); err != nil {
		return err
	}
	return writeAll(writer, body)
}

func readChannelTestFrame(reader io.Reader) (map[string]any, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(reader, prefix[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(prefix[:])
	if length == 0 || length > 1<<20 {
		return nil, io.ErrShortBuffer
	}
	body := make([]byte, int(length))
	if _, err := io.ReadFull(reader, body); err != nil {
		return nil, err
	}
	var frame map[string]any
	err := json.Unmarshal(body, &frame)
	return frame, err
}
