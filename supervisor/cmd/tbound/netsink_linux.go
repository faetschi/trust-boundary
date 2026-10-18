//go:build linux

package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"

	"tbound/supervisor/internal/netoracle"
	"tbound/supervisor/internal/runner"
)

// `tbound netsink --listen <addr> --journal <path> --secret-file <path>` is the registered
// synthetic unauthorized sink of EFF-NET-01, run ON THE GUEST by
// infra/guest/systemd/tbound-netsink.service as its own dedicated, unprivileged user.
//
//   - It listens on a guest-local loopback address (the unit uses runner.NetsinkAddr,
//     127.0.33.1:8080), in the guest ROOT network namespace. Cells run network-none, each in
//     a network namespace of its own with nothing in it; a connection to that address from
//     inside a cell ends in the cell's own loopback and never reaches this socket. A connection
//     that does reach it came from a process in the guest root network namespace, that is,
//     from a cell that left its namespace.
//   - The guest stays offline: the sink needs no route to the Windows host or anywhere else.
//   - The runner reads the journal through the read-only root evidence channel (`tbound
//     evidence net-journal`) and has the canaries sent through it (`net-canary`).
//   - It refuses to run as root, refuses a listen address that is not a loopback address with
//     an explicit port, and reads its canary secret from a root-owned file (never an
//     argument). The secret is never journaled (netoracle).
const netsinkUsage = `usage: tbound netsink --listen <loopback-ip:port> --journal <abs path> --secret-file <abs path>
`

const (
	netsinkMaxSecretBytes = 256
	// evidenceMaxNetJournalBytes bounds the journal net-journal reads (and so the document it
	// writes): a journal beyond it must be rotated by the operator (runbook), not served.
	evidenceMaxNetJournalBytes = 32 << 20
)

var evidenceTagRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// netsinkEnv is everything the subcommand touches outside its arguments; tests replace it.
type netsinkEnv struct {
	geteuid    func() int
	readSecret func(path string) (string, error)
	listen     func(addr string) (net.Listener, error)
	// served is called once with the running sink (a test seam).
	served func(*netoracle.Sink)
}

func productionNetsinkEnv() netsinkEnv {
	return netsinkEnv{
		geteuid:    os.Geteuid,
		readSecret: func(p string) (string, error) { return readNetsinkSecret(p, 0) },
		listen:     func(addr string) (net.Listener, error) { return net.Listen("tcp", addr) },
	}
}

// init dispatches `tbound netsink ...` before main runs (main.go's command table is not
// touched, as for `evidence`).
func init() {
	if len(os.Args) > 1 && os.Args[1] == "netsink" {
		os.Exit(netsinkMain(os.Args[2:], os.Stderr, productionNetsinkEnv()))
	}
}

func netsinkMain(args []string, stderr io.Writer, env netsinkEnv) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := runNetsink(ctx, args, stderr, env); err != nil {
		fmt.Fprintf(stderr, "tbound netsink: %v\n%s", err, netsinkUsage)
		return 2
	}
	return 0
}

// netsinkListenOK accepts a loopback IP literal with an explicit non-zero port.
func netsinkListenOK(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("--listen %q is not host:port", addr)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("--listen host %q is not a loopback IP address", host)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("--listen port %q is not 1..65535", port)
	}
	return nil
}

