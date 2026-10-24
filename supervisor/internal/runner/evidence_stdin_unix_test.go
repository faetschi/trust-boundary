//go:build !windows

package runner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSSH writes a stand-in for the ssh binary: it acknowledges, then echoes what its stdin holds up to the end of the
// file, the way a remote command sees the session's stdin.
func fakeSSH(t *testing.T) SSHTransport {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ssh")
	script := "#!/bin/sh\necho ready\nwhile IFS= read -r line; do echo \"got:$line\"; done\necho eof\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return SSHTransport{SSHPath: path, IdentityFile: "k", User: "root", Host: "10.0.0.1", HostKeyAlias: "guest", KnownHostsFile: "/kh",
		ClientCommand: DefaultEvidenceCommand()}
}

// RunReadyStdin hands the command the caller's stdin: what is written reaches it and closing the writer is its end of
// file, which is how the host ends the guest transient-watch.
func TestSSHTransportRunReadyStdinReachesTheRemoteCommand(t *testing.T) {
	tr := fakeSSH(t)
	if !(EvidenceClient{Transport: tr}).CanStop() {
		t.Fatal("SSHTransport is not a StdinTransport")
	}
	pr, pw := io.Pipe()
	ready := make(chan string, 1)
	done := make(chan struct{})
	var stdout []byte
	var exit int
	var err error
	go func() {
		defer close(done)
		stdout, _, exit, err = tr.RunReadyStdin(context.Background(), []string{"transient-watch"}, pr, func(l []byte) { ready <- string(l) })
	}()
	select {
	case l := <-ready:
		if l != "ready\n" {
			t.Fatalf("first line = %q", l)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no first line")
	}
	select {
	case <-done:
		t.Fatal("the command ended before its stdin did")
	case <-time.After(100 * time.Millisecond):
	}
	_, _ = io.WriteString(pw, TransientStopLine)
	_ = pw.Close()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the command did not end on the end of its stdin")
	}
	if err != nil || exit != 0 || string(stdout) != "ready\ngot:stop\neof\n" {
		t.Fatalf("stdout %q exit %d err %v", stdout, exit, err)
	}
	// Without a stdin the remote command sees the end of file at once (RunReady).
	out, _, _, err := tr.RunReady(context.Background(), []string{"x"}, nil)
	if err != nil || !strings.HasSuffix(string(out), "eof\n") || strings.Contains(string(out), "got:") {
		t.Fatalf("RunReady without stdin: %q, %v", out, err)
	}
}

// A remote command that ends by itself (a refused watch) does not keep the call waiting for a stdin the caller still
// holds open.
func TestSSHTransportRunReadyStdinReturnsWhenTheCommandEndsWhateverStdinDoes(t *testing.T) {
	tr := fakeSSH(t)
	if err := os.WriteFile(tr.SSHPath, []byte("#!/bin/sh\necho refused >&2\nexit 3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	pr, pw := io.Pipe()
	defer pw.Close()
	defer pr.Close()
	done := make(chan int, 1)
	go func() {
		_, stderr, exit, err := tr.RunReadyStdin(context.Background(), []string{"transient-watch"}, pr, nil)
		if err != nil || !strings.Contains(string(stderr), "refused") {
			exit = -1
		}
		done <- exit
	}()
	select {
	case exit := <-done:
		if exit != 3 {
			t.Fatalf("exit = %d, want 3", exit)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the call waited for a stdin that is still open")
	}
}
