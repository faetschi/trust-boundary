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

type retryCloseReader struct {
	reader   *strings.Reader
	calls    atomic.Int32
	firstErr error
}

func (r *retryCloseReader) Read(p []byte) (int, error) { return r.reader.Read(p) }
func (r *retryCloseReader) Close() error {
	if r.calls.Add(1) == 1 {
		return r.firstErr
	}
	return nil
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

func TestPiCLIWorkerCloseWaitsForDrainAndRetriesStdoutClose(t *testing.T) {
	completion := make(chan struct{})
	close(completion)
	process := &Process{
		cmd:        &exec.Cmd{Process: &os.Process{}},
		settler:    stoppedTestSettler{},
		completion: completion,
	}
	session, err := NewSession(process)
	if err != nil {
		t.Fatal(err)
	}
	if settlement, err := session.Close(context.Background()); err != nil ||
		settlement.State != SettlementStopped || !settlement.DescendantsSettled {
		t.Fatalf("prepare settled worker: settlement=%+v err=%v", settlement, err)
	}

	closeFailure := errors.New("stdout close failed")
	stdout := &retryCloseReader{reader: strings.NewReader(""), firstErr: closeFailure}
	readerDone := make(chan struct{})
	channels := &PiWorkerChannels{}
	worker := &PiCLIWorker{
		Session: session, Channels: channels, readerDone: readerDone,
		stdoutCloser: stdout,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	settlement, err := worker.Close(ctx)
	if settlement.State != SettlementStopped || !settlement.DescendantsSettled {
		t.Fatalf("timed-out drain changed process settlement: %+v", settlement)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close before output drain = %v, want deadline error", err)
	}
	if got := stdout.calls.Load(); got != 0 {
		t.Fatalf("stdout close calls before output drain = %d, want 0", got)
	}
	if channels.closed {
		t.Fatal("channels closed before output drain completed")
	}

	close(readerDone)
	_, err = worker.Close(context.Background())
	if !errors.Is(err, closeFailure) {
		t.Fatalf("retry did not preserve stdout close error: %v", err)
	}
	if got := stdout.calls.Load(); got != 1 {
		t.Fatalf("stdout close attempts after retry = %d, want 1", got)
	}

	_, err = worker.Close(context.Background())
	if !errors.Is(err, closeFailure) {
		t.Fatalf("repeated close lost sticky stdout close error: %v", err)
	}
	if got := stdout.calls.Load(); got != 2 {
		t.Fatalf("stdout close attempts after retry succeeded = %d, want 2", got)
	}
}

func TestPiCLIWorkerCloseRetainsOwnersWhenStoppedSessionReturnsError(t *testing.T) {
	sessionFailure := errors.New("session cleanup reported an observation error")
	completion := make(chan struct{})
	close(completion)
	process := &Process{
		cmd:        &exec.Cmd{Process: &os.Process{}},
		settler:    stoppedTestSettler{},
		completion: completion,
		waitResult: waitResult{observationErr: sessionFailure},
	}
	session, err := NewSession(process)
	if err != nil {
		t.Fatal(err)
	}

	stdout := &retryCloseReader{reader: strings.NewReader("")}
	readerDone := make(chan struct{})
	close(readerDone)
	channels := &PiWorkerChannels{}
	worker := &PiCLIWorker{
		Session: session, Channels: channels, readerDone: readerDone,
		stdoutCloser: stdout,
	}

	settlement, err := worker.Close(context.Background())
	if settlement.State != SettlementStopped || !settlement.ExitObserved ||
		!settlement.DescendantsSettled {
		t.Fatalf("session did not report measured STOPPED: %+v", settlement)
	}
	if !errors.Is(err, sessionFailure) {
		t.Fatalf("worker close lost session error: %v", err)
	}
	if got := stdout.calls.Load(); got != 0 {
		t.Fatalf("stdout close calls after session error = %d, want 0", got)
	}
	channels.mu.Lock()
	closed := channels.closed
	channels.mu.Unlock()
	if closed {
		t.Fatal("channels closed despite the session cleanup error")
	}
}

func TestStopFailedPiLaunchClosesStdoutOnlyAfterSettlement(t *testing.T) {
	completion := make(chan struct{})
	close(completion)
	stdout := &retryCloseReader{reader: strings.NewReader("")}
	process := &Process{
		cmd:        &exec.Cmd{Process: &os.Process{}},
		stdout:     stdout,
		settler:    stoppedTestSettler{},
		completion: completion,
	}

	err := stopFailedPiLaunch(process, &PiWorkerChannels{}, errors.New("startup failed"))
	var launchErr *PiWorkerLaunchError
	if !errors.As(err, &launchErr) || launchErr.Settlement.State != SettlementStopped {
		t.Fatalf("failed launch did not preserve settled result: %v", err)
	}
	if got := stdout.calls.Load(); got != 1 {
		t.Fatalf("settled failed-launch stdout close calls = %d, want 1", got)
	}
}

func TestPiWorkerFailedLaunchUnknownRetainsChannels(t *testing.T) {
	worker := newPiWorkerEventPumpFixture(t, 1)
	stdout := &retryCloseReader{reader: strings.NewReader("")}
	launchErr := stopFailedPiLaunch(&Process{stdout: stdout}, worker.Channels, errors.New("startup failed"))
	var typed *PiWorkerLaunchError
	if !errors.As(launchErr, &typed) || typed.Settlement.State != SettlementUnknown {
		t.Fatalf("failed launch did not retain UNKNOWN settlement: %v", launchErr)
	}
	if typed.processOwner == nil || stdout.calls.Load() != 0 {
		t.Fatal("UNKNOWN launch did not retain its stdout owner without closing it")
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

// RunGovernedChannels closes the provider channel when its loops end, and worker teardown closes it
// again through PiWorkerChannels.Close. The second close of a net.Conn is net.ErrClosed, which was
// recorded as a worker close error (close_error=true) and refused publication for a clean worker.
func TestProviderChannelClosedByTheGovernedLoopDoesNotFailWorkerClose(t *testing.T) {
	worker := newPiWorkerEventPumpFixture(t, 1)
	channels := worker.Channels
	if err := channels.ProviderChannel().Close(); err != nil {
		t.Fatalf("first close of the provider channel: %v", err)
	}
	if err := channels.ProviderChannel().Close(); err != nil {
		t.Fatalf("a repeated close of the provider channel returned %v", err)
	}
	if err := channels.Close(); err != nil {
		t.Fatalf("worker channel teardown after the governed loop closed the provider channel: %v", err)
	}
	waitWorkerEventPump(t, worker)
}
