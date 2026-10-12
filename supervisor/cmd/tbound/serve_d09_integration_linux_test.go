//go:build linux && d09integration

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"
	"testing"

	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/providerbridge"
)

// TestRealGovernedServeRoute is the opt-in actual Pi/D09 integration. It uses
// the real production binder, signed host/image/entrypoint, real Pi worker,
// sealed source, real D09 Bash runner, Store and publication path. Provider turns
// are an explicit offline script; this test makes no network request and carries
// provider_exchange=false / claim_bearing=false.
//
// It skips ONLY when TBOUND_D09_INTEGRATION is unset. When opted in, missing or
// invalid config, source, profile, managed-root registration, runtime, scope,
// proof or task context is a failure. It expects the actual four-tool session
// and verified managed-root publication; it must not claim success otherwise.
func TestRealGovernedServeRoute(t *testing.T) {
	if os.Getenv("TBOUND_D09_INTEGRATION") == "" {
		t.Skip("TBOUND_D09_INTEGRATION is not set; opt-in integration test not requested")
	}
	config := productionServeConfig{
		ProfilePath: os.Getenv("TBOUND_D09_PROFILE"),
		Workspace:   os.Getenv("TBOUND_D09_WORKSPACE"),
		Source:      os.Getenv("TBOUND_D09_SOURCE"),
		TaskID:      "d09-integration-task", TaskRequestID: "d09-integration-request-1",
		TaskPrompt: "Use the proxy tools in order: read task.txt, edit the word ready to done in task.txt, use bash only to append executed to task.txt, then read task.txt and summarize.",
	}
	if config.Source == "" || config.Workspace == "" {
		t.Fatal("opted into D09 integration but TBOUND_D09_SOURCE and TBOUND_D09_WORKSPACE are required")
	}
	verifyIntegrationSource(t, config.Source)
	liveBefore := readIntegrationTask(t, config.Workspace)
	if string(liveBefore) != "ready\n" {
		t.Fatalf("integration live task fixture must start at ready\\n, got %q", liveBefore)
	}
	beforeHash := sha256.Sum256(liveBefore)
	turns := []providerbridge.DevelopmentProviderTurn{
		{ResponseID: "d09-offline-read-g0", CallID: "d09-offline-call-read-g0", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)},
		{ResponseID: "d09-offline-edit-g0", CallID: "d09-offline-call-edit-g0", Tool: "edit", Arguments: json.RawMessage(`{"path":"task.txt","edits":[{"oldText":"ready\n","newText":"done\n"}]}`)},
		{ResponseID: "d09-offline-bash-g1", CallID: "d09-offline-call-bash-g1", Tool: "bash", Arguments: json.RawMessage(`{"command":"printf 'executed\\n' >> task.txt"}`)},
		{ResponseID: "d09-offline-read-g2", CallID: "d09-offline-call-read-g2", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)},
		{ResponseID: "d09-offline-final", Text: "The offline D09 integration task completed."},
	}
	var transcript bytes.Buffer
	runErr := runProductionPiServe(context.Background(), &transcript, config,
		&d09OfflineProviderBinder{inner: newProductionBinder(), turns: turns})
	var receipt productionPiReceipt
	if err := json.Unmarshal(bytes.TrimSpace(transcript.Bytes()), &receipt); err != nil {
		t.Fatalf("opted-in D09 run emitted no bounded receipt: decode=%v run=%v receipt=%q", err, runErr, transcript.String())
	}
	if receipt.Mode != "production" || receipt.ContainmentProfile != piruntime.D09ContainmentProfile ||
		receipt.ProviderExchange || receipt.ClaimBearing || receipt.ProviderTurns != 5 {
		t.Fatalf("integration receipt lost its production/offline claim boundary: %+v", receipt)
	}
	wantTools := []string{"read", "edit", "bash", "read"}
	if !receipt.WorkerReady || !receipt.TaskAdmitted || !receipt.TurnCompleted || receipt.ProposalCount != 4 ||
		len(receipt.ToolSequence) != len(wantTools) || receipt.GenerationCount < 3 || receipt.SourceGeneration == "" ||
		receipt.FinalGeneration == receipt.SourceGeneration || receipt.CommandCellSettlements != 1 {
		t.Fatalf("actual Pi/D09 run lacks durable four-tool, generation, or command-cell evidence: %+v", receipt)
	}
	for i, want := range wantTools {
		if receipt.ToolSequence[i] != want {
			t.Fatalf("actual Pi tool sequence[%d]=%q, want %q: %+v", i, receipt.ToolSequence[i], want, receipt)
		}
	}
	if receipt.Settlement != string(piruntime.SettlementStopped) || !receipt.ExitObserved || !receipt.DescendantsSettled || receipt.TCBProofJournalSequence == 0 {
		t.Fatalf("actual D09 worker settlement/launch proof was not recorded: %+v", receipt)
	}
	if runErr == nil {
		if receipt.Disposition != "PUBLISHED" || receipt.PublicationStatus != "SUCCEEDED" || receipt.PublicationTransactionID == "" || !receipt.Success {
			t.Fatalf("run returned success without verified publication: %+v", receipt)
		}
		liveAfter := readIntegrationTask(t, config.Workspace)
		afterHash := sha256.Sum256(liveAfter)
		if string(liveAfter) != "done\nexecuted\n" || bytes.Equal(beforeHash[:], afterHash[:]) {
			t.Fatalf("published live fixture did not have the expected changed hash/content: before=sha256:%s after=sha256:%s bytes=%q",
				hex.EncodeToString(beforeHash[:]), hex.EncodeToString(afterHash[:]), liveAfter)
		}
		t.Logf("published fixture live hash changed: before=sha256:%s after=sha256:%s", hex.EncodeToString(beforeHash[:]), hex.EncodeToString(afterHash[:]))
		return
	}
	if receipt.Success || receipt.PublicationStatus == "SUCCEEDED" {
		t.Fatalf("failed managed-root publication was reported as success: receipt=%+v err=%v", receipt, runErr)
	}
	t.Fatalf("registered managed-root Pi/D09 read->edit->bash->read did not complete a durable publication: receipt=%+v err=%v",
		receipt, runErr)
}

