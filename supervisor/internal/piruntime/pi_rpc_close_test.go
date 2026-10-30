package piruntime

import (
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type rpcInputCloseWriter struct {
	calls atomic.Int32
	err   error
}

func (w *rpcInputCloseWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *rpcInputCloseWriter) Close() error                { w.calls.Add(1); return w.err }

func TestPiRPCInputCloseIsConcurrentAndSticky(t *testing.T) {
	for _, failure := range []bool{false, true} {
		writer := &rpcInputCloseWriter{}
		if failure {
			writer.err = errors.New("input close failed")
		}
		client, err := NewPiRPCClient(strings.NewReader(""), writer)
		if err != nil {
			t.Fatal(err)
		}
		results := make(chan error, 16)
		var callers sync.WaitGroup
		for i := 0; i < cap(results); i++ {
			callers.Add(1)
			go func() { defer callers.Done(); results <- client.CloseInput() }()
		}
		callers.Wait()
		close(results)
		for got := range results {
			if !errors.Is(got, writer.err) {
				t.Fatalf("close error=%v want=%v", got, writer.err)
			}
		}
		if got := client.CloseInput(); !errors.Is(got, writer.err) {
			t.Fatalf("retry close error=%v want=%v", got, writer.err)
		}
		if got := writer.calls.Load(); got != 1 {
			t.Fatalf("underlying close calls=%d want=1", got)
		}
		if err := client.SendPrompt("after-close", "hello"); err == nil {
			t.Fatal("accepted prompt after input close")
		}
	}
}

type rpcInputBlockingWriter struct {
	started     chan struct{}
	released    chan struct{}
	startOnce   sync.Once
	releaseOnce sync.Once
}

func (w *rpcInputBlockingWriter) Write([]byte) (int, error) {
	w.startOnce.Do(func() { close(w.started) })
	<-w.released
	return 0, os.ErrClosed
}

func (w *rpcInputBlockingWriter) Close() error {
	w.releaseOnce.Do(func() { close(w.released) })
	return nil
}

func TestPiRPCInputCloseUnblocksWrite(t *testing.T) {
	writer := &rpcInputBlockingWriter{started: make(chan struct{}), released: make(chan struct{})}
	client, err := NewPiRPCClient(strings.NewReader(""), writer)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	written := make(chan error, 1)
	go func() { written <- client.SendPrompt("request-1", "hello") }()
	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("write did not start")
	}
	closed := make(chan error, 1)
	go func() { closed <- client.CloseInput() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close=%v", err)
		}
	case <-time.After(time.Second):
		writer.Close()
		select {
		case <-written:
		case <-time.After(time.Second):
			t.Fatal("write did not exit after forced release")
		}
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Fatal("close did not exit after forced release")
		}
		t.Fatal("close blocked behind write")
	}
	select {
	case err := <-written:
		if !errors.Is(err, os.ErrClosed) {
			t.Fatalf("write=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("write stayed blocked after close")
	}
}
