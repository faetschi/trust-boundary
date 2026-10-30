//go:build linux

package piruntime

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestOwnedStdoutPipeNativeChild(t *testing.T) {
	if os.Getenv("TBOUND_OWNED_STDOUT_CHILD") != "1" {
		return
	}
	line := []byte("{\"id\":\"final-1\",\"type\":\"response\",\"command\":\"prompt\",\"success\":true}\n")
	if _, err := os.Stdout.Write(line); err != nil {
		os.Exit(1)
	}
	// Bypass the test harness's trailing PASS output; stdout is the RPC stream.
	os.Exit(0)
}

func TestOwnedStdoutPipeSurvivesWaitBeforeRPCRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOwnedStdoutPipeNativeChild$")
	cmd.Env = append(os.Environ(), "TBOUND_OWNED_STDOUT_CHILD=1")
	stdoutPipe, err := attachOwnedStdout(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if err := stdoutPipe.Start(); err != nil {
		t.Fatalf("start native stdout child: %v", err)
	}
	defer stdoutPipe.CloseReader()

	// The process waiter is the sole owner of Wait, and Wait finishes before
	// the RPC client touches the buffered final record.
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait for native stdout child: %v", err)
	}
	if err := stdoutPipe.CloseWriter(); err != nil {
		t.Fatalf("close parent stdout writer: %v", err)
	}

	client, err := NewPiRPCClient(stdoutPipe.reader, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	type readResult struct {
		record PiRPCRecord
		err    error
	}
	readRecord := func() (PiRPCRecord, error) {
		result := make(chan readResult, 1)
		go func() {
			record, err := client.ReadRecord()
			result <- readResult{record: record, err: err}
		}()
		select {
		case got := <-result:
			return got.record, got.err
		case <-ctx.Done():
			_ = stdoutPipe.CloseReader()
			return PiRPCRecord{}, ctx.Err()
		}
	}

	record, err := readRecord()
	if err != nil {
		t.Fatalf("read final correlated record after Wait: %v", err)
	}
	if record.ID != "final-1" || record.Type != "response" || record.Command != "prompt" ||
		record.Success == nil || !*record.Success {
		t.Fatalf("unexpected final RPC record: %+v", record)
	}
	if _, err := readRecord(); !errors.Is(err, io.EOF) {
		t.Fatalf("stdout after draining final record = %v, want EOF", err)
	}

	if err := stdoutPipe.CloseWriter(); err != nil {
		t.Fatalf("repeated parent writer close: %v", err)
	}
	if err := stdoutPipe.CloseReader(); err != nil {
		t.Fatalf("repeated parent reader close: %v", err)
	}
}

func TestOwnedStdoutReaderCloseKeepsFirstCloseError(t *testing.T) {
	file, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reader := &ownedStdoutReader{file: file}
	first := reader.Close()
	second := reader.Close()
	if !errors.Is(first, os.ErrClosed) || second != first {
		t.Fatalf("close errors first=%v second=%v; want same sticky os.ErrClosed", first, second)
	}
}