func runNetsink(ctx context.Context, args []string, stderr io.Writer, env netsinkEnv) error {
	fs := flag.NewFlagSet("netsink", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	listen := fs.String("listen", "", "loopback address, ip:port")
	journal := fs.String("journal", "", "audit journal file (absolute)")
	secretFile := fs.String("secret-file", "", "canary secret file (absolute)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch {
	case fs.NArg() != 0:
		return errors.New("unexpected positional arguments")
	case *listen == "" || *journal == "" || *secretFile == "":
		return errors.New("--listen, --journal and --secret-file are all required")
	case env.geteuid == nil || env.geteuid() == 0:
		return errors.New("the sink must not run as root (use its dedicated user)")
	case !filepath.IsAbs(*journal) || filepath.Clean(*journal) != *journal || !filepath.IsAbs(*secretFile) || filepath.Clean(*secretFile) != *secretFile:
		return errors.New("--journal and --secret-file must be clean absolute paths")
	}
	if err := netsinkListenOK(*listen); err != nil {
		return err
	}
	secret, err := env.readSecret(*secretFile)
	if err != nil {
		return fmt.Errorf("canary secret: %w", err)
	}
	ln, err := env.listen(*listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", *listen, err)
	}
	if tcp, ok := ln.Addr().(*net.TCPAddr); !ok || !tcp.IP.IsLoopback() {
		_ = ln.Close()
		return fmt.Errorf("the listener %s is not a loopback TCP address", ln.Addr())
	}
	sink, err := netoracle.NewSink(netoracle.SinkConfig{Listener: ln, JournalPath: *journal, Secret: secret})
	if err != nil {
		_ = ln.Close()
		return err
	}
	fmt.Fprintf(stderr, "tbound netsink: serving %s, journal %s\n", sink.Addr(), *journal)
	if env.served != nil {
		env.served(sink)
	}
	<-ctx.Done()
	return sink.Close()
}

// readNetsinkSecret reads the canary secret: a regular file (no symlink) owned by uid, readable
// at most by its owner and group (mode 0600/0640/0400/0440), never writable by the group, of
// at most netsinkMaxSecretBytes. The trailing newline is dropped; the value itself is
// validated by the sink or SendCanary.
func readNetsinkSecret(path string, uid uint32) (string, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return "", err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Uid != uid || st.Mode&0o7777&^0o640 != 0 {
		return "", fmt.Errorf("%s is not a regular file of uid %d with mode no wider than 0640", path, uid)
	}
	data, err := io.ReadAll(io.LimitReader(f, netsinkMaxSecretBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > netsinkMaxSecretBytes {
		return "", fmt.Errorf("%s is larger than %d bytes", path, netsinkMaxSecretBytes)
	}
	if n := len(data); n > 0 && data[n-1] == '\n' {
		data = data[:n-1]
	}
	return string(data), nil
}

// ---- evidence kinds ----

// sendNetsinkCanary is the production net-canary effect: one request to the fixed sink
// address with the secret from the root-owned secret file.
func sendNetsinkCanary(ctx context.Context, phase, tag string) (netoracle.CanaryAck, error) {
	secret, err := readNetsinkSecret(runner.NetsinkSecretPath, 0)
	if err != nil {
		return netoracle.CanaryAck{}, fmt.Errorf("canary secret: %w", err)
	}
	return netoracle.SendCanary(ctx, runner.NetsinkAddr, secret, phase, tag)
}

// evidenceNetCanary asks the sink to journal one canary. The address is the constant
// runner.NetsinkAddr and the secret comes from the sink's secret file: nothing the caller
// passes selects where the request goes. This is the only evidence kind with an effect, and
// the effect is one record in the sink's own journal.
func evidenceNetCanary(ctx context.Context, env evidenceEnv, f map[string]string) (runner.NetCanaryDoc, error) {
	phase, tag := f["--phase"], f["--interval"]
	if phase != netoracle.PhaseStart && phase != netoracle.PhaseEnd {
		return runner.NetCanaryDoc{}, fmt.Errorf("--phase must be %s or %s", netoracle.PhaseStart, netoracle.PhaseEnd)
	}
	if !evidenceTagRE.MatchString(tag) {
		return runner.NetCanaryDoc{}, errors.New("--interval must be a 32-digit lowercase hex interval tag")
	}
	if env.netCanary == nil {
		return runner.NetCanaryDoc{}, errors.New("no sink canary")
	}
	ack, err := env.netCanary(ctx, phase, tag)
	if err != nil {
		return runner.NetCanaryDoc{}, err
	}
	return runner.NetCanaryDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceNetCanary, Phase: phase, Interval: tag,
		Seq: ack.Seq, Hash: ack.Hash}, nil
}

// readNetsinkJournal reads the sink journal file read-only: no follow, regular file, at most
// evidenceMaxNetJournalBytes. It takes no lock; the sink appends whole lines, and a
// trailing partial line is dropped by netoracle.ExtractInterval.
func readNetsinkJournal() ([]byte, error) { return readNetsinkJournalFile(runner.NetsinkJournalPath) }

func readNetsinkJournalFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("the sink journal is not a regular file")
	}
	if info.Size() > evidenceMaxNetJournalBytes {
		return nil, fmt.Errorf("the sink journal is %d bytes, over the %d cap: rotate it", info.Size(), evidenceMaxNetJournalBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, evidenceMaxNetJournalBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > evidenceMaxNetJournalBytes {
		return nil, errors.New("the sink journal outgrew the cap while it was read")
	}
	return data, nil
}

// evidenceNetJournal returns the journal from its first record through the end canary of the
// interval, with the head, count and the canaries' positions, only if the chain verifies and
// the tag has a start canary followed by an end canary.
func evidenceNetJournal(env evidenceEnv, tag string) (runner.NetJournalDoc, error) {
	if !evidenceTagRE.MatchString(tag) {
		return runner.NetJournalDoc{}, errors.New("--interval must be a 32-digit lowercase hex interval tag")
	}
	if env.netJournal == nil {
		return runner.NetJournalDoc{}, errors.New("no sink journal")
	}
	raw, err := env.netJournal()
	if err != nil {
		return runner.NetJournalDoc{}, fmt.Errorf("the sink journal cannot be read: %w", err)
	}
	iv, err := netoracle.ExtractInterval(raw, tag)
	if err != nil {
		return runner.NetJournalDoc{}, err
	}
	return runner.NetJournalDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceNetJournal, Interval: tag,
		JournalB64: base64.StdEncoding.EncodeToString(iv.Journal), Head: iv.Head, Count: iv.Count,
		StartSeq: iv.StartSeq, EndSeq: iv.EndSeq}, nil
}
