//go:build linux

package piruntime

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPiWorkerReadinessPreservesChildCloseError(t *testing.T) {
	worker := newPiWorkerEventPumpFixture(t, 1)
	if err := worker.Channels.childEvents.Close(); err != nil {
		t.Fatal(err)
	}

	first := closePiWorkerChildEndsForReadiness(worker.Channels)
	second := worker.Channels.CloseChildEnds()
	if !errors.Is(first, os.ErrClosed) || !errors.Is(second, os.ErrClosed) {
		t.Fatalf("child close failure was lost: readiness=%v retry=%v", first, second)
	}
	if err := worker.Channels.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("channel close did not preserve child close failure: %v", err)
	}
	waitWorkerEventPump(t, worker)
}

type countedCloseWriter struct {
	calls    atomic.Int32
	closeErr error
}

func (w *countedCloseWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *countedCloseWriter) Close() error {
	w.calls.Add(1)
	return w.closeErr
}

func TestPiWorkerSharedInputCloseIsStickyAndRunsOnce(t *testing.T) {
	closeFailure := errors.New("RPC input close failed")
	writer := &countedCloseWriter{closeErr: closeFailure}
	shared := &closeOnceWriter{writer: writer, closer: writer}
	channels := &PiWorkerChannels{controlInput: shared}
	client, err := NewPiRPCClient(strings.NewReader(""), shared)
	if err != nil {
		t.Fatal(err)
	}

	clientDone := make(chan error, 1)
	channelsDone := make(chan error, 1)
	go func() { clientDone <- client.CloseInput() }()
	go func() { channelsDone <- channels.Close() }()

	for i := 0; i < 2; i++ {
		select {
		case err := <-clientDone:
			if !errors.Is(err, closeFailure) {
				t.Fatalf("RPC close error = %v", err)
			}
		case err := <-channelsDone:
			if !errors.Is(err, closeFailure) {
				t.Fatalf("channels close error = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("shared input close did not finish")
		}
	}
	if got := writer.calls.Load(); got != 1 {
		t.Fatalf("underlying close calls = %d, want 1", got)
	}
	if err := channels.Close(); !errors.Is(err, closeFailure) {
		t.Fatalf("repeated channel close lost its error: %v", err)
	}
}

type signalingConn struct {
	net.Conn
	started   chan struct{}
	startOnce sync.Once
}

func (c *signalingConn) Write(p []byte) (int, error) {
	c.startOnce.Do(func() { close(c.started) })
	return c.Conn.Write(p)
}

func TestPiWorkerCloseUnblocksSendControlHoldingChannelMutex(t *testing.T) {
	parent, peer := net.Pipe()
	writer := &signalingConn{Conn: parent, started: make(chan struct{})}
	shared := &closeOnceWriter{writer: writer, closer: writer}
	channels := &PiWorkerChannels{
		controlWriter: writer,
		controlInput:  shared,
		controlSeq:    1,
	}

	writeDone := make(chan error, 1)
	go func() {
		writeDone <- channels.sendControl(context.Background(), PiWorkerControl{
			Type: "stop", RequestID: "request-1",
		})
	}()
	select {
	case <-writer.started:
	case <-time.After(time.Second):
		_ = peer.Close()
		t.Fatal("control write did not start")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- channels.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("close channels: %v", err)
		}
	case <-time.After(time.Second):
		// Release the pipe so a regression cannot leave either goroutine stuck.
		_ = peer.Close()
		select {
		case <-writeDone:
		case <-time.After(time.Second):
			t.Fatal("control write remained blocked after releasing the peer")
		}
		select {
		case <-closeDone:
		case <-time.After(time.Second):
			t.Fatal("channel close remained blocked after releasing the peer")
		}
		t.Fatal("channel close waited on the mutex held by a blocked control write")
	}
	select {
	case err := <-writeDone:
		if err == nil {
			t.Fatal("control write unexpectedly succeeded without a peer reader")
		}
	case <-time.After(time.Second):
		_ = peer.Close()
		t.Fatal("closing the shared endpoint did not unblock control write")
	}
	_ = peer.Close()
}

type stoppedTestSettler struct{}

func (stoppedTestSettler) Settle(context.Context) (bool, error) { return true, nil }

func TestPiCLIWorkerCloseSharesRPCCloseAndReturnsCloseError(t *testing.T) {
	for _, closeFailure := range []error{nil, errors.New("control endpoint close failed")} {
		t.Run(map[bool]string{true: "close error", false: "successful close"}[closeFailure != nil], func(t *testing.T) {
			writer := &countedCloseWriter{closeErr: closeFailure}
			shared := &closeOnceWriter{writer: writer, closer: writer}
			channels := &PiWorkerChannels{controlInput: shared}
			rpc, err := NewPiRPCClient(strings.NewReader(""), shared)
			if err != nil {
				t.Fatal(err)
			}

			completion := make(chan struct{})
			close(completion)
			process := &Process{
				cmd:        &exec.Cmd{Process: &os.Process{}},
				settler:    stoppedTestSettler{},
				completion: completion,
				settlement: Settlement{State: SettlementRunning},
			}
			session, err := NewSession(process)
			if err != nil {
				t.Fatal(err)
			}
			readerDone := make(chan struct{})
			close(readerDone)
			worker := &PiCLIWorker{
				Session: session, Channels: channels, rpc: rpc, readerDone: readerDone,
			}

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			settlement, closeErr := worker.Close(ctx)
			if settlement.State != SettlementStopped || !settlement.DescendantsSettled {
				t.Fatalf("worker did not settle: %+v", settlement)
			}
			if closeFailure == nil {
				if closeErr != nil {
					t.Fatalf("successful close returned error: %v", closeErr)
				}
			} else if !errors.Is(closeErr, closeFailure) {
				t.Fatalf("worker close lost endpoint error: %v", closeErr)
			}
			if got := writer.calls.Load(); got != 1 {
				t.Fatalf("underlying endpoint close calls = %d, want 1", got)
			}
		})
	}
}

func TestPiWorkerFailedLaunchUnknownRetainsChannels(t *testing.T) {
	worker := newPiWorkerEventPumpFixture(t, 1)
	launchErr := stopFailedPiLaunch(&Process{}, worker.Channels, errors.New("startup failed"))
	var typed *PiWorkerLaunchError
	if !errors.As(launchErr, &typed) || typed.Settlement.State != SettlementUnknown {
		t.Fatalf("failed launch did not retain UNKNOWN settlement: %v", launchErr)
	}
	worker.Channels.mu.Lock()
	closed := worker.Channels.closed
	worker.Channels.mu.Unlock()
	if closed {
		t.Fatal("channels were released before UNKNOWN cleanup settled")
	}
	if err := worker.Channels.Close(); err != nil {
		t.Fatalf("test cleanup: %v", err)
	}
	waitWorkerEventPump(t, worker)
}
