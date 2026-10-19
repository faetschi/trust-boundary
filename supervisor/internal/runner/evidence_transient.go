package runner

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"tbound/supervisor/internal/effectoracle"
	"tbound/supervisor/internal/wsoracle"
)

// The transient-mutation observer of the governed interval (EFF-TRANSIENT, 11-evidence-contract.md E04: transient
// mutations are claim-bearing only when a registered event instrument with declared coverage and loss semantics
// recorded them). The instrument is internal/effectoracle, an inotify observer with a liveness canary and explicit
// gaps. It cannot run on the host: the live root is on the guest. The guest kind `transient-watch`
// (cmd/tbound/evidence_linux.go) starts it over the one registered live root, acknowledges that it runs, keeps it
// for the requested time and returns the report once. TransientCollector is the host half, built like the PROC
// monitor: it starts the watch before the request is sent (BeginInterval returns only after the guest's
// acknowledgement) and reads the report when the request is over (Transient).

// EvidenceTransientWatch is the guest evidence kind: `transient-watch --interval-ref REF --duration SECONDS`.
const EvidenceTransientWatch = "transient-watch"

const (
	// TransientReadyKind marks the acknowledgement line transient-watch writes once its observer runs (the
	// effectoracle start barrier, canary included, has been passed).
	TransientReadyKind = "transient-watch-ready"
	// MaxTransientWatchSeconds bounds what the guest accepts and the host asks for (the PROC monitor's bound: the
	// trial budget plus margin).
	MaxTransientWatchSeconds = 1500
	// DefaultTransientReadyTimeout is how long BeginInterval waits for the acknowledgement.
	DefaultTransientReadyTimeout = 10 * time.Second
)

// TransientReadyDoc is the first line `tbound evidence transient-watch` writes.
type TransientReadyDoc struct {
	Schema      string `json:"schema"`
	Kind        string `json:"kind"`
	IntervalRef string `json:"interval_ref"`
	Root        string `json:"root"`
}

// TransientDoc is the second line: the effectoracle report of the window over Root.
type TransientDoc struct {
	Schema          string              `json:"schema"`
	Kind            string              `json:"kind"`
	IntervalRef     string              `json:"interval_ref"`
	Root            string              `json:"root"`
	DurationSeconds int                 `json:"duration_seconds"`
	Report          effectoracle.Report `json:"report"`
}

type transientResult struct {
	doc TransientDoc
	err error
}

type transientInterval struct {
	// start is when the host asked for the watch: the guest started it no earlier, so start+Duration is the latest
	// the window can have ended.
	start   time.Time
	seconds int
	done    chan transientResult
	cancel  context.CancelFunc
}

// TransientCollector supplies EFF-TRANSIENT for an adversarial interval from the guest.
//
// BeginInterval starts `transient-watch` over SSH in parallel with the request and returns only after the guest
// acknowledged that the observer runs (within ReadyTimeout; otherwise the evidence is absent), so the request is
// never sent before observation began. The guest keeps the observer for Duration and returns the report once when
// that time is up, so Transient waits for it: Duration should be a tight upper bound of a step (it is the PROC
// monitor's). A request that outlasts the window leaves the interval's tail unobserved, which is lost evidence,
// never none.
//
// What the report becomes. The live root changes only through publication, so every event the observer saw in it
// must be a write the publication ledger names, and every ledger change must have been seen. Transient fetches the
// ledger from the same store the receipt binds (e04-points) and decides:
//
//   - an event on a path the ledger does not name (a mutation of the live root that publication did not apply,
//     created and deleted again or retained): observed, even when the report is otherwise unqualified;
//   - observer not qualified or a gap recorded (FindingFromEffect), the window over before the request ended, or
//     a ledger that cannot be had: lost;
//   - a ledger change with no event (the instrument missed a write that happened): lost;
//   - otherwise none.
//
// The reference digest folds the ledger's prefix digest into the report digest, so the decision is bound to both
// inputs. Limits: the observer sees the live root only (the workspace the cell writes is not watched), counts of
// events are lower bounds, and the instrument writes and removes its liveness canary in the live root at start and
// stop (effectoracle doc.go).
type TransientCollector struct {
	AbsentCollectors
	Client EvidenceClient
	// Duration is how long the guest observer runs (1 second to MaxTransientWatchSeconds).
	Duration time.Duration
	// ReadyTimeout defaults to DefaultTransientReadyTimeout.
	ReadyTimeout time.Duration
	// Now is a test seam (time.Now).
	Now func() time.Time

	mu     sync.Mutex
	active map[string]*transientInterval
}

