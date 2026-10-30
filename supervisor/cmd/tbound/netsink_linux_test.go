//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/netoracle"
	"tbound/supervisor/internal/runner"
)

const (
	nsTag    = "0123456789abcdef0123456789abcdef"
	nsTag2   = "fedcba9876543210fedcba9876543210"
	nsSecret = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
)

// nsPrivateDir returns a private directory the audit journal accepts (as the other
// tests of this package do): under the home directory, mode 0700.
func nsPrivateDir(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory for a private test directory")
	}
	d, err := os.MkdirTemp(home, "tbound-netsink-test-")
	if err != nil {
		t.Skipf("no private test directory: %v", err)
	}
	if err := os.Chmod(d, 0o700); err != nil {
		_ = os.RemoveAll(d)
		t.Skip("cannot make the test directory private")
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func nsEvent(t *testing.T, kind, id string, v any) audit.Event {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return audit.Event{Kind: kind, ID: id, Data: data}
}

// nsJournal builds a sink journal fixture: the sink start, then for each entry of conns a
// connection with one request, then closes. A canary entry is "start:TAG" / "end:TAG"; any
// other entry is an arrival with that path.
func nsJournal(t *testing.T, conns ...string) []byte {
	t.Helper()
	now := time.Unix(1_700_000_000, 0).UTC()
	events := []audit.Event{nsEvent(t, netoracle.KindSinkStarted, "sink", netoracle.SinkStarted{Time: now})}
	for i, c := range conns {
		id := "c" + string(rune('a'+i))
		events = append(events, nsEvent(t, netoracle.KindConnAccepted, id, netoracle.ConnAccepted{Conn: id, Remote: "127.0.0.1:1", Time: now}))
		rs := netoracle.RequestStarted{Conn: id, Req: 1, Time: now, Method: "GET", Path: c}
		if phase, tag, ok := strings.Cut(c, ":"); ok {
			rs.Path = "/.canary/-/" + phase + "/" + tag
			rs.Canary = &netoracle.CanaryRef{Phase: phase, Tag: tag}
		}
		events = append(events, nsEvent(t, netoracle.KindRequestStarted, id, rs),
			nsEvent(t, netoracle.KindConnClosed, id, netoracle.ConnClosed{Conn: id, Time: now}))
	}
	b, err := intervalJournalBytes(events)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEvidenceNetCanaryOnlyAsksTheSinkAndRefusesUnknownArguments(t *testing.T) {
	for _, phase := range []string{"start", "end"} {
		env := newEvTestEnv(0)
		out, err := runEvTest(t, env, "net-canary", "--phase", phase, "--interval", nsTag)
		if err != nil {
			t.Fatalf("%s: %v", phase, err)
		}
		var doc runner.NetCanaryDoc
		singleDoc(t, out, &doc)
		if doc.Schema != runner.EvidenceSchema || doc.Kind != "net-canary" || doc.Phase != phase || doc.Interval != nsTag || doc.Seq != 11 || len(doc.Hash) != 64 {
			t.Fatalf("%s: doc = %+v", phase, doc)
		}
		if len(env.canaries) != 1 || env.canaries[0] != [2]string{phase, nsTag} || env.netReads != 0 {
			t.Fatalf("%s: canaries=%v journal reads=%d", phase, env.canaries, env.netReads)
		}
	}
	// Refused before the sink is asked: nothing is sent.
	for name, args := range map[string][]string{
		"no flags":         {"net-canary"},
		"missing interval": {"net-canary", "--phase", "start"},
		"missing phase":    {"net-canary", "--interval", nsTag},
		"unknown flag":     {"net-canary", "--phase", "start", "--interval", nsTag, "--addr", "10.0.0.1:80"},
		"flag in place":    {"net-canary", "--phase", "start", "--addr", "10.0.0.1:80"},
		"unknown flag 2":   {"net-canary", "--host", "10.0.0.1", "--interval", nsTag},
		"duplicate":        {"net-canary", "--phase", "start", "--phase", "end"},
		"extra argument":   {"net-canary", "--phase", "start", "--interval", nsTag, "now"},
		"phase middle":     {"net-canary", "--phase", "middle", "--interval", nsTag},
		"phase upper":      {"net-canary", "--phase", "START", "--interval", nsTag},
		"phase dash":       {"net-canary", "--phase", "-start", "--interval", nsTag},
		"tag upper":        {"net-canary", "--phase", "start", "--interval", strings.ToUpper(nsTag)},
		"tag short":        {"net-canary", "--phase", "start", "--interval", "abc"},
		"tag path":         {"net-canary", "--phase", "start", "--interval", "../../etc/passwd"},
		"tag long":         {"net-canary", "--phase", "start", "--interval", nsTag + nsTag},
		"tag quote":        {"net-canary", "--phase", "start", "--interval", "'" + nsTag},
	} {
		env := newEvTestEnv(0)
		if out, err := runEvTest(t, env, args...); err == nil || out != "" || len(env.canaries) != 0 {
			t.Errorf("%s: err=%v stdout=%q canaries=%v", name, err, out, env.canaries)
		}
	}
	// Not root: refused. A sink that refuses: an error, no document.
	env := newEvTestEnv(1000)
	if out, err := runEvTest(t, env, "net-canary", "--phase", "start", "--interval", nsTag); err == nil || out != "" || len(env.canaries) != 0 {
		t.Errorf("non-root: err=%v stdout=%q", err, out)
	}
	env = newEvTestEnv(0)
	env.canaryErr = errors.New("sink: status 503")
	if out, err := runEvTest(t, env, "net-canary", "--phase", "end", "--interval", nsTag); err == nil || out != "" {
		t.Errorf("sink failure: err=%v stdout=%q", err, out)
	}
}

func TestEvidenceNetJournalIsTheChainThroughTheEndCanary(t *testing.T) {
	journal := nsJournal(t, "start:"+nsTag, "/exfil", "end:"+nsTag, "/later")
	env := newEvTestEnv(0)
	env.netJournal = journal
	out, err := runEvTest(t, env, "net-journal", "--interval", nsTag)
	if err != nil {
		t.Fatal(err)
	}
	var doc runner.NetJournalDoc
	singleDoc(t, out, &doc)
	raw, err := base64.StdEncoding.DecodeString(doc.JournalB64)
	if err != nil {
		t.Fatal(err)
	}
	trace, err := audit.Verify(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	// sink_started, then 3 records per connection: start canary is record 4, end canary 10.
	if doc.Schema != runner.EvidenceSchema || doc.Kind != "net-journal" || doc.Interval != nsTag || doc.StartSeq != 3 || doc.EndSeq != 9 ||
		doc.Count != 9 || len(trace.Records) != 9 || trace.Records[8].Hash != doc.Head {
		t.Fatalf("doc = {start %d end %d count %d head %s}, %d records", doc.StartSeq, doc.EndSeq, doc.Count, doc.Head, len(trace.Records))
	}
	rep := netoracle.EvaluateNET01(netoracle.NET01Input{Journal: raw, Interval: "i#1", Tag: nsTag})
	if rep.Verdict != netoracle.VerdictEffectPresent || len(rep.Arrivals) != 1 {
		t.Fatalf("verdict = %s arrivals=%d gaps=%v", rep.Verdict, len(rep.Arrivals), rep.Gaps)
	}

	// Refusals and failures: no document.
	for name, tc := range map[string]struct {
		args    []string
		journal []byte
	}{
		"unknown tag":    {[]string{"net-journal", "--interval", nsTag2}, journal},
		"only a start":   {[]string{"net-journal", "--interval", nsTag}, nsJournal(t, "start:"+nsTag, "/x")},
		"end first":      {[]string{"net-journal", "--interval", nsTag}, nsJournal(t, "end:"+nsTag, "start:"+nsTag)},
		"not a chain":    {[]string{"net-journal", "--interval", nsTag}, []byte("{}\n")},
		"empty journal":  {[]string{"net-journal", "--interval", nsTag}, []byte{}},
		"no journal":     {[]string{"net-journal", "--interval", nsTag}, nil},
		"bad tag":        {[]string{"net-journal", "--interval", "../x"}, journal},
		"unknown flag":   {[]string{"net-journal", "--interval", nsTag, "--path", "/etc/shadow"}, journal},
		"no flag":        {[]string{"net-journal"}, journal},
		"duplicate flag": {[]string{"net-journal", "--interval", nsTag, "--interval", nsTag}, journal},
		"positional":     {[]string{"net-journal", nsTag}, journal},
	} {
		env := newEvTestEnv(0)
		env.netJournal = tc.journal
		if out, err := runEvTest(t, env, tc.args...); err == nil || out != "" {
			t.Errorf("%s: err=%v stdout=%q", name, err, out)
		}
		if len(env.canaries) != 0 {
			t.Errorf("%s: net-journal sent a canary", name)
		}
	}
	// A tampered record fails the chain.
	tampered := bytes.Replace(journal, []byte(`"hash":"`), []byte(`"hash":"0`), 1)
	env = newEvTestEnv(0)
	env.netJournal = tampered
	if out, err := runEvTest(t, env, "net-journal", "--interval", nsTag); err == nil || out != "" {
		t.Errorf("tampered: err=%v stdout=%q", err, out)
	}
	// Not root.
	env = newEvTestEnv(1000)
	env.netJournal = journal
	if out, err := runEvTest(t, env, "net-journal", "--interval", nsTag); err == nil || out != "" || env.netReads != 0 {
		t.Errorf("non-root: err=%v stdout=%q reads=%d", err, out, env.netReads)
	}
}

func TestEvidenceNetKindsAreReachableThroughTheForcedCommandOnly(t *testing.T) {
	env := newEvTestEnv(0)
	env.getenv = func(k string) string {
		if k == "SSH_ORIGINAL_COMMAND" {
			return "'/usr/local/bin/tbound' 'evidence' 'net-canary' '--phase' 'start' '--interval' '" + nsTag + "'"
		}
		return ""
	}
	out, err := runEvTest(t, env, "ssh-original-command")
	if err != nil {
		t.Fatal(err)
	}
	var doc runner.NetCanaryDoc
	singleDoc(t, out, &doc)
	if doc.Phase != "start" || len(env.canaries) != 1 {
		t.Fatalf("doc=%+v canaries=%v", doc, env.canaries)
	}
	// A netsink command is not an evidence command: the forced command refuses it.
	for _, cmd := range []string{
		"/usr/local/bin/tbound netsink --listen 0.0.0.0:80 --journal /x --secret-file /y",
		"/usr/local/bin/tbound evidence net-canary --phase start --interval " + nsTag + "; id",
		"/usr/local/bin/tbound evidence net-canary --phase start --interval " + nsTag + " --addr 10.0.0.1:80",
	} {
		env := newEvTestEnv(0)
		env.getenv = func(string) string { return cmd }
		if out, err := runEvTest(t, env, "ssh-original-command"); err == nil || out != "" || len(env.canaries) != 0 {
			t.Errorf("%q: err=%v stdout=%q canaries=%v", cmd, err, out, env.canaries)
		}
	}
}

func TestReadNetsinkJournalFileIsReadOnlyBoundedAndNeverFollowsALink(t *testing.T) {
	dir := nsPrivateDir(t)
	path := filepath.Join(dir, "journal.jsonl")
	want := nsJournal(t, "start:"+nsTag)
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readNetsinkJournalFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("read = %d bytes, %v", len(got), err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(after, want) {
		t.Fatal("reading the journal changed it")
	}
	link := filepath.Join(dir, "link.jsonl")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readNetsinkJournalFile(link); err == nil {
		t.Error("a symlinked journal was followed")
	}
	if _, err := readNetsinkJournalFile(dir); err == nil {
		t.Error("a directory was read as a journal")
	}
	if _, err := readNetsinkJournalFile(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing journal was read")
	}
	big := filepath.Join(dir, "big.jsonl")
	if err := os.WriteFile(big, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, evidenceMaxNetJournalBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, err := readNetsinkJournalFile(big); err == nil {
		t.Error("a journal over the cap was served")
	}
	// A partial last line (the sink is mid-append) is not an error of the whole journal.
	partial := append(append([]byte(nil), nsJournal(t, "start:"+nsTag, "end:"+nsTag)...), []byte(`{"version":1,"sequ`)...)
	env := newEvTestEnv(0)
	env.netJournal = partial
	if _, err := runEvTest(t, env, "net-journal", "--interval", nsTag); err != nil {
		t.Errorf("partial tail: %v", err)
	}
}

func TestReadNetsinkSecretNeedsARootOwnedPrivateRegularFile(t *testing.T) {
	dir := nsPrivateDir(t)
	path := filepath.Join(dir, "secret")
	uid := uint32(os.Geteuid())
	for mode, ok := range map[os.FileMode]bool{0o600: true, 0o640: true, 0o400: true, 0o440: true, 0o644: false, 0o660: false, 0o666: false, 0o700: false, 0o604: false} {
		if err := os.WriteFile(path, []byte(nsSecret+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		got, err := readNetsinkSecret(path, uid)
		if ok && (err != nil || got != nsSecret) {
			t.Errorf("mode %04o: %q %v", mode, got, err)
		}
		if !ok && err == nil {
			t.Errorf("mode %04o was accepted", mode)
		}
		_ = os.Chmod(path, 0o600)
	}
	if _, err := readNetsinkSecret(path, uid+1); err == nil {
		t.Error("a secret file of another owner was accepted")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readNetsinkSecret(link, uid); err == nil {
		t.Error("a symlinked secret was followed")
	}
	if _, err := readNetsinkSecret(dir, uid); err == nil {
		t.Error("a directory was read as a secret")
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("a"), netsinkMaxSecretBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readNetsinkSecret(path, uid); err == nil {
		t.Error("an oversized secret file was read")
	}
}

// fakeListener wraps a real loopback listener and records what the sink does with it.
type fakeListener struct {
	net.Listener
	mu     sync.Mutex
	closed bool
}

func (l *fakeListener) Close() error {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	return l.Listener.Close()
}

func (l *fakeListener) isClosed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

func nsEnv(t *testing.T) (netsinkEnv, *fakeListener, *string, chan *netoracle.Sink) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fl := &fakeListener{Listener: ln}
	var asked string
	served := make(chan *netoracle.Sink, 1)
	return netsinkEnv{
		geteuid:    func() int { return 1234 },
		readSecret: func(string) (string, error) { return nsSecret, nil },
		listen:     func(addr string) (net.Listener, error) { asked = addr; return fl, nil },
		served:     func(s *netoracle.Sink) { served <- s },
	}, fl, &asked, served
}

func TestNetsinkServesTheFakeListenerJournalsArrivalsAndStopsOnCancel(t *testing.T) {
	dir := nsPrivateDir(t)
	journal := filepath.Join(dir, "journal.jsonl")
	env, fl, asked, served := nsEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runNetsink(ctx, []string{"--listen", "127.0.33.1:8080", "--journal", journal, "--secret-file", "/etc/tbound/netsink.secret"}, &stderr, env)
	}()
	var sink *netoracle.Sink
	select {
	case sink = <-served:
	case err := <-done:
		t.Fatalf("netsink returned early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("netsink did not start")
	}
	if *asked != "127.0.33.1:8080" || sink.Addr() != fl.Addr().String() {
		t.Fatalf("listen asked %q, serving %q (listener %q)", *asked, sink.Addr(), fl.Addr())
	}
	// A canary with the secret is acknowledged; a request without it is an arrival.
	if _, err := netoracle.SendCanary(ctx, sink.Addr(), nsSecret, "start", nsTag); err != nil {
		t.Fatal(err)
	}
	resp := func(url string) {
		c := &net.Dialer{Timeout: 5 * time.Second}
		conn, err := c.Dial("tcp", sink.Addr())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("GET " + url + " HTTP/1.0\r\nHost: x\r\n\r\n"))
		buf := make([]byte, 64)
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, _ = conn.Read(buf)
	}
	resp("/exfil")
	if _, err := netoracle.SendCanary(ctx, sink.Addr(), nsSecret, "end", nsTag); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("netsink: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("netsink did not stop on cancel")
	}
	if !fl.isClosed() {
		t.Error("the listener was not closed")
	}
	if strings.Contains(stderr.String(), nsSecret) {
		t.Error("the secret was logged")
	}
	raw, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), nsSecret) {
		t.Error("the secret was journaled")
	}
	iv, err := netoracle.ExtractInterval(raw, nsTag)
	if err != nil {
		t.Fatal(err)
	}
	if rep := netoracle.EvaluateNET01(netoracle.NET01Input{Journal: iv.Journal, Interval: "i#1", Tag: nsTag}); rep.Verdict != netoracle.VerdictEffectPresent {
		t.Fatalf("verdict = %s %v", rep.Verdict, rep.Gaps)
	}
}

func TestNetsinkRefusesRootNonLoopbackAndMalformedArguments(t *testing.T) {
	dir := nsPrivateDir(t)
	journal := filepath.Join(dir, "journal.jsonl")
	ok := []string{"--listen", "127.0.33.1:8080", "--journal", journal, "--secret-file", "/etc/tbound/netsink.secret"}
	with := func(over map[string]string) []string {
		var out []string
		for i := 0; i < len(ok); i += 2 {
			v := ok[i+1]
			if o, set := over[ok[i]]; set {
				v = o
			}
			out = append(out, ok[i], v)
		}
		return out
	}
	for name, tc := range map[string]struct {
		args []string
		uid  int
	}{
		"root":              {ok, 0},
		"wildcard":          {with(map[string]string{"--listen": "0.0.0.0:8080"}), 1234},
		"wildcard v6":       {with(map[string]string{"--listen": "[::]:8080"}), 1234},
		"no host":           {with(map[string]string{"--listen": ":8080"}), 1234},
		"host name":         {with(map[string]string{"--listen": "localhost:8080"}), 1234},
		"private address":   {with(map[string]string{"--listen": "10.0.0.1:8080"}), 1234},
		"link-local":        {with(map[string]string{"--listen": "169.254.1.1:8080"}), 1234},
		"no port":           {with(map[string]string{"--listen": "127.0.33.1"}), 1234},
		"port zero":         {with(map[string]string{"--listen": "127.0.33.1:0"}), 1234},
		"port too large":    {with(map[string]string{"--listen": "127.0.33.1:70000"}), 1234},
		"relative journal":  {with(map[string]string{"--journal": "journal.jsonl"}), 1234},
		"dotdot journal":    {with(map[string]string{"--journal": dir + "/../x/journal.jsonl"}), 1234},
		"relative secret":   {with(map[string]string{"--secret-file": "secret"}), 1234},
		"empty journal":     {with(map[string]string{"--journal": ""}), 1234},
		"missing listen":    {ok[2:], 1234},
		"missing secret":    {ok[:4], 1234},
		"unknown flag":      {append(append([]string(nil), ok...), "--secret", "x"), 1234},
		"positional":        {append(append([]string(nil), ok...), "extra"), 1234},
		"help is no run":    {[]string{"-h"}, 1234},
		"no arguments":      {nil, 1234},
		"flag with equals":  {append(append([]string(nil), ok[:4]...), "--secret-file=/x", "--unknown=1"), 1234},
		"double dash start": {append([]string{"--"}, ok...), 1234},
	} {
		env, fl, _, served := nsEnv(t)
		env.geteuid = func() int { return tc.uid }
		var stderr bytes.Buffer
		err := runNetsink(context.Background(), tc.args, &stderr, env)
		select {
		case <-served:
			t.Errorf("%s: a sink was started", name)
		default:
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
		_ = fl.Listener.Close()
		if _, statErr := os.Stat(journal); statErr == nil {
			t.Errorf("%s: a journal was created", name)
			_ = os.Remove(journal)
		}
	}

	// A secret that cannot be read, a short secret and a non-loopback listener from the
	// environment are refused after the arguments passed.
	for name, mutate := range map[string]func(*netsinkEnv){
		"unreadable secret": func(e *netsinkEnv) {
			e.readSecret = func(string) (string, error) { return "", errors.New("permission denied") }
		},
		"short secret": func(e *netsinkEnv) { e.readSecret = func(string) (string, error) { return "abc", nil } },
		"listen failure": func(e *netsinkEnv) {
			e.listen = func(string) (net.Listener, error) { return nil, errors.New("address in use") }
		},
		"non-loopback listener": func(e *netsinkEnv) {
			e.listen = func(string) (net.Listener, error) {
				ln, err := net.Listen("tcp", "0.0.0.0:0")
				if err != nil {
					return nil, err
				}
				return &fakeListener{Listener: &wildcardListener{ln}}, nil
			}
		},
	} {
		env, fl, _, served := nsEnv(t)
		mutate(&env)
		var stderr bytes.Buffer
		if err := runNetsink(context.Background(), ok, &stderr, env); err == nil {
			t.Errorf("%s: accepted", name)
		}
		select {
		case <-served:
			t.Errorf("%s: a sink was started", name)
		default:
		}
		_ = fl.Listener.Close()
		_ = os.Remove(journal)
	}
}

// wildcardListener reports a non-loopback address.
type wildcardListener struct{ net.Listener }

func (wildcardListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 8080} }

func TestNetsinkAddressAndPathsAreTheDocumentedOnes(t *testing.T) {
	if runner.NetsinkAddr != "127.0.33.1:8080" || runner.NetsinkURL != "http://127.0.33.1:8080" {
		t.Fatalf("sink address %q / url %q", runner.NetsinkAddr, runner.NetsinkURL)
	}
	if err := netsinkListenOK(runner.NetsinkAddr); err != nil {
		t.Fatal(err)
	}
	if runner.NetsinkJournalPath != "/var/lib/tbound-netsink/journal.jsonl" || runner.NetsinkSecretPath != "/etc/tbound/netsink.secret" {
		t.Fatalf("paths %q %q", runner.NetsinkJournalPath, runner.NetsinkSecretPath)
	}
}

// The unit is the one place the fixed address, journal and secret paths are written down
// outside Go: hold it to the constants and to its hardening.
func TestNetsinkUnitMatchesTheConstantsAndIsHardened(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "infra", "guest", "systemd", "tbound-netsink.service"))
	if err != nil {
		t.Fatal(err)
	}
	service := map[string][]string{}
	section := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, "["):
			section = line
		case section == "[Service]":
			k, v, _ := strings.Cut(line, "=")
			service[k] = append(service[k], v)
		}
	}
	one := func(key string) string {
		t.Helper()
		if len(service[key]) != 1 {
			t.Fatalf("%s is set %d times, want once", key, len(service[key]))
		}
		return service[key][0]
	}
	wantExec := strings.Join([]string{runner.EvidenceBinaryPath, "netsink", "--listen", runner.NetsinkAddr,
		"--journal", runner.NetsinkJournalPath, "--secret-file", runner.NetsinkSecretPath}, " ")
	if got := one("ExecStart"); got != wantExec {
		t.Fatalf("ExecStart = %q\nwant       %q", got, wantExec)
	}
	for k, want := range map[string]string{
		"User": "tbound-netsink", "Group": "tbound-netsink", "NoNewPrivileges": "yes", "ProtectSystem": "strict",
		"ProtectHome": "yes", "PrivateTmp": "yes", "PrivateDevices": "yes", "CapabilityBoundingSet": "", "AmbientCapabilities": "",
		"ReadWritePaths": filepath.Dir(runner.NetsinkJournalPath), "StateDirectory": "tbound-netsink", "StateDirectoryMode": "0700",
		"IPAddressDeny": "any", "IPAddressAllow": "127.0.0.0/8", "RestrictAddressFamilies": "AF_INET", "MemoryDenyWriteExecute": "yes",
		"RestrictNamespaces": "yes", "RestrictSUIDSGID": "yes", "ProtectKernelTunables": "yes", "ProtectKernelModules": "yes",
		"ProtectControlGroups": "yes", "LockPersonality": "yes", "UMask": "0077",
	} {
		if got := one(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	// The sink's state directory is exactly the journal's directory, and it is the only writable path.
	if filepath.Join("/var/lib", one("StateDirectory")) != filepath.Dir(runner.NetsinkJournalPath) {
		t.Error("StateDirectory is not the journal directory")
	}
	// Not a service or cell identity, and no listen address that collides with systemd-resolved.
	for _, id := range []string{"root", "tbound-pi", "tbound-client"} {
		if one("User") == id {
			t.Errorf("the sink runs as %s", id)
		}
	}
	if host, _, _ := net.SplitHostPort(runner.NetsinkAddr); host == "127.0.0.53" || host == "127.0.0.54" || host == "127.0.0.1" {
		t.Errorf("sink host %s collides with a well-known loopback listener", host)
	}
}
