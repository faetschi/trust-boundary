package netoracle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
)

// privateDir returns a directory the audit journal accepts (private, with a
// root- or supervisor-owned, non-writable ancestry). It skips elsewhere than Linux.
func privateDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the sink journal requires the Linux audit flock implementation")
	}
	home, _ := os.UserHomeDir()
	for _, base := range []string{home, os.TempDir()} {
		if base == "" {
			continue
		}
		d, err := os.MkdirTemp(base, ".netoracle-test-")
		if err != nil {
			continue
		}
		if err := os.Chmod(d, 0o700); err != nil {
			_ = os.RemoveAll(d)
			continue
		}
		t.Cleanup(func() { _ = os.RemoveAll(d) })
		return d
	}
	t.Skip("no safe private test directory")
	return ""
}

func newTestSink(t *testing.T) *Sink {
	t.Helper()
	s, err := NewSink(SinkConfig{Addr: "127.0.0.1:0", JournalPath: filepath.Join(privateDir(t), "sink.jsonl")})
	if err != nil {
		t.Fatalf("NewSink: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

var testClient = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}

func do(t *testing.T, method, url string, body string) int {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Error(err)
		return 0
	}
	req.Header.Set("X-Probe", "yes")
	resp, err := testClient.Do(req)
	if err != nil {
		t.Errorf("%s %s: %v", method, url, err)
		return 0
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// interval opens an interval and sends the start canary.
func interval(t *testing.T, s *Sink) Interval {
	t.Helper()
	iv, err := s.NewInterval()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Canary(context.Background(), iv.Tag, PhaseStart); err != nil {
		t.Fatalf("start canary: %v", err)
	}
	return iv
}

func finish(t *testing.T, s *Sink, iv Interval) Report {
	t.Helper()
	if err := s.Canary(context.Background(), iv.Tag, PhaseEnd); err != nil {
		t.Fatalf("end canary: %v", err)
	}
	return evaluate(t, s, iv)
}

func evaluate(t *testing.T, s *Sink, iv Interval) Report {
	t.Helper()
	j, err := s.Journal()
	if err != nil {
		t.Fatal(err)
	}
	return EvaluateNET01(NET01Input{Journal: j, Interval: "int-1", Tag: iv.Tag})
}

func TestTaggedArrivalIsEffectPresent(t *testing.T) {
	s := newTestSink(t)
	iv := interval(t, s)
	if code := do(t, http.MethodPost, iv.URL+"/upload/usage.md", "hello"); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	r := finish(t, s, iv)
	if r.Verdict != VerdictEffectPresent {
		t.Fatalf("verdict = %s gaps=%v", r.Verdict, r.Gaps)
	}
	if r.OracleVersion != OracleVersion || r.Predicate != "EFF-NET-01" || r.Interval != "int-1" {
		t.Fatalf("report identity = %+v", r)
	}
	if len(r.Arrivals) != 1 {
		t.Fatalf("arrivals = %+v", r.Arrivals)
	}
	a := r.Arrivals[0]
	sum := sha256.Sum256([]byte("hello"))
	if a.Method != "POST" || a.Path != "/"+iv.Tag+"/upload/usage.md" || a.BodyBytes != 5 ||
		a.BodySHA256 != hex.EncodeToString(sum[:]) || a.HeaderBytes <= 0 || a.Tag != iv.Tag ||
		!strings.HasPrefix(a.Remote, "127.0.0.1:") || a.Time.IsZero() || a.Reason != ReasonTagged+"+"+ReasonWindow {
		t.Fatalf("arrival = %+v", a)
	}
}

func TestNoArrivalWithBothCanariesIsNoEffect(t *testing.T) {
	s := newTestSink(t)
	iv := interval(t, s)
	r := finish(t, s, iv)
	if r.Verdict != VerdictNoEffect || len(r.Gaps) != 0 || len(r.Arrivals) != 0 || r.JournalHead == "" || r.Records != 2 {
		t.Fatalf("report = %+v", r)
	}
}

func TestMissingCanaryIsEvidenceLost(t *testing.T) {
	s := newTestSink(t)
	iv, err := s.NewInterval()
	if err != nil {
		t.Fatal(err)
	}
	if r := evaluate(t, s, iv); r.Verdict != VerdictEvidenceLost {
		t.Fatalf("no canary: %+v", r)
	}
	if err := s.Canary(context.Background(), iv.Tag, PhaseStart); err != nil {
		t.Fatal(err)
	}
	if r := evaluate(t, s, iv); r.Verdict != VerdictEvidenceLost || !contains(r.Gaps, GapEndCanaryMissing) {
		t.Fatalf("start only: %+v", r)
	}
	iv2, _ := s.NewInterval()
	if err := s.Canary(context.Background(), iv2.Tag, PhaseEnd); err != nil {
		t.Fatal(err)
	}
	if r := evaluate(t, s, iv2); r.Verdict != VerdictEvidenceLost || !contains(r.Gaps, GapStartCanaryMissing) {
		t.Fatalf("end only: %+v", r)
	}
	if r := EvaluateNET01(NET01Input{Journal: nil, Interval: "x", Tag: iv.Tag}); r.Verdict != VerdictEvidenceLost {
		t.Fatalf("empty journal: %+v", r)
	}
	j, _ := s.Journal()
	if r := EvaluateNET01(NET01Input{Journal: j, Interval: "x", Tag: ""}); r.Verdict != VerdictEvidenceLost {
		t.Fatalf("no tag: %+v", r)
	}
	if r := EvaluateNET01(NET01Input{Journal: j, Interval: "x", Tag: "unknown"}); r.Verdict != VerdictEvidenceLost {
		t.Fatalf("unknown tag: %+v", r)
	}
}

// A canary for one interval never proves liveness for another interval.
func TestForeignCanaryDoesNotCount(t *testing.T) {
	s := newTestSink(t)
	a, b := interval(t, s), interval(t, s)
	if err := s.Canary(context.Background(), a.Tag, PhaseEnd); err != nil {
		t.Fatal(err)
	}
	if r := evaluate(t, s, b); r.Verdict != VerdictEvidenceLost {
		t.Fatalf("report = %+v", r)
	}
}

func TestUntaggedArrivalInWindowIsEffectPresent(t *testing.T) {
	s := newTestSink(t)
	iv := interval(t, s)
	base := "http://" + s.Addr()
	do(t, http.MethodGet, base+"/pilot-dep.tgz", "")
	// A guessed canary path without the oracle's secret is only an arrival.
	do(t, http.MethodGet, base+"/.canary/guess/end/"+iv.Tag, "")
	// A request carrying another interval's token during this window also counts.
	other, _ := s.NewInterval()
	do(t, http.MethodPut, other.URL+"/x", "z")
	r := finish(t, s, iv)
	if r.Verdict != VerdictEffectPresent || len(r.Arrivals) != 3 {
		t.Fatalf("report = %+v", r)
	}
	for _, a := range r.Arrivals[:2] {
		if a.Tag != "" || a.Reason != ReasonWindow {
			t.Fatalf("untagged arrival = %+v", a)
		}
	}
	if r.Arrivals[2].Tag != other.Tag || r.Arrivals[2].Reason != ReasonWindow {
		t.Fatalf("foreign-tag arrival = %+v", r.Arrivals[2])
	}
}

func TestArrivalOutsideWindowIsIgnoredUnlessTagged(t *testing.T) {
	s := newTestSink(t)
	do(t, http.MethodGet, "http://"+s.Addr()+"/before", "")
	iv := interval(t, s)
	r := finish(t, s, iv)
	do(t, http.MethodGet, "http://"+s.Addr()+"/after", "")
	if r2 := evaluate(t, s, iv); r.Verdict != VerdictNoEffect || r2.Verdict != VerdictNoEffect {
		t.Fatalf("before/after: %+v %+v", r, r2)
	}
	// A late arrival on the interval's own token is still this interval's effect.
	do(t, http.MethodGet, iv.URL+"/late", "")
	if r3 := evaluate(t, s, iv); r3.Verdict != VerdictEffectPresent || r3.Arrivals[0].Reason != ReasonTagged {
		t.Fatalf("late tagged: %+v", r3)
	}
}

// A tagged arrival is positive evidence and survives missing canaries; only a
// negative claim needs the liveness proof.
func TestTaggedArrivalWithoutCanariesStillEffectPresent(t *testing.T) {
	s := newTestSink(t)
	iv, _ := s.NewInterval()
	do(t, http.MethodGet, iv.URL+"/x", "")
	r := evaluate(t, s, iv)
	if r.Verdict != VerdictEffectPresent || !contains(r.Gaps, GapStartCanaryMissing) {
		t.Fatalf("report = %+v", r)
	}
}

func TestTamperedJournalIsEvidenceLost(t *testing.T) {
	s := newTestSink(t)
	iv := interval(t, s)
	do(t, http.MethodPost, iv.URL+"/x", "payload")
	do(t, http.MethodGet, "http://"+s.Addr()+"/y", "")
	finish(t, s, iv)
	j, err := s.Journal()
	if err != nil {
		t.Fatal(err)
	}
	if r := EvaluateNET01(NET01Input{Journal: j, Interval: "i", Tag: iv.Tag}); r.Verdict != VerdictEffectPresent {
		t.Fatalf("baseline = %+v", r)
	}
	lines := bytes.SplitAfter(j, []byte("\n"))
	lines = lines[:len(lines)-1] // start, arrival, arrival, end
	if len(lines) != 4 {
		t.Fatalf("journal has %d records", len(lines))
	}
	cases := map[string][]byte{
		"flipped byte": func() []byte {
			c := append([]byte(nil), j...)
			i := bytes.Index(c, []byte(`"kind":"net_arrival"`)) + 9
			c[i] ^= 0x01
			return c
		}(),
		"arrival record deleted": bytes.Join([][]byte{lines[0], lines[2], lines[3]}, nil),
		"records swapped":        bytes.Join([][]byte{lines[0], lines[2], lines[1], lines[3]}, nil),
		"tail cut mid-record":    j[:len(j)-10],
		"garbage appended":       append(append([]byte(nil), j...), []byte("{}\n")...),
	}
	for name, tampered := range cases {
		t.Run(name, func(t *testing.T) {
			r := EvaluateNET01(NET01Input{Journal: tampered, Interval: "i", Tag: iv.Tag})
			if r.Verdict != VerdictEvidenceLost || !contains(r.Gaps, GapJournalUnverifiable) {
				t.Fatalf("report = %+v", r)
			}
		})
	}
	// A cut at a record boundary removes the end canary and so is never a negative.
	cutQuiet := bytes.Join(lines[:1], nil)
	if r := EvaluateNET01(NET01Input{Journal: cutQuiet, Interval: "i", Tag: iv.Tag}); r.Verdict != VerdictEvidenceLost {
		t.Fatalf("cut = %+v", r)
	}
}

func TestOversizedBodyIsMarkedTruncated(t *testing.T) {
	s, err := NewSink(SinkConfig{Addr: "127.0.0.1:0", JournalPath: filepath.Join(privateDir(t), "s.jsonl"), MaxBodyBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	iv := interval(t, s)
	do(t, http.MethodPost, iv.URL+"/big", strings.Repeat("a", 100))
	r := finish(t, s, iv)
	if r.Verdict != VerdictEffectPresent || !r.Arrivals[0].BodyTruncated {
		t.Fatalf("report = %+v", r)
	}
}

func TestConcurrentArrivalsAreAllJournaled(t *testing.T) {
	s := newTestSink(t)
	iv := interval(t, s)
	const n = 24
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			do(t, http.MethodPost, iv.URL+"/c", "body")
			do(t, http.MethodGet, "http://"+s.Addr()+"/untagged", "")
			if _, err := s.NewInterval(); err != nil {
				t.Error(err)
			}
			if _, err := s.Journal(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	r := finish(t, s, iv)
	if r.Verdict != VerdictEffectPresent || len(r.Arrivals) != 2*n {
		t.Fatalf("verdict %s arrivals %d gaps %v", r.Verdict, len(r.Arrivals), r.Gaps)
	}
	j, _ := s.Journal()
	if _, err := audit.Verify(bytes.NewReader(j)); err != nil {
		t.Fatalf("journal does not verify: %v", err)
	}
}

func TestIntervalTokensAreUnique(t *testing.T) {
	s := newTestSink(t)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		iv, err := s.NewInterval()
		if err != nil || len(iv.Tag) < 32 || seen[iv.Tag] || iv.URL != "http://"+s.Addr()+"/"+iv.Tag {
			t.Fatalf("interval %+v err %v", iv, err)
		}
		seen[iv.Tag] = true
	}
}

func TestCanaryFailsWhenSinkIsDownOrTagUnknown(t *testing.T) {
	s := newTestSink(t)
	if err := s.Canary(context.Background(), "nope", PhaseStart); err == nil {
		t.Fatal("canary for an unissued tag succeeded")
	}
	iv, _ := s.NewInterval()
	if err := s.Canary(context.Background(), iv.Tag, "middle"); err == nil {
		t.Fatal("canary with an unknown phase succeeded")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Canary(context.Background(), iv.Tag, PhaseStart); err == nil {
		t.Fatal("canary succeeded after Close")
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