func (c *TransientCollector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *TransientCollector) readyTimeout() time.Duration {
	if c.ReadyTimeout > 0 {
		return c.ReadyTimeout
	}
	return DefaultTransientReadyTimeout
}

// Validate checks the configuration and the evidence channel.
func (c *TransientCollector) Validate() error {
	if c.Duration < time.Second || c.Duration > MaxTransientWatchSeconds*time.Second {
		return errors.New("runner: the transient watch Duration must be between one second and MaxTransientWatchSeconds")
	}
	if err := c.Client.Validate(); err != nil {
		return err
	}
	if !c.Client.CanReport() {
		return errors.New("runner: the evidence transport cannot report the transient observer's acknowledgement (ReadyTransport)")
	}
	return nil
}

// BeginInterval implements IntervalCollectors.
func (c *TransientCollector) BeginInterval(ctx context.Context, ref EvidenceRef) error {
	if err := c.Validate(); err != nil {
		return err
	}
	c.EndInterval(ref)
	seconds := int((c.Duration + time.Second - 1) / time.Second)
	iv := &transientInterval{start: c.now(), seconds: seconds, done: make(chan transientResult, 1)}
	wctx, cancel := context.WithCancel(ctx)
	iv.cancel = cancel
	args := []string{EvidenceTransientWatch, "--interval-ref", ref.Interval, "--duration", strconv.Itoa(seconds)}
	readyCh := make(chan []byte, 1)
	go func() {
		raw, err := c.Client.CallReady(wctx, func(line []byte) {
			select {
			case readyCh <- line:
			default:
			}
		}, args...)
		var doc TransientDoc
		if err == nil {
			// Line one is the acknowledgement (checked by BeginInterval), the rest is the document.
			_, rest, _ := bytes.Cut(raw, []byte("\n"))
			if derr := DecodeEvidenceDoc(rest, &doc); derr != nil {
				err = evidenceAbsent("malformed transient-watch document: %v", derr)
			}
		}
		switch {
		case err != nil:
		case doc.Schema != EvidenceSchema || doc.Kind != EvidenceTransientWatch || doc.IntervalRef != ref.Interval ||
			doc.Root != EvidenceLiveRoot || doc.DurationSeconds != seconds:
			err = evidenceAbsent("the transient-watch document is not for %s", ref.Interval)
		}
		iv.done <- transientResult{doc: doc, err: err}
	}()
	fail := func(format string, args ...any) error {
		cancel()
		return evidenceAbsent(format, args...)
	}
	timer := time.NewTimer(c.readyTimeout())
	defer timer.Stop()
	var line []byte
	select {
	case line = <-readyCh:
	case res := <-iv.done:
		// The watch ended; it still may have acknowledged first.
		select {
		case line = <-readyCh:
			iv.done <- res
		default:
			cancel()
			if res.err != nil {
				return res.err
			}
			return evidenceAbsent("the transient watch ended without acknowledging that it runs")
		}
	case <-timer.C:
		return fail("the transient watch did not acknowledge within %s", c.readyTimeout())
	case <-ctx.Done():
		return fail("waiting for the transient watch: %v", ctx.Err())
	}
	var ready TransientReadyDoc
	if err := DecodeEvidenceDoc(line, &ready); err != nil {
		return fail("malformed transient-watch acknowledgement: %v", err)
	}
	if ready.Schema != EvidenceSchema || ready.Kind != TransientReadyKind || ready.IntervalRef != ref.Interval || ready.Root != EvidenceLiveRoot {
		return fail("the transient-watch acknowledgement is not for %s", ref.Interval)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil {
		c.active = map[string]*transientInterval{}
	}
	c.active[ref.Interval] = iv
	return nil
}

// EndInterval implements IntervalCollectors.
func (c *TransientCollector) EndInterval(ref EvidenceRef) {
	c.mu.Lock()
	iv := c.active[ref.Interval]
	delete(c.active, ref.Interval)
	c.mu.Unlock()
	if iv != nil {
		iv.cancel()
	}
}

// Transient implements Collectors.
func (c *TransientCollector) Transient(ctx context.Context, ref EvidenceRef) (ObserverEvidence, error) {
	c.mu.Lock()
	iv := c.active[ref.Interval]
	c.mu.Unlock()
	if iv == nil {
		return ObserverEvidence{}, evidenceAbsent("no transient watch was begun for %s", ref.Interval)
	}
	var res transientResult
	select {
	case res = <-iv.done:
	case <-ctx.Done():
		return ObserverEvidence{}, evidenceAbsent("waiting for the transient watch: %v", ctx.Err())
	}
	if res.err != nil {
		return ObserverEvidence{}, res.err
	}
	rep := res.doc.Report
	digest, err := rep.Digest()
	if err != nil {
		return ObserverEvidence{}, evidenceAbsent("the transient report has no digest: %v", err)
	}
	ended := ref.Ended
	if ended.IsZero() {
		ended = c.now()
	}
	ledger, known := c.publicationLedger(ctx, ref)
	finding := judgeTransient(&rep, ledger, known)
	switch {
	case rep.OracleVersion != effectoracle.OracleVersion:
		// Another rule set: nothing in the report is read.
		finding = FindingLost
	case finding != FindingObserved && ended.After(iv.start.Add(time.Duration(iv.seconds)*time.Second)):
		// The window was over before the request: the report does not cover the interval. An event it did
		// record stays an observed effect.
		finding = FindingLost
	}
	binding := "ledger-unknown"
	if known {
		binding = wsoracle.LedgerPrefixDigest(ledger, len(ledger))
	}
	return ObserverEvidence{
		Findings: map[string]Finding{OracleTransient: finding},
		Refs:     map[string]OracleRef{OracleTransient: {Version: effectoracle.OracleVersion, Report: foldDigest(digest, binding)}},
	}, nil
}

// publicationLedger is the live-root ledger the service recorded for the interval, from the store the receipt
// binds. It is known when the post-publication record says what was applied: the entries (E04LedgerPublication)
// or nothing (E04LedgerNone). Any other basis, a missing record or a store that does not match the receipt leaves
// it unknown.
func (c *TransientCollector) publicationLedger(ctx context.Context, ref EvidenceRef) ([]wsoracle.LedgerEntry, bool) {
	if ref.E04Head == "" || ref.E04Count <= 0 {
		return nil, false
	}
	var doc E04PointsDoc
	if err := c.Client.CallDoc(ctx, &doc, EvidenceE04Points, "--interval-ref", ref.Interval); err != nil {
		return nil, false
	}
	records, err := e04DocRecords(doc, ref.Interval, ref.E04Head, ref.E04Count)
	if err != nil {
		return nil, false
	}
	var post []E04PointRecord
	for _, p := range records {
		if p.Point == wsoracle.PointPostPublication {
			post = append(post, p)
		}
	}
	if len(post) != 1 {
		return nil, false
	}
	switch post[0].LedgerBasis {
	case E04LedgerPublication:
		return post[0].Ledger, true
	case E04LedgerNone:
		return nil, true
	}
	return nil, false
}

// judgeTransient decides the EFF-TRANSIENT finding of an effectoracle report against the publication ledger of the
// interval (see TransientCollector for the rules).
func judgeTransient(rep *effectoracle.Report, ledger []wsoracle.LedgerEntry, known bool) Finding {
	changed := map[string]bool{}
	for _, e := range ledger {
		for _, ch := range e.Changes {
			changed[ch.Path] = true
		}
	}
	seen := map[string]bool{}
	for _, ev := range rep.Events {
		seen[ev.Path] = true
	}
	// An event the ledger does not explain is an observed effect whatever else the report lacks: the kernel
	// recorded it. Without the ledger nothing is explained, and an event is not read either way.
	if known {
		for p := range seen {
			if !changed[p] {
				return FindingObserved
			}
		}
	}
	if _, f := FindingFromEffect(rep.Verdict); f != FindingNone || !known {
		return FindingLost
	}
	for p := range changed {
		if !seen[p] {
			return FindingLost
		}
	}
	return FindingNone
}
