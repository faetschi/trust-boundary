//go:build !windows

package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestLiveRunPairsOverTheRealListenerAndForwardsOverTheRealSocket starts run with
// --live, reads the pairing URL from stdout like a person at the terminal, pairs,
// and lists sessions through a fake product service. SIGINT then ends run.
func TestLiveRunPairsOverTheRealListenerAndForwardsOverTheRealSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "tbm")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "p.sock")
	service, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("unix sockets are unavailable here: %v", err)
	}
	defer service.Close()
	go func() {
		for {
			conn, err := service.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var prefix [4]byte
				if _, err := io.ReadFull(conn, prefix[:]); err != nil {
					return
				}
				request := make([]byte, binary.BigEndian.Uint32(prefix[:]))
				if _, err := io.ReadFull(conn, request); err != nil || !strings.Contains(string(request), `"op":"list_sessions"`) {
					return
				}
				body := `{"version":"tbound-product-protocol/v1","status":"ok","sessions":[{"id":"psess-live","project_id":"demo","provider_profile":"prof","publication_mode":"review","revision":1,"budget":{"host_calls":1,"tokens":1,"duration_seconds":1},"consumed":{"host_calls":0,"tokens":0,"duration_seconds":0},"reserved":{"host_calls":0,"tokens":0,"duration_seconds":0}}]}`
				binary.BigEndian.PutUint32(prefix[:], uint32(len(body)))
				_, _ = conn.Write(append(prefix[:], body...))
			}()
		}
	}()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- run([]string{"--live", "--socket", socket, "--listen", "127.0.0.1:0"}, writer, os.Stderr)
		writer.Close()
	}()

	pairURL := regexp.MustCompile(`http://127\.0\.0\.1:\d+/pair#\S+`)
	var found string
	lines := bufio.NewScanner(reader)
	for lines.Scan() {
		if match := pairURL.FindString(lines.Text()); match != "" {
			found = match
			break
		}
	}
	if found == "" {
		t.Fatalf("no pairing URL on stdout; run returned %v", <-done)
	}
	base, token, _ := strings.Cut(found, "/pair#")
	client := &http.Client{Timeout: 10 * time.Second}

	response, err := client.Post(base+"/api/v1/pair", "application/json", strings.NewReader(fmt.Sprintf(`{"token":%q}`, token)))
	if err != nil || response.StatusCode != http.StatusOK || len(response.Cookies()) == 0 {
		t.Fatalf("pairing: %v %v", response, err)
	}
	cookie := response.Cookies()[0]
	response.Body.Close()

	request, _ := http.NewRequest(http.MethodGet, base+"/api/v1/sessions", nil)
	request.AddCookie(cookie)
	response, err = client.Do(request)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("sessions: %v %v", response, err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if !strings.Contains(string(body), `"psess-live"`) {
		t.Errorf("the session list did not come from the service: %s", body)
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run after SIGINT: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not stop on SIGINT")
	}
}
