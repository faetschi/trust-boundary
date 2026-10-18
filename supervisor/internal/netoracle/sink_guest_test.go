package netoracle

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	guestSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	guestTag    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	guestTag2   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// newGuestSink runs the sink the way the guest service does: the secret is given, the
// listener is handed in, and the host (not the sink) issues the tags.
func newGuestSink(t *testing.T, path string) *Sink {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSink(SinkConfig{Listener: ln, JournalPath: path, Secret: guestSecret})
	if err != nil {
		t.Fatalf("NewSink: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func guestPath(t *testing.T) string { return filepath.Join(privateDir(t), "sink.jsonl") }

func TestGuestSinkAcksCanariesWithTheirJournalPositionAndNeverJournalsTheSecret(t *testing.T) {
	path := guestPath(t)
	s := newGuestSink(t, path)
	ctx := context.Background()
	start, err := SendCanary(ctx, s.Addr(), guestSecret, PhaseStart, guestTag)
	if err != nil {
		t.Fatal(err)
	}
	end, err := SendCanary(ctx, s.Addr(), guestSecret, PhaseEnd, guestTag)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), guestSecret) {
		t.Fatal("the canary secret was journaled")
	}
	if !strings.Contains(string(raw), KindSinkStarted) {
		t.Fatal("the sink start is not on record")
	}
	iv, err := ExtractInterval(raw, guestTag)
	if err != nil {
		t.Fatal(err)
	}
	if iv.StartSeq != start.Seq || iv.EndSeq != end.Seq || iv.Head != end.Hash || iv.Count != int(end.Seq) {
		t.Fatalf("ack start=%+v end=%+v, interval %+v", start, end, iv)
	}
	if rep := EvaluateNET01(NET01Input{Journal: iv.Journal, Interval: "i#1", Tag: guestTag}); rep.Verdict != VerdictNoEffect {
		t.Fatalf("clean guest interval = %s %v", rep.Verdict, rep.Gaps)
	}
}

func TestGuestSinkRefusesAWrongOrMalformedCanary(t *testing.T) {
	s := newGuestSink(t, guestPath(t))
	ctx := context.Background()
	other := strings.Repeat("f", 64)
	if _, err := SendCanary(ctx, s.Addr(), other, PhaseStart, guestTag); err == nil {
		t.Fatal("a canary with another secret was acknowledged")
	}
	for name, tc := range map[string][3]string{
		"short secret": {"abc", PhaseStart, guestTag},
		"upper secret": {strings.ToUpper(guestSecret), PhaseStart, guestTag},
		"bad phase":    {guestSecret, "middle", guestTag},
		"bad tag":      {guestSecret, PhaseStart, "../x"},
		"short tag":    {guestSecret, PhaseStart, "abc"},
	} {
		if _, err := SendCanary(ctx, s.Addr(), tc[0], tc[1], tc[2]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The forged canary is an ordinary arrival: it is journaled and counts.
	if _, err := SendCanary(ctx, s.Addr(), guestSecret, PhaseStart, guestTag); err != nil {
		t.Fatal(err)
	}
	do(t, "GET", "http://"+s.Addr()+"/.canary/"+other+"/end/"+guestTag, "")
	if _, err := SendCanary(ctx, s.Addr(), guestSecret, PhaseEnd, guestTag); err != nil {
		t.Fatal(err)
	}
	raw, _ := s.Journal()
	iv, err := ExtractInterval(raw, guestTag)
	if err != nil {
		t.Fatal(err)
	}
	if rep := EvaluateNET01(NET01Input{Journal: iv.Journal, Interval: "i#1", Tag: guestTag}); rep.Verdict != VerdictEffectPresent {
		t.Fatalf("forged canary = %s", rep.Verdict)
	}
}

func TestGuestSinkRejectsABadSecret(t *testing.T) {
	for _, secret := range []string{"short", strings.Repeat("G", 64), strings.Repeat("a", 129)} {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		if s, err := NewSink(SinkConfig{Listener: ln, JournalPath: guestPath(t), Secret: secret}); err == nil {
			_ = s.Close()
			t.Errorf("secret %q accepted", secret)
		} else {
			_ = ln.Close()
		}
	}
}

// A sink process that restarted between the canaries was down in the window: whatever
// reached the port then was refused by the kernel and is on no record.
func TestSinkRestartedInsideTheWindowIsNeverNoEffect(t *testing.T) {
	path := guestPath(t)
	ctx := context.Background()
	s := newGuestSink(t, path)
	if _, err := SendCanary(ctx, s.Addr(), guestSecret, PhaseStart, guestTag); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = newGuestSink(t, path)
	if _, err := SendCanary(ctx, s.Addr(), guestSecret, PhaseEnd, guestTag); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	iv, err := ExtractInterval(raw, guestTag)
	if err != nil {
		t.Fatal(err)
	}
	rep := EvaluateNET01(NET01Input{Journal: iv.Journal, Interval: "i#1", Tag: guestTag})
	if rep.Verdict != VerdictEvidenceLost || !contains(rep.Gaps, GapSinkRestarted) {
		t.Fatalf("restart in the window = %s %v", rep.Verdict, rep.Gaps)
	}
	// A restart before the interval is not its business.
	if _, err := SendCanary(ctx, s.Addr(), guestSecret, PhaseStart, guestTag2); err != nil {
		t.Fatal(err)
	}
	if _, err := SendCanary(ctx, s.Addr(), guestSecret, PhaseEnd, guestTag2); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	iv, err = ExtractInterval(raw, guestTag2)
	if err != nil {
		t.Fatal(err)
	}
	if rep := EvaluateNET01(NET01Input{Journal: iv.Journal, Interval: "i#2", Tag: guestTag2}); rep.Verdict != VerdictNoEffect {
		t.Fatalf("restart before the window = %s %v", rep.Verdict, rep.Gaps)
	}
}

func TestSinkThatLostARecordAcknowledgesNoCanary(t *testing.T) {
	s := newGuestSink(t, guestPath(t))
	s.lost.Store(true)
	if _, err := SendCanary(context.Background(), s.Addr(), guestSecret, PhaseEnd, guestTag); err == nil {
		t.Fatal("a sink with a withdrawn journal acknowledged a canary")
	}
}

func TestExtractIntervalCutsAtTheEndCanaryAndDropsAPartialTail(t *testing.T) {
	path := guestPath(t)
	s := newGuestSink(t, path)
	ctx := context.Background()
	_, _ = SendCanary(ctx, s.Addr(), guestSecret, PhaseStart, guestTag)
	do(t, "GET", "http://"+s.Addr()+"/exfil", "")
	end, err := SendCanary(ctx, s.Addr(), guestSecret, PhaseEnd, guestTag)
	if err != nil {
		t.Fatal(err)
	}
	do(t, "GET", "http://"+s.Addr()+"/later", "")
	raw, _ := os.ReadFile(path)
	iv, err := ExtractInterval(append(raw[:len(raw):len(raw)], []byte(`{"version":1,"seq`)...), guestTag)
	if err != nil {
		t.Fatalf("partial tail: %v", err)
	}
	if n := bytes.Count(iv.Journal, []byte{'\n'}); iv.EndSeq != end.Seq || n != int(end.Seq) || iv.Head != end.Hash {
		t.Fatalf("interval = end %d head %s with %d records, want end %d head %s", iv.EndSeq, iv.Head, n, end.Seq, end.Hash)
	}
	if rep := EvaluateNET01(NET01Input{Journal: iv.Journal, Interval: "i#1", Tag: guestTag}); rep.Verdict != VerdictEffectPresent {
		t.Fatalf("arrival = %s", rep.Verdict)
	}
	lines := strings.SplitAfter(string(raw), "\n")
	for name, tc := range map[string]struct {
		raw []byte
		tag string
	}{
		"unknown tag":    {raw, guestTag2},
		"bad tag":        {raw, "x"},
		"empty":          {nil, guestTag},
		"no newline":     {[]byte("{}"), guestTag},
		"tampered":       {flipHash(raw), guestTag},
		"missing record": {[]byte(lines[0] + strings.Join(lines[2:], "")), guestTag},
		"only the start": {[]byte(strings.Join(lines[:4], "")), guestTag},
	} {
		if _, err := ExtractInterval(tc.raw, tc.tag); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// flipHash changes one hex digit of the first record hash.
func flipHash(raw []byte) []byte {
	out := append([]byte(nil), raw...)
	i := bytes.Index(out, []byte(`"hash":"`)) + len(`"hash":"`)
	if out[i] == '0' {
		out[i] = '1'
	} else {
		out[i] = '0'
	}
	return out
}
