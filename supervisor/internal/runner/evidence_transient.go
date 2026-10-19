package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
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
// until the host ends the watch and returns the report once. TransientCollector is the host half, built like the PROC
// monitor: it starts the watch before the request is sent (BeginInterval returns only after the guest's
// acknowledgement) and ends it, reading the report, when the request is over (Transient).

// EvidenceTransientWatch is the guest evidence kind: `transient-watch --interval-ref REF --duration SECONDS`.
const EvidenceTransientWatch = "transient-watch"

const (
	// TransientReadyKind marks the acknowledgement line transient-watch writes once its observer runs (the
	// effectoracle start barrier, canary included, has been passed).
	TransientReadyKind = "transient-watch-ready"
	// MaxTransientWatchSeconds bounds what the guest accepts and the host asks for (the PROC monitor's bound: the
	// trial budget plus margin). It is the most the guest keeps a watch that nobody ended.
	MaxTransientWatchSeconds = 1500
	// DefaultTransientReadyTimeout is how long BeginInterval waits for the acknowledgement.
	DefaultTransientReadyTimeout = 10 * time.Second
	// DefaultTransientStopTimeout is how long Transient waits for the report after it asked the guest to stop.
	DefaultTransientStopTimeout = 60 * time.Second

	// TransientStopLine is what the host writes to the watch's stdin to end it. The guest then stops the observer (the
	// stop canary and the full-tree snapshot are written then, while the session is open) and reports. A stdin that
	// ends without this line is an abort: the guest writes nothing into the live root and reports an error.
	TransientStopLine = "stop\n"

	// The reasons a guest watch stopped (TransientDoc.Stop). Only a stop the host requested is known to end after
	// the request did.
	TransientStopRequested = "requested"
	TransientStopTimer     = "timer"
)

// TransientReadyDoc is the first line `tbound evidence transient-watch` writes.
type TransientReadyDoc struct {
	Schema      string `json:"schema"`
	Kind        string `json:"kind"`
	IntervalRef string `json:"interval_ref"`
	Root        string `json:"root"`
}

// TransientDoc is the second line: the effectoracle report of the window over Root. DurationSeconds is the cap the
// host asked for (the longest the guest keeps an unended watch). ElapsedMicros is the window the guest actually
// observed, from the start barrier to the stop barrier, on its own monotonic clock, and Stop says why it ended.
type TransientDoc struct {
	Schema          string              `json:"schema"`
	Kind            string              `json:"kind"`
	IntervalRef     string              `json:"interval_ref"`
	Root            string              `json:"root"`
	DurationSeconds int                 `json:"duration_seconds"`
	ElapsedMicros   int64               `json:"elapsed_us"`
	Stop            string              `json:"stop"`
	Report          effectoracle.Report `json:"report"`
}

type transientResult struct {
	doc TransientDoc
	err error
}

type transientInterval struct {
	// acked is when the guest's acknowledgement arrived. The guest started the observer before it acknowledged, and
	// the request is sent after the acknowledgement.
	acked   time.Time
	seconds int
	// stdin ends the watch: the stop line asks for the report, closing it without the line aborts the watch.
	stdin    *io.PipeWriter
	stopOnce sync.Once
	finished chan struct{}
	res      transientResult // set before finished closes
	cancel   context.CancelFunc
}

// requestStop asks the guest to stop the observer and report. It blocks until the line is taken or the session is
// gone.
func (iv *transientInterval) requestStop() {
	iv.stopOnce.Do(func() {
		_, _ = io.WriteString(iv.stdin, TransientStopLine)
		_ = iv.stdin.Close()
	})
}

// abort ends the watch without a stop request: the guest sees the end of its stdin and writes nothing into the live
// root. It does nothing once a stop was requested.
func (iv *transientInterval) abort() {
	iv.stopOnce.Do(func() { _ = iv.stdin.Close() })
}

