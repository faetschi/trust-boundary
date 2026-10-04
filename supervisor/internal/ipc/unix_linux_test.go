//go:build linux

package ipc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/broker/protocol"
)

func TestUnixSocketTransportIsPrivateAndCarriesBoundProposal(t *testing.T) {
	socketDir, err := os.MkdirTemp("", "ipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	path := filepath.Join(socketDir, "session.sock")
	listener, err := ListenUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket permissions = %04o, want 0600", info.Mode().Perm())
	}
	sharedDir, err := os.MkdirTemp("/tmp", "tbound-unsafe-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sharedDir) })
	if err := os.Chmod(sharedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ListenUnix(filepath.Join(sharedDir, "session.sock")); err == nil {
		t.Fatal("listener accepted a socket under a shared parent directory")
	}

	serverCh := make(chan *Server, 1)
	acceptErr := make(chan error, 1)
	go func() {
		server, err := listener.Accept(testToken)
		if err != nil {
			acceptErr <- err
			return
		}
		serverCh <- server
	}()
	client, err := DialUnix(path, testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var server *Server
	select {
	case server = <-serverCh:
	case err := <-acceptErr:
		t.Fatal(err)
	}
	defer server.Close()

	proposal := protocol.Proposal{
		SchemaVersion: protocol.ProposalSchemaVersion,
		ToolCallID:    "call-unix",
		Tool:          "read",
		Arguments:     json.RawMessage(`{"path":"README.md"}`),
	}
	writeErr := make(chan error, 1)
	go func() { writeErr <- client.SendProposal(proposal) }()
	got, err := server.ReceiveProposal()
	if err != nil {
		t.Fatal(err)
	}
	if got.ToolCallID != proposal.ToolCallID || got.Tool != proposal.Tool {
		t.Fatalf("Unix transport changed proposal: %+v", got)
	}
	if err := <-writeErr; err != nil {
		t.Fatal(err)
	}
	if _, err := listener.Accept(testToken); err != ErrClosed {
		t.Fatalf("Unix listener accepted a second session connection: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("socket path remained after close: %v", err)
	}
}
