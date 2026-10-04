package ipc

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"

	"tbound/supervisor/internal/broker/protocol"
)

const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestProposalResultPipeAndCleanClose(t *testing.T) {
	client, server, err := NewPipe(testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	defer server.Close()
	proposal := protocol.Proposal{
		SchemaVersion: protocol.ProposalSchemaVersion,
		ToolCallID:    "call-1",
		Tool:          "read",
		Arguments:     json.RawMessage(`{"path":"README.md"}`),
	}
	writeErr := make(chan error, 1)
	go func() { writeErr <- client.SendProposal(proposal) }()
	got, err := server.ReceiveProposal()
	if err != nil {
		t.Fatal(err)
	}
	if got.ToolCallID != proposal.ToolCallID || got.Tool != proposal.Tool || string(got.Arguments) != string(proposal.Arguments) {
		t.Fatalf("proposal changed in IPC: got %+v, want %+v", got, proposal)
	}
	if err := <-writeErr; err != nil {
		t.Fatal(err)
	}
	result := protocol.Result{
		SchemaVersion: protocol.ResultSchemaVersion,
		ToolCallID:    proposal.ToolCallID,
		Tool:          proposal.Tool,
		Sequence:      1,
		Verdict:       "DENY",
		ReasonCode:    "policy_default_deny",
		PolicyDigest:  "tbound-policy/v1:sha256:" + strings.Repeat("a", 64),
	}
	go func() { writeErr <- server.SendResult(result) }()
	gotResult, err := client.ReceiveResult()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotResult, result) {
		t.Fatalf("result changed in IPC: got %+v, want %+v", gotResult, result)
	}
	if err := <-writeErr; err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ReceiveProposal(); !errors.Is(err, ErrClosed) {
		t.Fatalf("clean EOF should report ErrClosed, got %v", err)
	}
}

func TestOversizedLengthRejectedBeforePayloadAllocation(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	server, err := NewServer(serverConn, testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	var prefix [PrefixBytes]byte
	binary.BigEndian.PutUint32(prefix[:], MaxMessageBytes+1)
	writeErr := make(chan error, 1)
	go func() {
		_, err := clientConn.Write(prefix[:])
		writeErr <- err
	}()
	if _, err := server.ReceiveProposal(); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversized frame should fail closed: %v", err)
	}
	if err := <-writeErr; err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("unexpected writer error: %v", err)
	}
}

func TestTruncatedPrefixAndPayloadRejected(t *testing.T) {
	t.Run("prefix", func(t *testing.T) {
		clientConn, serverConn := net.Pipe()
		defer clientConn.Close()
		server, err := NewServer(serverConn, testToken)
		if err != nil {
			t.Fatal(err)
		}
		defer server.Close()
		go func() {
			_, _ = clientConn.Write([]byte{0, 0})
			_ = clientConn.Close()
		}()
		if _, err := server.ReceiveProposal(); !errors.Is(err, ErrMalformedFrame) {
			t.Fatalf("truncated prefix should fail closed: %v", err)
		}
	})
	t.Run("payload", func(t *testing.T) {
		clientConn, serverConn := net.Pipe()
		defer clientConn.Close()
		server, err := NewServer(serverConn, testToken)
		if err != nil {
			t.Fatal(err)
		}
		defer server.Close()
		var prefix [PrefixBytes]byte
		binary.BigEndian.PutUint32(prefix[:], 32)
		go func() {
			_, _ = clientConn.Write(prefix[:])
			_, _ = clientConn.Write([]byte(`{"version"`))
			_ = clientConn.Close()
		}()
		if _, err := server.ReceiveProposal(); !errors.Is(err, ErrMalformedFrame) {
			t.Fatalf("truncated payload should fail closed: %v", err)
		}
	})
}

func TestBindingMismatchDuplicateUnknownAndReplayRejected(t *testing.T) {
	t.Run("binding mismatch", func(t *testing.T) {
		assertRejectedFrame(t, wire(testToken[:len(testToken)-1]+"0", 1, "proposal", validProposalJSON()), ErrBindingMismatch)
	})
	t.Run("duplicate field", func(t *testing.T) {
		raw := fmt.Sprintf(`{"version":%q,"version":%q,"binding_token":%q,"sequence":1,"kind":"proposal","payload":%s}`,
			WireVersion, WireVersion, testToken, validProposalJSON())
		assertRejectedRaw(t, []byte(raw), ErrMalformedFrame)
	})
	t.Run("unknown field", func(t *testing.T) {
		raw := fmt.Sprintf(`{"version":%q,"binding_token":%q,"sequence":1,"kind":"proposal","payload":%s,"admin":true}`,
			WireVersion, testToken, validProposalJSON())
		assertRejectedRaw(t, []byte(raw), ErrMalformedFrame)
	})
	t.Run("unknown kind", func(t *testing.T) {
		assertRejectedFrame(t, wire(testToken, 1, "admin", validProposalJSON()), ErrUnsupportedKind)
	})
	t.Run("replayed frame", func(t *testing.T) {
		clientConn, serverConn := net.Pipe()
		defer clientConn.Close()
		server, err := NewServer(serverConn, testToken)
		if err != nil {
			t.Fatal(err)
		}
		defer server.Close()
		raw := wire(testToken, 1, "proposal", validProposalJSON())
		transmit(t, clientConn, server, raw, true, nil)
		transmit(t, clientConn, server, raw, false, ErrReplay)
	})
}