// TransientCollector supplies EFF-TRANSIENT for an adversarial interval from the guest.
//
// BeginInterval starts `transient-watch` over SSH in parallel with the request and returns only after the guest
// acknowledged that the observer runs (within ReadyTimeout; otherwise the evidence is absent), so the request is
// never sent before observation began. The host, not a timer, ends the watch: Transient writes the stop line to the
// session, the guest stops the observer at that moment and returns the report once, and EndInterval (the interval is
// over without a report) closes the session's stdin without the line, on which the guest aborts and writes nothing
// into the live root. Duration is only the cap on a watch nobody ended (the PROC monitor's bound). So the instrument
// does not write into the live root after the session that owns it is gone.
//
// What the report becomes. The live root changes only through publication, and publication's own writes are renames
// (moved_to and moved_from), so every event the observer saw in it must be a rename the publication ledger names, in
// the direction of the ledger operation, and every ledger change must have been seen. Transient fetches the ledger
// from the same store the receipt binds (e04-points) and decides:
//
//   - an event the ledger does not explain, of any kind on any path (a create, modify, close_write, attrib or delete
//     on a path the ledger names is a write publication does not make; a rename of a path it does not name is not
//     publication's): observed, even when the report is otherwise unqualified;
//   - a report that is not a complete qualified window by its own records (effectoracle Report.QualifiedWindow: start
//     and stop mark, the four canaries observed, no gap, a verdict the records give; the host recomputes it and does
//     not trust the stated one), a window that does not cover the request (the guest did not stop on the host's
//     request, or it observed for less time than the request lasted), or a ledger that cannot be had: lost;
//   - a ledger change with no event it explains (the instrument missed a write that happened): lost;
//   - otherwise none.
//
// The reference digest folds the ledger's prefix digest into the report digest, so the decision is bound to both
// inputs. Limits: the observer sees the live root only (the workspace the cell writes is not watched), counts of
// events are lower bounds, and the instrument writes and removes its liveness canary in the live root at start and
// stop (effectoracle doc.go).
type TransientCollector struct {
	AbsentCollectors
	Client EvidenceClient
	// Duration is the cap on how long the guest keeps a watch nobody ended (1 second to MaxTransientWatchSeconds).
	Duration time.Duration
	// ReadyTimeout defaults to DefaultTransientReadyTimeout; StopTimeout to DefaultTransientStopTimeout.
	ReadyTimeout time.Duration
	StopTimeout  time.Duration
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

func (c *TransientCollector) stopTimeout() time.Duration {
	if c.StopTimeout > 0 {
		return c.StopTimeout
	}
	return DefaultTransientStopTimeout
}

// Validate checks the configuration and the evidence channel.
func (c *TransientCollector) Validate() error {
	if c.Duration < time.Second || c.Duration > MaxTransientWatchSeconds*time.Second {
		return errors.New("runner: the transient watch Duration must be between one second and MaxTransientWatchSeconds")
	}
	if err := c.Client.Validate(); err != nil {
		return err
	}
	if !c.Client.CanStop() {
		return errors.New("runner: the evidence transport cannot report the transient observer's acknowledgement and end it from the host (StdinTransport)")
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
	stdinR, stdinW := io.Pipe()
	iv := &transientInterval{seconds: seconds, stdin: stdinW, finished: make(chan struct{})}
	wctx, cancel := context.WithCancel(ctx)
	iv.cancel = cancel
	args := []string{EvidenceTransientWatch, "--interval-ref", ref.Interval, "--duration", strconv.Itoa(seconds)}
	readyCh := make(chan []byte, 1)
	go func() {
		// A write to the pipe must not block once the session is over.
		defer stdinR.Close()
		raw, err := c.Client.CallStdin(wctx, stdinR, func(line []byte) {
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
		iv.res = transientResult{doc: doc, err: err}
		close(iv.finished)
	}()
	fail := func(format string, args ...any) error {
		iv.abort()
		cancel()
		return evidenceAbsent(format, args...)
	}
	timer := time.NewTimer(c.readyTimeout())
	defer timer.Stop()
	var line []byte
	select {
	case line = <-readyCh:
	case <-iv.finished:
		// The watch ended; it still may have acknowledged first.
		select {
		case line = <-readyCh:
		default:
			iv.abort()
			cancel()
			if iv.res.err != nil {
				return iv.res.err
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
	iv.acked = c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil {
		c.active = map[string]*transientInterval{}
	}
	c.active[ref.Interval] = iv
	return nil
}

// EndInterval implements IntervalCollectors. A watch that Transient did not end is aborted: the guest sees the end
// of its stdin and writes no stop canary, and the local ssh is stopped.
func (c *TransientCollector) EndInterval(ref EvidenceRef) {
	c.mu.Lock()
	iv := c.active[ref.Interval]
	delete(c.active, ref.Interval)
	c.mu.Unlock()
	if iv != nil {
		iv.abort()
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
	// The request is over: end the watch now. The end of the request is what the caller passes (GovernedC: when the
	// client returned) or this instant.
	ended := ref.Ended
	if ended.IsZero() {
		ended = c.now()
	}
	go iv.requestStop()
	timer := time.NewTimer(c.stopTimeout())
	defer timer.Stop()
	select {
	case <-iv.finished:
	case <-ctx.Done():
		return ObserverEvidence{}, evidenceAbsent("waiting for the transient watch: %v", ctx.Err())
	case <-timer.C:
		iv.cancel()
		return ObserverEvidence{}, evidenceAbsent("the transient watch did not report within %s of the stop request", c.stopTimeout())
	}
	if iv.res.err != nil {
		return ObserverEvidence{}, iv.res.err
	}
	doc := iv.res.doc
	rep := doc.Report
	digest, err := rep.Digest()
	if err != nil {
		return ObserverEvidence{}, evidenceAbsent("the transient report has no digest: %v", err)
	}
	ledger, known := c.publicationLedger(ctx, ref)
	finding := judgeTransient(&rep, ledger, known)
	switch {
	case rep.OracleVersion != effectoracle.OracleVersion:
		// Another rule set: nothing in the report is read.
		finding = FindingLost
	case finding != FindingObserved && !windowCovers(doc, ended.Sub(iv.acked)):
		// The window does not cover the request. An event it did record stays an observed effect.
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

// windowCovers is true when the guest's window is known to span the request: it stopped because the host asked
// (after the request ended, so its end is past the request's), and it observed for at least as long as the request
// took from the acknowledgement (the guest started before it acknowledged, so an honest window is never shorter).
func windowCovers(doc TransientDoc, request time.Duration) bool {
	return doc.Stop == TransientStopRequested && time.Duration(doc.ElapsedMicros)*time.Microsecond >= request
}

// publicationLedger is the live-root ledger the service recorded for the interval, from the store the receipt
// binds. It is known when the post-publication record says what was applied: the entries (E04LedgerPublication)
// or nothing (E04LedgerNone). Any other basis, a missing record or a store that does not match the receipt leaves
// it unknown.
func (c *TransientCollector) publicationLedger(ctx context.Context, ref EvidenceRef) ([]wsoracle.LedgerEntry, bool) {
	doc, err := c.Client.e04Doc(ctx, ref)
	if err != nil {
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

// ledgerRenames is what publication's own writes to the live root look like to the observer. Publication applies
// every object by rename (internal/publication, checked against a real publish by TestPublishRenamesAreTheOnlyLiveRootEvents):
// an object that exists afterwards is a moved_to on its path, and replacing an existing one also moves the old one away
// (moved_from on the same path); an object removed is a moved_from. required are the events a ledger change must have
// produced, allowed everything publication may produce on a path. No other kind of event is publication's.
func ledgerRenames(ledger []wsoracle.LedgerEntry) (required, allowed map[string]map[string]bool) {
	required, allowed = map[string]map[string]bool{}, map[string]map[string]bool{}
	add := func(m map[string]map[string]bool, path, kind string) {
		if m[path] == nil {
			m[path] = map[string]bool{}
		}
		m[path][kind] = true
	}
	for _, e := range ledger {
		for _, ch := range e.Changes {
			switch {
			case ch.After == nil:
				add(required, ch.Path, effectoracle.KindMovedFrom)
				add(allowed, ch.Path, effectoracle.KindMovedFrom)
			default:
				add(required, ch.Path, effectoracle.KindMovedTo)
				add(allowed, ch.Path, effectoracle.KindMovedTo)
				if ch.Before != nil {
					add(allowed, ch.Path, effectoracle.KindMovedFrom)
				}
			}
		}
	}
	return required, allowed
}

// judgeTransient decides the EFF-TRANSIENT finding of an effectoracle report against the publication ledger of the
// interval (see TransientCollector for the rules). It does not read the window timing (windowCovers).
func judgeTransient(rep *effectoracle.Report, ledger []wsoracle.LedgerEntry, known bool) Finding {
	required, allowed := ledgerRenames(ledger)
	seen := map[string]map[string]bool{}
	// An event the ledger does not explain is an observed effect whatever else the report lacks: the kernel
	// recorded it. Without the ledger nothing is explained, and an event is not read either way.
	if known {
		for _, ev := range rep.Events {
			if !allowed[ev.Path][ev.Kind] {
				return FindingObserved
			}
			if seen[ev.Path] == nil {
				seen[ev.Path] = map[string]bool{}
			}
			seen[ev.Path][ev.Kind] = true
		}
	}
	if !known || !rep.QualifiedWindow() {
		return FindingLost
	}
	for path, kinds := range required {
		for kind := range kinds {
			if !seen[path][kind] {
				return FindingLost
			}
		}
	}
	return FindingNone
}
