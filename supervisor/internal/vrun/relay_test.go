package vrun

import (
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRelayCarriesBytesBothWaysAndDiesWithItsProcess(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	socket := filepath.Join(t.TempDir(), SocketName)
	if runtime.GOOS == "windows" {
		socket = `\\.\pipe\tbound-relay-test-` + strconv.Itoa(os.Getpid())
	}
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(probe.Addr().(*net.TCPAddr).Port)
	_ = probe.Close()
	script := strings.NewReplacer(ForwarderDir+"/"+SocketName, strings.ReplaceAll(socket, `\`, `\\`), `listen(`+RelayPort, `listen(`+port).Replace(relayScript)
	upstream := exec.Command(node, "-e", `require("net").createServer(c=>c.pipe(c)).listen(process.argv[1])`, socket)
	relay := exec.Command(node, "-e", script)
	for _, cmd := range []*exec.Cmd{upstream, relay} {
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	}
	var conn net.Conn
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if conn, err = net.Dial("tcp", "127.0.0.1:"+port); err != nil {
			continue
		}
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err = conn.Write([]byte("ping")); err == nil {
			buf := make([]byte, 4)
			if _, err = io.ReadFull(conn, buf); err == nil && string(buf) == "ping" {
				break
			}
		}
		_ = conn.Close()
		conn = nil
	}
	if conn == nil {
		t.Fatalf("the relay did not carry bytes to the socket and back: %v", err)
	}
	_ = conn.Close()
	_ = relay.Process.Kill()
	_ = relay.Wait()
	if c, err := net.DialTimeout("tcp", "127.0.0.1:"+port, time.Second); err == nil {
		_ = c.Close()
		t.Fatal("the relay port still listens after its process ended")
	}
}
