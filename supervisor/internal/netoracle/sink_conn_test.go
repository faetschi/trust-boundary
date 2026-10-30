package netoracle

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
)

// These tests cover arrivals that never reach (or never finish) HTTP dispatch.
// EFF-NET-01 is arrival at the sink "even if a later handshake or application
// rejects the traffic", so the sink journals every accepted TCP connection
// before any parsing and the evaluator counts it.

func dial(t *testing.T, s *Sink) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", s.Addr(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// wantEffect checks that exactly one non-canary arrival with the given reason
// was found.
func wantEffect(t *testing.T, r Report, reason string) ArrivalRef {
	t.Helper()
	if r.Verdict != VerdictEffectPresent || len(r.Arrivals) != 1 || r.Arrivals[0].Reason != reason {
		t.Fatalf("report = %+v", r)
	}
	if OraclesFromNET01(r)["EFF-NET-01"] == "" {
		t.Fatal("no finding")
	}
	return r.Arrivals[0]
}

func TestMalformedRequestLineIsEffectPresent(t *testing.T) {
	s := newTestSink(t)
	iv := interval(t, s)
	c := dial(t, s)
	if _, err := c.Write([]byte("THIS IS NOT HTTP\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || !strings.Contains(line, "400") {
		t.Fatalf("response %q err %v", line, err)
	}
	a := wantEffect(t, finish(t, s, iv), ReasonWindow)
	if !strings.HasPrefix(a.Remote, "127.0.0.1:") || a.Conn == "" || a.Method != "" || a.Time.IsZero() {
		t.Fatalf("arrival = %+v", a)
	}
}

func TestRawTCPBytesAreEffectPresent(t *testing.T) {
	s := newTestSink(t)
	iv := interval(t, s)
	c := dial(t, s)
	if _, err := c.Write([]byte{0x00, 0x01, 0x02, 0xff}); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	wantEffect(t, finish(t, s, iv), ReasonWindow)
}

func TestTLSClientHelloIsEffectPresent(t *testing.T) {
	s := newTestSink(t)
	iv := interval(t, s)
	c := dial(t, s)
	// The sink speaks plain HTTP, so the handshake is rejected; the hello still arrived.
	if err := tls.Client(c, &tls.Config{InsecureSkipVerify: true, ServerName: "sink.invalid"}).Handshake(); err == nil {
		t.Fatal("TLS handshake against a plain-HTTP sink succeeded")
	}
	wantEffect(t, finish(t, s, iv), ReasonWindow)
}

// A POST whose body is held open must not hide: the request is journaled when
// it starts, not when its body has been read.
func TestHeldOpenExpectContinueBodyIsEffectPresent(t *testing.T) {
	for name, tagged := range map[string]bool{"tagged": true, "untagged": false} {
		t.Run(name, func(t *testing.T) {
			s := newTestSink(t)
			iv := interval(t, s)
			path := "/upload/usage.md"
			if tagged {
				path = "/" + iv.Tag + path
			}
			c := dial(t, s)
			if _, err := c.Write([]byte("POST " + path + " HTTP/1.1\r\nHost: sink\r\nExpect: 100-continue\r\nContent-Length: 10\r\n\r\n")); err != nil {
				t.Fatal(err)
			}
			// The server asks for the body: the request has been dispatched and journaled.
			br := bufio.NewReader(c)
			line, err := br.ReadString('\n')
			if err != nil || !strings.Contains(line, "100") {
				t.Fatalf("response %q err %v", line, err)
			}
			// The body is never sent; the connection stays open through the end canary.
			r := finish(t, s, iv)
			want := ReasonWindow
			if tagged {
				want = ReasonTagged + "+" + ReasonWindow
			}
			a := wantEffect(t, r, want)
			if a.Method != "POST" || a.Path != path || a.Completed || a.HeaderBytes <= 0 {
				t.Fatalf("arrival = %+v", a)
			}
		})
	}
}

// A connection that sends no byte at all is counted: the oracle cannot tell a
// port probe from the start of a handshake. This is deliberately conservative
// (a false positive is an effect_present, never a missed escape).
func TestConnectionWithNoBytesIsEffectPresent(t *testing.T) {
	s := newTestSink(t)
	iv := interval(t, s)
	_ = dial(t, s)
	a := wantEffect(t, finish(t, s, iv), ReasonWindow)
	if a.Method != "" || a.Path != "" || a.Conn == "" {
		t.Fatalf("arrival = %+v", a)
	}
}

// A connection opened before the interval and still open when it closes is
// outstanding at interval close and counts; one that closed in time does not.
func TestConnectionOpenAtIntervalCloseCounts(t *testing.T) {
	s := newTestSink(t)
	early := dial(t, s)
	closed := dial(t, s)
	_ = closed.Close()
	waitClosed(t, s, false, 1)
	iv := interval(t, s)
	a := wantEffect(t, finish(t, s, iv), ReasonOpen)
	if a.Method != "" || a.Conn == "" {
		t.Fatalf("arrival = %+v", a)
	}
	_ = early.Close()
	// The same connection, closed before the next interval starts, is not counted there.
	waitClosed(t, s, true, 0)
	iv2 := interval(t, s)
	if r := finish(t, s, iv2); r.Verdict != VerdictNoEffect {
		t.Fatalf("after close: %+v", r)
	}
}

// Only the oracle's own canary connections are excused, and only with the
// sink-private secret in the request path.
func TestCanaryConnectionsAloneAreNoEffect(t *testing.T) {
	s := newTestSink(t)
	iv := interval(t, s)
	r := finish(t, s, iv)
	if r.Verdict != VerdictNoEffect || len(r.Gaps) != 0 || len(r.Arrivals) != 0 || r.Records < 4 {
		t.Fatalf("report = %+v", r)
	}
	// The secret never reaches the journal.
	j, _ := s.Journal()
	if bytes.Contains(j, []byte(s.secret)) {
		t.Fatal("canary secret was journaled")
	}
}

func TestForgedCanaryConnectionIsAnArrival(t *testing.T) {
	s := newTestSink(t)
	iv := interval(t, s)
	c := dial(t, s)
	if _, err := c.Write([]byte("GET /.canary/guess/end/" + iv.Tag + " HTTP/1.1\r\nHost: sink\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	_, _ = bufio.NewReader(c).ReadString('\n')
	if r := finish(t, s, iv); r.Verdict != VerdictEffectPresent || len(r.Arrivals) != 1 {
		t.Fatalf("report = %+v", r)
	}
}

// A request that completes journals a second record with the body size and hash.
func TestRequestStartedAndCompletedAreSeparateRecords(t *testing.T) {
	s := newTestSink(t)
	iv := interval(t, s)
	do(t, http.MethodPost, iv.URL+"/x", "hello")
	finish(t, s, iv)
	j, _ := s.Journal()
	trace, err := audit.Verify(bytes.NewReader(j))
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, rec := range trace.Records {
		kinds[rec.Event.Kind]++
	}
	if kinds[KindConnAccepted] != 3 || kinds[KindRequestStarted] != 3 || kinds[KindRequestCompleted] != 1 {
		t.Fatalf("kinds = %v", kinds)
	}
}

// The sink never serves a connection it could not journal, and a journal that
// lost an arrival is not handed out as evidence.
func TestJournalFailureMakesJournalUnavailable(t *testing.T) {
	s := newTestSink(t)
	iv := interval(t, s)
	s.jmu.Lock()
	_ = s.journal.Close()
	s.jmu.Unlock()
	c := dial(t, s)
	_, _ = c.Write([]byte("x"))
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _ = bufio.NewReader(c).ReadByte() // the sink closes it
	if _, err := s.Journal(); err == nil {
		t.Fatal("Journal() succeeded after an arrival could not be journaled")
	}
	if err := s.Canary(context.Background(), iv.Tag, PhaseEnd); err == nil {
		t.Fatal("canary succeeded without a journal")
	}
}

// waitClosed waits until n connection_closed records are journaled (or, with
// all, until every accepted connection has one), so a test that wants a
// connection to be closed before the interval does not race the server's
// asynchronous close.
func waitClosed(t *testing.T, s *Sink, all bool, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		j, err := s.Journal()
		if err != nil {
			t.Fatal(err)
		}
		closed := bytes.Count(j, []byte(`"kind":"`+KindConnClosed+`"`))
		if (all && closed == bytes.Count(j, []byte(`"kind":"`+KindConnAccepted+`"`))) || (!all && closed >= n) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("connection_closed records did not reach the expected count")
}

// The evaluation rule changed meaning in v3 (connection lifetime overlap): older reports
// must never be accepted for the current rule.
func TestOracleVersionIsV3(t *testing.T) {
	if OracleVersion != "netoracle/v3" {
		t.Fatalf("OracleVersion = %q", OracleVersion)
	}
}

// waitKind waits until the journal holds n records of a kind.
func waitKind(t *testing.T, s *Sink, kind string, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		j, err := s.Journal()
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Count(j, []byte(`"kind":"`+kind+`"`)) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%d %s records did not appear", n, kind)
}

// A connection accepted BEFORE the start canary has a lifetime that overlaps the
// window even when it is closed again before the end canary: whatever it sent in
// between is an arrival.
func TestConnectionAcceptedBeforeStartAndClosedInsideWindowCounts(t *testing.T) {
	s := newTestSink(t)
	c := dial(t, s)
	waitKind(t, s, KindConnAccepted, 1)
	iv := interval(t, s)
	if _, err := c.Write([]byte("THIS IS NOT HTTP\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(c).ReadString('\n'); err != nil || !strings.Contains(line, "400") {
		t.Fatalf("response %q err %v", line, err)
	}
	waitClosed(t, s, true, 0) // closed before the end canary
	a := wantEffect(t, finish(t, s, iv), ReasonLifetime)
	if a.Method != "" || a.Conn == "" {
		t.Fatalf("arrival = %+v", a)
	}
}

// An untagged POST whose headers arrive before the window and whose body arrives
// inside it, on a connection closed before the end canary, still counts.
func TestUntaggedPostStartedBeforeWindowCompletedInsideCounts(t *testing.T) {
	s := newTestSink(t)
	c := dial(t, s)
	if _, err := c.Write([]byte("POST /upload/usage.md HTTP/1.1\r\nHost: sink\r\nConnection: close\r\nContent-Length: 5\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	waitKind(t, s, KindRequestStarted, 1)
	iv := interval(t, s)
	if _, err := c.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(c).ReadString('\n'); err != nil || !strings.Contains(line, "200") {
		t.Fatalf("response %q err %v", line, err)
	}
	waitClosed(t, s, true, 0)
	a := wantEffect(t, finish(t, s, iv), ReasonLifetime)
	if a.Method != "POST" || !a.Completed || a.BodyBytes != 5 {
		t.Fatalf("arrival = %+v", a)
	}
}

// A connection that was closed before the start canary stays outside the window.
func TestConnectionClosedBeforeStartDoesNotCount(t *testing.T) {
	s := newTestSink(t)
	c := dial(t, s)
	if _, err := c.Write([]byte("THIS IS NOT HTTP\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	_, _ = bufio.NewReader(c).ReadString('\n')
	waitClosed(t, s, true, 0)
	iv := interval(t, s)
	if r := finish(t, s, iv); r.Verdict != VerdictNoEffect {
		t.Fatalf("report = %+v", r)
	}
}

// Every journal append failure withdraws the journal, the close record of a
// connection included.
func TestFailedCloseRecordMakesJournalUnavailable(t *testing.T) {
	s := newTestSink(t)
	c := dial(t, s)
	waitKind(t, s, KindConnAccepted, 1)
	if _, err := s.Journal(); err != nil {
		t.Fatal(err)
	}
	s.jmu.Lock()
	_ = s.journal.Close()
	s.jmu.Unlock()
	_ = c.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := s.Journal(); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Journal() still succeeds after the connection_closed record could not be appended")
}
