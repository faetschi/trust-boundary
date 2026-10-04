package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/ipc"
)

func TestUnixSocketSyntheticSupervisorRoundTrip(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("real Unix-domain socket integration runs only on Linux")
	}

	socketDir, err := os.MkdirTemp("", "tb-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(socketDir); err != nil {
			t.Errorf("remove temporary socket directory: %v", err)
		}
	})
	if err := os.Chmod(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(socketDir)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 || !dirInfo.IsDir() {
		t.Fatalf("test socket directory mode = %04o; want 0700", dirInfo.Mode().Perm())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var transcript bytes.Buffer
	serveDone := make(chan error, 1)
	go func() { serveDone <- runSyntheticListener(ctx, socketDir, &transcript) }()

	socketPath := filepath.Join(socketDir, smokeSocketName)
	tokenPath := smokeTokenFilePath(socketDir)
	if err := waitForSmokeEndpoint(ctx, socketPath, tokenPath, serveDone); err != nil {
		t.Fatal(err)
	}
	socketInfo, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if socketInfo.Mode()&os.ModeSocket == 0 || socketInfo.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v (%04o); want Unix socket mode 0600", socketInfo.Mode(), socketInfo.Mode().Perm())
	}
	tokenInfo, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if tokenInfo.Mode().Perm() != 0o600 {
		t.Fatalf("binding token file mode = %04o; want 0600", tokenInfo.Mode().Perm())
	}
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	client, err := ipc.DialUnix(socketPath, string(token))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	for index, proposal := range smokeProposals() {
		if err := client.SendProposal(proposal); err != nil {
			t.Fatalf("send %s proposal: %v", proposal.Tool, err)
		}
		result, err := client.ReceiveResult()
		if err != nil {
			t.Fatalf("receive %s result: %v", proposal.Tool, err)
		}
		digest, err := protocol.CanonicalArgumentsDigest(proposal.Tool, proposal.Arguments)
		if err != nil {
			t.Fatal(err)
		}
		expectedCall := smokeCalls[index]
		if result.Verdict != string(gate.Allow) || result.ReasonCode != "policy_rule_allow" ||
			result.ToolCallID != expectedCall.callID || result.Tool != expectedCall.tool ||
			result.Sequence != uint64(index+1) || result.CanonicalArgumentsDigest != digest ||
			result.ResponseID == nil || result.ResponseID.Opaque != expectedCall.responseID ||
			result.Output == nil || string(result.Output) != `{"status":"stubbed-no-effect"}` {
			t.Fatalf("unexpected %s integration result: %+v", proposal.Tool, result)
		}
	}

	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("synthetic socket listener failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("synthetic socket listener did not finish: %v", ctx.Err())
	}

	lines := strings.Split(strings.TrimSpace(transcript.String()), "\n")
	if len(lines) != len(smokeCalls) {
		t.Fatalf("transcript has %d records; want %d: %s", len(lines), len(smokeCalls), transcript.String())
	}
	for index, line := range lines {
		var entry transcriptEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode transcript record %d: %v", index+1, err)
		}
		call := smokeCalls[index]
		if entry.Proposal.ToolCallID != call.callID || entry.Proposal.Tool != call.tool ||
			!entry.Correlation.Accepted || entry.Correlation.ReasonCode != "matched" ||
			entry.Decision.Verdict != gate.Allow || entry.Result.Verdict != string(gate.Allow) ||
			entry.Result.Sequence != uint64(index+1) {
			t.Fatalf("unexpected transcript record %d: %+v", index+1, entry)
		}
	}
}

func waitForSmokeEndpoint(ctx context.Context, socketPath, tokenPath string, serveDone <-chan error) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, socketErr := os.Lstat(socketPath); socketErr == nil {
			if _, tokenErr := os.Stat(tokenPath); tokenErr == nil {
				return nil
			}
		}
		select {
		case err := <-serveDone:
			if err == nil {
				return fmt.Errorf("synthetic socket listener exited before becoming ready")
			}
			return fmt.Errorf("synthetic socket listener failed before becoming ready: %w", err)
		default:
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
