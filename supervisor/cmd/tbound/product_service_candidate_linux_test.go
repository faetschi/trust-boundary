//go:build linux

package main

import (
	"encoding/json"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tbound/supervisor/internal/productprotocol"
)

func TestProductServiceCandidateFrameBoundary(t *testing.T) {
	data, err := os.ReadFile("../../internal/productprotocol/testdata/response-candidate-at-frame-limit.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, extra := range []int{0, 1} {
		var response productprotocol.Response
		if err := json.Unmarshal(data, &response); err != nil {
			t.Fatal(err)
		}
		if extra == 1 {
			response.Candidate.Changes[0].Diff += "x"
		}
		if size, err := jsonSize(response); err != nil || size != productprotocol.MaxResponseBytes+extra {
			t.Fatalf("boundary fixture size %d, extra %d: %v", size, extra, err)
		}
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(t.TempDir(), "reply.sock"), Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
		done := make(chan error, 1)
		go func() {
			conn, err := listener.AcceptUnix()
			if err == nil {
				svc := &productService{log: log.New(io.Discard, "", 0)}
				svc.reply(conn, &response)
				_ = conn.Close()
			}
			done <- err
		}()
		conn, err := net.Dial("unix", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		var got productprotocol.Response
		err = productprotocol.ReadFrame(conn, productprotocol.MaxResponseBytes, &got)
		_ = conn.Close()
		if acceptErr := <-done; acceptErr != nil || err != nil {
			t.Fatalf("candidate reply: %v, accept: %v", err, acceptErr)
		}
		if extra == 0 {
			if got.Candidate == nil || len(got.Candidate.Changes) != len(response.Candidate.Changes) {
				t.Fatal("a candidate at the frame bound lost paths")
			}
		} else if got.Candidate != nil || got.Error == nil || got.Error.Code != "CANDIDATE_TOO_LARGE" {
			t.Fatalf("oversized candidate must be refused explicitly: %+v", got.Error)
		}
	}
}