func TestUnknownDirectionAndMalformedPayloadFailClosed(t *testing.T) {
	t.Run("wrong direction", func(t *testing.T) {
		clientConn, serverConn := net.Pipe()
		defer clientConn.Close()
		server, err := NewServer(serverConn, testToken)
		if err != nil {
			t.Fatal(err)
		}
		defer server.Close()
		transmit(t, clientConn, server, wire(testToken, 1, "result", validResultJSON()), false, ErrUnexpectedKind)
		_ = clientConn.Close()
	})
	t.Run("bad proposal schema", func(t *testing.T) {
		bad := []byte(`{"schema_version":"tbound-proposal/v1","tool_call_id":"call-1","tool":"read","arguments":{"path":"x"},"extra":true}`)
		assertRejectedFrame(t, wire(testToken, 1, "proposal", bad), ErrMalformedFrame)
	})
}

func TestBindingTokenValidationAndGeneratedToken(t *testing.T) {
	if _, _, err := NewPipe("not-a-token"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("invalid token accepted: %v", err)
	}
	token, err := NewBindingToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != BindingTokenHexBytes {
		t.Fatalf("generated token length = %d", len(token))
	}
}

func assertRejectedFrame(t *testing.T, raw []byte, want error) {
	t.Helper()
	assertRejectedRaw(t, raw, want)
}

func assertRejectedRaw(t *testing.T, raw []byte, want error) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	server, err := NewServer(serverConn, testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	transmit(t, clientConn, server, raw, false, want)
}

func transmit(t *testing.T, conn net.Conn, server *Server, raw []byte, accepted bool, want error) {
	t.Helper()
	errCh := make(chan error, 1)
	go func() { errCh <- sendFrame(conn, raw) }()
	_, receiveErr := server.ReceiveProposal()
	writeErr := <-errCh
	if accepted {
		if receiveErr != nil {
			t.Fatalf("valid frame rejected: %v", receiveErr)
		}
		if writeErr != nil {
			t.Fatalf("valid frame writer failed: %v", writeErr)
		}
		return
	}
	if receiveErr == nil {
		t.Fatal("invalid frame was accepted")
	}
	if !errors.Is(receiveErr, ErrMalformedFrame) && !errors.Is(receiveErr, ErrBindingMismatch) &&
		!errors.Is(receiveErr, ErrUnsupportedKind) && !errors.Is(receiveErr, ErrUnexpectedKind) &&
		!errors.Is(receiveErr, ErrReplay) {
		t.Fatalf("unexpected failure class: %v", receiveErr)
	}
	if want != nil && !errors.Is(receiveErr, want) {
		t.Fatalf("got %v, want error matching %v", receiveErr, want)
	}
	if writeErr != nil && !errors.Is(writeErr, net.ErrClosed) && !errors.Is(writeErr, io.ErrClosedPipe) {
		t.Fatalf("unexpected writer error: %v", writeErr)
	}
}

func sendFrame(conn net.Conn, raw []byte) error {
	var prefix [PrefixBytes]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(raw)))
	if err := writeAll(conn, prefix[:]); err != nil {
		return err
	}
	return writeAll(conn, raw)
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func wire(token string, sequence uint64, kind string, payload []byte) []byte {
	return []byte(fmt.Sprintf(`{"version":%q,"binding_token":%q,"sequence":%d,"kind":%q,"payload":%s}`,
		WireVersion, token, sequence, kind, payload))
}

func validProposalJSON() []byte {
	return []byte(`{"schema_version":"tbound-proposal/v1","tool_call_id":"call-1","tool":"read","arguments":{"path":"x"}}`)
}

func validResultJSON() []byte {
	return []byte(`{"schema_version":"tbound-result/v1","tool_call_id":"call-1","tool":"read","sequence":1,"verdict":"DENY","reason_code":"policy_default_deny","policy_digest":"tbound-policy/v1:sha256:` + strings.Repeat("a", 64) + `"}`)
}
