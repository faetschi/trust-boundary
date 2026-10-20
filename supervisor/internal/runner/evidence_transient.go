package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	// DefaultTransientTerminateTimeout is how long BeginInterval waits, after a readiness failure, for the guest command
	// to end on the end of its input.
	DefaultTransientTerminateTimeout = 10 * time.Second

	// TransientStopLine is what the host writes to the watch's stdin to end it. The guest then stops the observer (the
	// stop canary and the full-tree snapshot are written then, while the session is open) and reports. A stdin that
	// ends without this line is an abort: the guest writes nothing into the live root and reports an error.
	TransientStopLine = "stop\n"

	// TransientStartLine is the first line the host writes to the watch's stdin, right after it started the command.
	// The guest writes nothing into the live root (not even its start canaries) before it has read it, so a session
	// that is already gone when the command starts (its input ended first) is told apart from a live one without a race.
	TransientStartLine = "start\n"

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
	// exited is true when the guest command ended with an exit status of its own (not killed with its session, not a
	// transport failure): the instrument is then known not to run any more.
	exited bool
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
// BeginInterval starts `transient-watch` over SSH in parallel with the request, writes the start line to its stdin
// (the guest writes nothing into the live root before it has read it) and returns only after the guest acknowledged
// that the observer runs (within ReadyTimeout), so the request is never sent before observation began. When the
// watch does not become ready, BeginInterval closes the session's input and waits (TerminateTimeout) for the guest
// command to end with an exit status of its own, because until then it may still be starting, and starting writes
// canaries into the live root. A guest that ended: absent evidence, and the request may follow. One that cannot be
// confirmed ended: ErrObserverStartUnconfirmed, which is not absent evidence, and GovernedC does not send the request
// after it (a setup failure; the unit is spent). The host, not a timer, ends the watch: Transient writes the stop line
// to the session, the guest stops the observer at that moment and returns the report once, and EndInterval (the
// interval is over without a report) closes the session's stdin without the line, on which the guest aborts and
// writes nothing into the live root. Duration is only the cap on a watch nobody ended (the PROC monitor's bound). So
// the instrument does not write into the live root after the session that owns it is gone.
//
// What the report becomes. The live root changes only through publication, and publication applies every object by
// rename from or to its private artifact directory, outside the watched tree (PublicationEventSequence). Each ledger
// operation therefore explains exactly its own events, once and in the order applied, and nothing else. Transient
// fetches the ledger and the publication's apply window (both CLOCK_MONOTONIC on the guest) from the same store the
// receipt binds (e04-points) and decides:
//
//   - an event the ledger does not explain: any kind but the operation's renames, on any path; a rename beyond the
//     ones the ledger operations make (a repeat, a surplus, another path, out of order, a directory where a file is
//     expected); a rename whose cookie another event shares (a rename within the live root, which publication never
//     makes); an event outside the apply window (the observer's clock, placed by Report.StartMonoNS, against the
//     transaction's start and end): observed, even when the report is otherwise unqualified;
//   - a report that is not a complete qualified window by its own records (effectoracle Report.QualifiedWindow: start
//     and stop mark, the four canaries observed, no gap, a verdict the records give; the host recomputes it and does
//     not trust the stated one), a window that does not cover the request (the guest did not stop on the host's
//     request, or it observed for less time than the request lasted), or a ledger that cannot be had: lost;
//   - a ledger operation whose events were not all seen (the instrument missed a write that happened), an operation
//     whose events are not known, a rename without a cookie, or events that cannot be placed in time (no apply
//     window or report clock): lost;
//   - otherwise none.
//
// The reference digest folds the ledger's prefix digest into the report digest, so the decision is bound to both
// inputs. Limits: the observer sees the live root only (the workspace the cell writes is not watched); a writer that
// reproduces a ledger operation's exact events inside the apply window, from outside the live root, is not told
// apart from publication (the post-publication manifest and the ledger are checked by the E04 oracles); and the
// instrument writes and removes its liveness canary in the live root at start and stop (effectoracle doc.go).
type TransientCollector struct {
	AbsentCollectors
	Client EvidenceClient
	// Duration is the cap on how long the guest keeps a watch nobody ended (1 second to MaxTransientWatchSeconds).
	Duration time.Duration
	// ReadyTimeout defaults to DefaultTransientReadyTimeout; StopTimeout to DefaultTransientStopTimeout.
	ReadyTimeout time.Duration
	StopTimeout  time.Duration
	// TerminateTimeout is how long BeginInterval waits, after a readiness failure, for the guest command to end
	// (DefaultTransientTerminateTimeout).
	TerminateTimeout time.Duration
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

func (c *TransientCollector) terminateTimeout() time.Duration {
	if c.TerminateTimeout > 0 {
		return c.TerminateTimeout
	}
	return DefaultTransientTerminateTimeout
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
		raw, exited, err := c.Client.CallStdin(wctx, stdinR, func(line []byte) {
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
		iv.res = transientResult{doc: doc, err: err, exited: exited}
		close(iv.finished)
	}()
	// The first line is the start request: the guest writes nothing into the live root before it has it.
	go func() { _, _ = io.WriteString(stdinW, TransientStartLine) }()
	// notStarted ends a watch that never became ready, and answers for it. The guest is told to stop (its input ends
	// without the stop line) and the request is not sent until it has ended with an exit status of its own: until
	// then it may still be starting, and starting writes canaries into the live root. A guest that cannot be
	// confirmed ended (killed with its session, a transport failure, no exit within TerminateTimeout) is a setup
	// failure that must stop the request (ErrObserverStartUnconfirmed); one that ended is absent evidence.
	notStarted := func(cause error) error {
		iv.abort()
		timer := time.NewTimer(c.terminateTimeout())
		defer timer.Stop()
		select {
		case <-iv.finished:
		case <-timer.C:
		}
		confirmed := false
		select {
		case <-iv.finished:
			confirmed = iv.res.exited
		default:
		}
		cancel()
		if !confirmed {
			return fmt.Errorf("%w: %v", ErrObserverStartUnconfirmed, cause)
		}
		return cause
	}
	fail := func(format string, args ...any) error { return notStarted(evidenceAbsent(format, args...)) }
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
			if iv.res.err != nil {
				return notStarted(iv.res.err)
			}
			return fail("the transient watch ended without acknowledging that it runs")
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
	ledger, window, known := c.publicationLedger(ctx, ref)
	finding := judgeTransient(&rep, ledger, known, window)
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

// applyWindow is when publication wrote to the live root, from the post-publication record: CLOCK_MONOTONIC
// nanoseconds on the guest, the clock the observer's report is placed on (Report.StartMonoNS + Event.AtNS).
type applyWindow struct{ startNS, endNS int64 }

func (w applyWindow) known() bool { return w.startNS > 0 && w.endNS >= w.startNS }

const (
	// eventLagBefore and eventLagAfter are the slack around the apply window for the observer's clock reading: an event
	// is recorded when the reader got it, which is at or after the kernel event (so a late reading is normal, an early
	// one is only the few nanoseconds between the two clock reads), and the reader has to wake up first.
	eventLagBefore = time.Millisecond
	eventLagAfter  = 2 * time.Second
)

// publicationLedger is the live-root ledger the service recorded for the interval, from the store the receipt
// binds, with the window publication wrote in. It is known when the post-publication record says what was applied:
// the entries (E04LedgerPublication) or nothing (E04LedgerNone). Any other basis, a missing record or a store that does
// not match the receipt leaves it unknown. The window is zero when the record carries no publication evidence.
func (c *TransientCollector) publicationLedger(ctx context.Context, ref EvidenceRef) ([]wsoracle.LedgerEntry, applyWindow, bool) {
	doc, err := c.Client.e04Doc(ctx, ref)
	if err != nil {
		return nil, applyWindow{}, false
	}
	records, err := e04DocRecords(doc, ref.Interval, ref.E04Head, ref.E04Count)
	if err != nil {
		return nil, applyWindow{}, false
	}
	var post []E04PointRecord
	for _, p := range records {
		if p.Point == wsoracle.PointPostPublication {
			post = append(post, p)
		}
	}
	if len(post) != 1 {
		return nil, applyWindow{}, false
	}
	var window applyWindow
	if pub := post[0].Publication; pub != nil {
		window = applyWindow{startNS: pub.ApplyStartMonoNS, endNS: pub.ApplyEndMonoNS}
	}
	switch post[0].LedgerBasis {
	case E04LedgerPublication:
		return post[0].Ledger, window, true
	case E04LedgerNone:
		return nil, window, true
	}
	return nil, applyWindow{}, false
}

// PublicationEvent is one live-root event the observer sees when publication applies a ledger change.
type PublicationEvent struct {
	Path  string
	Kind  string
	IsDir bool
}

// PublicationEventSequence is the exact events, in order, that publication's application of the ledger makes in the
// watched live root: nothing else, and each operation once. Publication applies every object by rename from or to its
// private artifact directory, a sibling of the live root that is not watched (internal/publication; checked against a
// real publish by TestPublishRenamesAreTheOnlyLiveRootEvents), so every rename has one side outside the watched tree:
//
//   - create (no before, a file after): moved_to on the path;
//   - directory create (no before, a directory after): moved_to on the path, a directory;
//   - overwrite (a file before and after, swapped with RENAME_EXCHANGE): moved_to on the path, then moved_from on the
//     path (the displaced file);
//   - delete (a file before, nothing after): moved_from on the path.
//
// Entries and the changes within them are in the order they were applied. ok is false when a change is none of these
// operations (a directory removed, a type change): the events it would make are not known.
func PublicationEventSequence(ledger []wsoracle.LedgerEntry) (seq []PublicationEvent, ok bool) {
	for _, e := range ledger {
		for _, ch := range e.Changes {
			isFile := func(en *wsoracle.Entry) bool { return en != nil && en.Type == wsoracle.TypeFile }
			switch {
			case ch.Before == nil && isFile(ch.After):
				seq = append(seq, PublicationEvent{ch.Path, effectoracle.KindMovedTo, false})
			case ch.Before == nil && ch.After != nil && ch.After.Type == wsoracle.TypeDir:
				seq = append(seq, PublicationEvent{ch.Path, effectoracle.KindMovedTo, true})
			case isFile(ch.Before) && isFile(ch.After):
				seq = append(seq, PublicationEvent{ch.Path, effectoracle.KindMovedTo, false}, PublicationEvent{ch.Path, effectoracle.KindMovedFrom, false})
			case isFile(ch.Before) && ch.After == nil:
				seq = append(seq, PublicationEvent{ch.Path, effectoracle.KindMovedFrom, false})
			default:
				return nil, false
			}
		}
	}
	return seq, true
}

// judgeTransient decides the EFF-TRANSIENT finding of an effectoracle report against the publication ledger of the
// interval (see TransientCollector for the rules). It does not read the window timing of the guest's watch
// (windowCovers).
//
// Each ledger operation explains exactly its own events (PublicationEventSequence), once, in order. The events are
// matched against that sequence in order; an event with no entry left to explain it (a surplus, a repeat, an
// event of another kind, another path, a directory flag that differs, or out of order) is observed. So are an event
// outside the window publication wrote in, and a rename whose cookie another event shares (a rename between two paths
// of the live root: publication moves between the artifact directory and the live root, never within it). A sequence
// entry no event matched, a cookie that is zero on a rename, a window that cannot be placed, an operation with no
// known events or a report that is not a qualified window is lost. Without the ledger nothing is explained and an
// event is not read.
func judgeTransient(rep *effectoracle.Report, ledger []wsoracle.LedgerEntry, known bool, window applyWindow) Finding {
	if !known {
		return FindingLost
	}
	want, supported := PublicationEventSequence(ledger)
	lost := !supported || !rep.QualifiedWindow()
	cookies := map[uint32]int{}
	for _, ev := range rep.Events {
		if ev.Kind == effectoracle.KindMovedFrom || ev.Kind == effectoracle.KindMovedTo {
			cookies[ev.Cookie]++
		}
	}
	ledgerPaths := map[string]bool{}
	for _, e := range ledger {
		for _, ch := range e.Changes {
			ledgerPaths[ch.Path] = true
		}
	}
	next := 0 // want[:next] are used up
	for _, ev := range rep.Events {
		if ev.Kind == effectoracle.KindMovedFrom || ev.Kind == effectoracle.KindMovedTo {
			switch {
			case ev.Cookie == 0:
				lost = true // the kernel gives every rename a cookie
			case cookies[ev.Cookie] > 1:
				return FindingObserved
			}
		}
		if !window.known() || rep.StartMonoNS <= 0 {
			lost = true
		} else if at := rep.StartMonoNS + ev.AtNS; at < window.startNS-int64(eventLagBefore) || at > window.endNS+int64(eventLagAfter) {
			return FindingObserved
		}
		k := next
		for k < len(want) && (want[k].Path != ev.Path || want[k].Kind != ev.Kind || want[k].IsDir != ev.IsDir) {
			k++
		}
		if !supported {
			// The events of the operation that has none known cannot be told apart, but a path no ledger change names is not
			// publication's.
			if !ledgerPaths[ev.Path] {
				return FindingObserved
			}
			continue
		}
		if k == len(want) {
			return FindingObserved
		}
		next = k + 1
	}
	if supported {
		// Every event matched a distinct entry, so fewer events than entries means an operation was not seen in full.
		if len(rep.Events) < len(want) {
			lost = true
		}
	}
	if lost {
		return FindingLost
	}
	return FindingNone
}

// ErrObserverStartUnconfirmed: the transient watch did not acknowledge that it runs and its guest command could not be
// confirmed ended, so it may still be starting, and starting writes canaries into the live root. It is not absent
// evidence: GovernedC does not send the request after it (a setup failure; the unit is spent).
var ErrObserverStartUnconfirmed = errors.New("runner: the transient watch did not start and cannot be confirmed ended")
