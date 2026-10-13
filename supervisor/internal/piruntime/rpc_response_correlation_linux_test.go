//go:build linux

package piruntime

import (
	"io"
	"testing"
)

func TestPiWorkerRPCFailuresRequireMatchingPromptResponse(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		wantEvent      bool
	}{
		{"late abort", `{"id":"request-old","type":"response","command":"abort","success":false,"error":"failed"}`, false},
		{"abort with active ID", `{"id":"request-current","type":"response","command":"abort","success":false,"error":"failed"}`, false},
		{"unmatched prompt", `{"id":"request-old","type":"response","command":"prompt","success":false,"error":"failed"}`, false},
		{"matching prompt", `{"id":"request-current","type":"response","command":"prompt","success":false,"error":"failed"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader, writer := io.Pipe()
			defer writer.Close()
			rpc, err := NewPiRPCClient(reader, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			worker := newPiWorkerEventPumpFixture(t, 1, rpc)
			released := make(chan struct{})
			worker.mu.Lock()
			worker.leases = map[string]*workerPromptLease{"request-current": {lease: &Lease{release: func() { close(released) }}}}
			worker.mu.Unlock()
			defer worker.releasePromptLease("request-current")
			if _, err := io.WriteString(writer, tc.response+"\n"); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := worker.Channels.childEvents.Close(); err != nil {
				t.Fatal(err)
			}
			waitWorkerEventPump(t, worker)
			if tc.wantEvent {
				event, ok := <-worker.events
				if !ok || event.Type != "worker_error" || event.RequestID != "request-current" || event.Code != "rpc_command_failed" {
					t.Fatalf("event=%+v open=%v", event, ok)
				}
				select {
				case <-released:
				default:
					t.Fatal("matching prompt lease not released")
				}
				return
			}
			if got := len(worker.events); got != 0 {
				t.Fatalf("unmatched response emitted %d events", got)
			}
			select {
			case <-released:
				t.Fatal("unmatched response released prompt")
			default:
			}
			worker.mu.Lock()
			active := worker.leases["request-current"] != nil
			worker.mu.Unlock()
			if !active {
				t.Fatal("unmatched response removed prompt lease")
			}
		})
	}
}
