package webchat

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// rawService answers each connection with whatever serve writes, after it has read
// the one request frame.
func rawService(t *testing.T, serve func(conn net.Conn, request ProductRequest)) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "tbr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "r.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("unix sockets are unavailable here: %v", err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
				var request ProductRequest
				if readProductFrame(conn, MaxProductRequestBytes, &request) == nil {
					serve(conn, request)
				}
			}()
		}
	}()
	return path
}

func writeRaw(conn net.Conn, body string) {
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(body)))
	_, _ = conn.Write(append(prefix[:], body...))
}

func TestSocketClientSendsOneStrictFrameAndSetsTheVersion(t *testing.T) {
	received := make(chan ProductRequest, 1)
	path := rawService(t, func(conn net.Conn, request ProductRequest) {
		received <- request
		writeRaw(conn, `{"version":"`+ProductVersion+`","status":"ok"}`)
	})
	response, err := socketClient{path: path}.Call(context.Background(), ProductRequest{Op: OpListSessions})
	if err != nil || response.Status != StatusOK {
		t.Fatalf("%v %+v", err, response)
	}
	got := <-received
	if got.Version != ProductVersion || got.Op != OpListSessions {
		t.Errorf("the service received %+v", got)
	}
}

func TestSocketClientRefusesMalformedResponses(t *testing.T) {
	bad := map[string]string{
		"wrong version":  `{"version":"tbound-product-protocol/v0","status":"ok"}`,
		"odd status":     `{"version":"` + ProductVersion + `","status":"maybe"}`,
		"error no body":  `{"version":"` + ProductVersion + `","status":"error"}`,
		"duplicate key":  `{"version":"` + ProductVersion + `","status":"ok","status":"error"}`,
		"unknown field":  `{"version":"` + ProductVersion + `","status":"ok","principal":"root"}`,
		"trailing value": `{"version":"` + ProductVersion + `","status":"ok"}{}`,
		"not json":       `ok`,
	}
	for name, body := range bad {
		body := body
		path := rawService(t, func(conn net.Conn, _ ProductRequest) { writeRaw(conn, body) })
		_, err := socketClient{path: path}.Call(context.Background(), ProductRequest{Op: OpListSessions})
		var service *ServiceError
		if !errors.As(err, &service) || service.Code != CodeBadServiceResponse {
			t.Errorf("%s: err=%v, want BAD_SERVICE_RESPONSE", name, err)
		}
	}
	// A frame longer than the 1 MiB response limit is refused before it is read.
	path := rawService(t, func(conn net.Conn, _ ProductRequest) {
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], MaxProductResponseBytes+1)
		_, _ = conn.Write(prefix[:])
	})
	_, err := socketClient{path: path}.Call(context.Background(), ProductRequest{Op: OpListSessions})
	var service *ServiceError
	if !errors.As(err, &service) || service.Code != CodeBadServiceResponse {
		t.Errorf("oversized frame: %v", err)
	}
}

func TestSocketClientKeepsServiceErrorCodesAndMarksSentTransportFailures(t *testing.T) {
	path := rawService(t, func(conn net.Conn, _ ProductRequest) {
		writeRaw(conn, `{"version":"`+ProductVersion+`","status":"error","error":{"code":"STALE_REVISION","detail":"d"}}`)
	})
	_, err := socketClient{path: path}.Call(context.Background(), ProductRequest{Op: OpListSessions})
	var service *ServiceError
	if !errors.As(err, &service) || service.Code != "STALE_REVISION" || service.Detail != "d" {
		t.Errorf("service error: %v", err)
	}
	cut := rawService(t, func(conn net.Conn, _ ProductRequest) { _, _ = conn.Write([]byte{0, 0, 0, 40, '{'}) })
	_, err = socketClient{path: cut}.Call(context.Background(), ProductRequest{Op: OpListSessions})
	var transport *TransportError
	if !errors.As(err, &transport) || !transport.Sent {
		t.Errorf("a frame cut short: %v", err)
	}
	_, err = socketClient{path: filepath.Join(t.TempDir(), "none.sock")}.Call(context.Background(), ProductRequest{Op: OpListSessions})
	if !errors.As(err, &transport) || transport.Sent {
		t.Errorf("no service: %v", err)
	}
}

func TestSocketClientStopsWhenTheCallerGoesAway(t *testing.T) {
	path := rawService(t, func(conn net.Conn, _ ProductRequest) { time.Sleep(15 * time.Second) })
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	_, err := socketClient{path: path}.Call(ctx, ProductRequest{Op: OpObserve, Observe: &ProductObserve{SessionID: "psess-x", WaitMillis: 25000}})
	if !errors.Is(err, context.Canceled) || time.Since(start) > 5*time.Second {
		t.Errorf("err=%v after %v", err, time.Since(start))
	}
}