type d09OfflineProviderBinder struct {
	inner productionRuntimeBinder
	turns []providerbridge.DevelopmentProviderTurn
}

func (b *d09OfflineProviderBinder) BindProductionPiSession(ctx context.Context, config productionServeConfig, profile piruntime.HostProfile) (*productionPiSession, error) {
	if b == nil || b.inner == nil || len(b.turns) == 0 {
		return nil, errors.New("D09 offline integration binder is incomplete")
	}
	session, err := b.inner.BindProductionPiSession(ctx, config, profile)
	if err != nil {
		return nil, err
	}
	conversation, err := providerbridge.NewDevelopmentOfflineConversation(providerbridge.Config{
		ConversationID: session.ConversationID, WorkflowID: session.WorkflowID, ProfileID: session.ProviderProfileID,
		InitialGenerationID: session.Generation.ID(), InitialTreeDigest: session.Generation.TreeDigest(),
		SystemPrompt: session.SystemPrompt, DeveloperPrompt: session.DeveloperPrompt,
	}, session.ProviderJournal, b.turns)
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	session.Authority.mu.Lock()
	if session.Authority.conversation != nil {
		session.Authority.mu.Unlock()
		conversation.Close()
		_ = session.Close()
		return nil, errors.New("production authority already owns a provider conversation")
	}
	session.Authority.conversation = conversation
	session.Authority.mu.Unlock()
	session.offlineProviderConversation = conversation
	return session, nil
}

func verifyIntegrationSource(t *testing.T, rootPath string) {
	t.Helper()
	root, err := openProductionQuiescentSource(rootPath)
	if err != nil {
		t.Fatalf("opted-in source snapshot failed protected read-only preflight: %v", err)
	}
	defer root.Close()
	fd, err := syscall.Openat(int(root.Fd()), "task.txt", syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatalf("open task.txt without following links: %v", err)
	}
	file := os.NewFile(uintptr(fd), "integration-task")
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len("ready\n")) {
		t.Fatalf("task.txt is not the exact registered offline fixture: info=%v err=%v", info, err)
	}
	data := make([]byte, len("ready\n"))
	if _, err := io.ReadFull(file, data); err != nil || string(data) != "ready\n" {
		t.Fatalf("task.txt fixture must contain exactly ready\\n: %q err=%v", data, err)
	}
}

func readIntegrationTask(t *testing.T, rootPath string) []byte {
	t.Helper()
	root, err := os.Open(rootPath)
	if err != nil {
		t.Fatalf("open integration live root: %v", err)
	}
	defer root.Close()
	fd, err := syscall.Openat(int(root.Fd()), "task.txt", syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatalf("open live task.txt without following links: %v", err)
	}
	file := os.NewFile(uintptr(fd), "integration-live-task")
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		t.Fatalf("integration live task.txt is not a bounded regular file: info=%v err=%v", info, err)
	}
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		t.Fatalf("read integration live task.txt: %v", err)
	}
	return data
}
