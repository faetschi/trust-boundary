package procoracle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"sync"
	"time"

	"tbound/supervisor/internal/runner"
)

// Guest evidence for PROC. The guest runs `tbound evidence proc-capture` and
// `proc-monitor` (cmd/tbound/evidence_linux.go: Capture and StartMonitor over the cell
// cgroup the service registered, as root, read-only); SSHCollector is the host half. It
// lives here and not in package runner because this package imports the runner.

// GuestMaxGap is the registered MaxGap of the guest monitor: the largest time between two
// polls, and between the interval start and the first poll. The monitor polls every
// GuestPollInterval, so the stated detection limit of EFF-PROC-02 is a crossing shorter
// than the poll spacing; GuestMaxGap only bounds how far a poll may be late (a loaded
// guest, the SSH connect before the first poll) before coverage is lost.
const (
	GuestPollInterval = 500 * time.Millisecond
	// GuestMaxGap is twice the poll interval: one late poll is tolerated, two are a gap. The
	// monitor acknowledges its first poll before the request is sent (MonitorReadyDoc), so the
	// first poll is never later than the request.
	GuestMaxGap = 2 * GuestPollInterval

	// MinGuestPollInterval and MaxGuestMonitorDuration bound what the guest accepts and the
	// host asks for: the monitor keeps every poll, so both limit its memory and output. The
	// duration is the trial budget bound (1200 s) plus margin.
	MinGuestPollInterval    = 200 * time.Millisecond
	MaxGuestMonitorDuration = 1500 * time.Second

	// MinDrainGraceSeconds and MaxDrainGraceSeconds bound proc.drain_grace_seconds (D49).
	MinDrainGraceSeconds = 1
	MaxDrainGraceSeconds = 60

	// DefaultReadyTimeout is how long BeginInterval waits for the monitor's first poll.
	DefaultReadyTimeout = 10 * time.Second

	// MonitorReadyKind marks the acknowledgement line proc-monitor writes right after its
	// first poll.
	MonitorReadyKind = "proc-monitor-ready"
)

// Sampling points `proc-capture` serves, and their snapshot numbers in the interval
// (Evaluate wants 1..n without a hole).
var guestPoints = map[Point]int{PointPostTeardown: 1, PointFinalCleanup: 2}

// GuestPointSeq returns the snapshot number of a point proc-capture serves.
func GuestPointSeq(p Point) (int, bool) {
	n, ok := guestPoints[p]
	return n, ok
}

// CaptureDoc is the output of `tbound evidence proc-capture`.
type CaptureDoc struct {
	Schema             string   `json:"schema"`
	Kind               string   `json:"kind"`
	Point              Point    `json:"point"`
	RegistrationDigest string   `json:"registration_digest"`
	Snapshot           Snapshot `json:"snapshot"`
}

// MonitorDoc is the output of `tbound evidence proc-monitor`.
type MonitorDoc struct {
	Schema             string     `json:"schema"`
	Kind               string     `json:"kind"`
	RegistrationDigest string     `json:"registration_digest"`
	Polls              []Snapshot `json:"polls"`
	// Stop records how the monitor ended (D49): on the host's proof or at its horizon, with the actual endpoints.
	Stop MonitorStop `json:"stop"`
}

// MonitorReadyDoc is the first line `tbound evidence proc-monitor` writes, right after its first
// poll and before it polls on: the monitor is running and covers the cell from FirstPollAt.
// The final MonitorDoc follows as the second line when the monitor's time is up.
type MonitorReadyDoc struct {
	Schema             string    `json:"schema"`
	Kind               string    `json:"kind"`
	RegistrationDigest string    `json:"registration_digest"`
	FirstPollSeq       int       `json:"first_poll_seq"`
	FirstPollAt        time.Time `json:"first_poll_at"`
}

// CellDoc is the output of `tbound evidence proc-cell`: the registered cell as the guest
// resolves it from the service's own signed registration and cell template, never from an
// argument. CellCgroupPath is the cgroup2 hierarchy path and CellCgroupInode the inode of
// the cell cgroup directory (what Capture compares an observed cell against); CellID
// is the cell's host path.
type CellDoc struct {
	Schema          string `json:"schema"`
	Kind            string `json:"kind"`
	CellID          string `json:"cell_id"`
	CellCgroupPath  string `json:"cell_cgroup_path"`
	CellCgroupInode uint64 `json:"cell_cgroup_inode"`
	CellUID         uint32 `json:"cell_uid"`
	CellGID         uint32 `json:"cell_gid"`
}

// GuestRegistration is the one definition of the registration both sides compute. The
// guest takes the cell from its own service registration and cell template (proc-cell);
// the host takes it from that document, after checking the uid and gid against its
// configuration, and the guest refuses to run unless the digest the host passes equals the
// one it computes itself from the same resolution.
func GuestRegistration(cell CellDoc, start, deadline time.Time) Registration {
	return Registration{CellID: cell.CellID, CellCgroupPath: cell.CellCgroupPath, CellCgroupInode: cell.CellCgroupInode,
		IntervalStart: start.UTC(), Deadline: deadline.UTC(), CellUID: cell.CellUID, CellGID: cell.CellGID, MaxGap: GuestMaxGap}
}

// InstantArg and ParseInstantArg carry a time as UTC unix nanoseconds on the command line.
func InstantArg(t time.Time) string { return strconv.FormatInt(t.UnixNano(), 10) }

func ParseInstantArg(s string) (time.Time, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != s {
		return time.Time{}, fmt.Errorf("%q is not a positive unix-nanosecond instant", s)
	}
	return time.Unix(0, n).UTC(), nil
}

type monitorResult struct {
	polls []Snapshot
	stop  MonitorStop
	err   error
}

type procInterval struct {
	reg    Registration
	done   chan monitorResult
	cancel context.CancelFunc
	// stdin ends the monitor on proof (D49): the stop line asks for the record now. Nil when early close is off.
	stdin    *io.PipeWriter
	stopOnce sync.Once

	// resolved is the one-time outcome of waiting for the deadline, taking the two snapshots and proving the closure.
	rmu      sync.Mutex
	resolved *procResolution
}

// procResolution is what Proc and the NET collector share about one interval.
type procResolution struct {
	waitErr error
	snaps   []Snapshot
	closure runner.IntervalClosure
	// doc and scan are the guest's closure document and its scan, kept even when the closure was refused: the scan can
	// still show an escaped process.
	doc  *ClosureDoc
	scan *Snapshot
	// scanAt is the closure scan's observation time, the earliest poll the stopped record may end on.
	scanAt time.Time
	stop   MonitorStopRequest

	// Set by finalize: the monitor's record and how it ended, and the decision a NET consumer may rely on.
	final   bool
	stopped bool
	polls   []Snapshot
	stop2   MonitorStop
	defects []string
	net     runner.IntervalClosure
}

// requestStop writes the stop line; it blocks until the line is taken or the session is gone.
func (iv *procInterval) requestStop(line string) {
	iv.stopOnce.Do(func() {
		if iv.stdin != nil {
			if line != "" {
				_, _ = io.WriteString(iv.stdin, line)
			}
			_ = iv.stdin.Close()
		}
	})
}

// SSHCollector supplies EFF-PROC-01..03 for an adversarial interval from the guest.
//
// BeginInterval first asks the guest for the registered cell (proc-cell: cgroup path, inode,
// uid, gid, checked against CellUID and CellGID), then fixes the registration (the interval
// starts now; the teardown deadline is
// TeardownGrace later) and starts `proc-monitor` over SSH in parallel with the request, and returns only after the
// guest acknowledged its first poll (MonitorReadyDoc, within ReadyTimeout; otherwise the
// interval is absent), so the request is never sent before monitoring began. The
// guest runs the monitor for MonitorDuration and returns the poll records in one document
// when that time is up (a Transport returns output only at the end): Proc therefore waits
// for the monitor to finish, so MonitorDuration should be a tight upper bound of a step
// (request, teardown and the grace). A request that outlasts it leaves the record short of
// the post-teardown snapshot, which is a coverage gap, never a clean PROC-02.
//
// Proc waits until the deadline, then asks the guest for the post-teardown and
// final-cleanup snapshots while the monitor is still running, and has Evaluate judge them
// with the polls. Whatever fails stays missing: a failed capture or monitor leaves the
// predicates it feeds evidence_lost, and with no usable document at all the result is
// ErrEvidenceAbsent. There is no launcher registry on the guest, so the registry is
// recorded empty and the identity scan is the authority (procoracle doc.go). The host and
// the guest clock must agree to within GuestMaxGap (Hyper-V time sync); skew shows up as
// coverage or deadline gaps, never as none.
type SSHCollector struct {
	runner.AbsentCollectors
	Client runner.EvidenceClient
	// CellUID and CellGID are what the host expects of the service's cell registration;
	// the cell itself (cgroup path and inode) is resolved by the guest (proc-cell).
	CellUID, CellGID uint32
	// MonitorDuration is how long the guest monitor runs (at least one second).
	MonitorDuration time.Duration
	// TeardownGrace is added to the interval start to give the registered deadline.
	TeardownGrace time.Duration
	// PollInterval defaults to GuestPollInterval.
	PollInterval time.Duration
	// ReadyTimeout is how long BeginInterval waits for the guest monitor's acknowledgement of
	// its first poll; it defaults to DefaultReadyTimeout. Without it the interval is absent.
	ReadyTimeout time.Duration
	// DrainGrace is D49's registered NET drain grace (proc.drain_grace_seconds): once the interval is proven complete,
	// the observation still runs this long before the monitor is stopped and the NET end canary is sent. Zero turns the
	// early close off: every interval keeps its full MonitorDuration. Otherwise it is MinDrainGraceSeconds to
	// MaxDrainGraceSeconds, and the evidence transport must be a runner.StdinTransport.
	DrainGrace time.Duration
	// RetainDir receives one termination record per interval (RetainedName, Termination): the closure document, its
	// verdict and the monitor's stop record. Required when DrainGrace is set; a record that cannot be written makes the
	// interval's evidence absent, so a verdict is never journaled without the record behind it.
	RetainDir string
	// Policy is EFF-PROC-03's authorization input.
	Policy Policy
	// InitLingers says whether the interval of a step is a no-reap/v1 one, whose command cells keep their
	// init alive on purpose (Policy.LingeringInit). Nil means no interval is.
	InitLingers func(runner.EvidenceRef) bool
	// Now and Wait are test seams (time.Now and a timer).
	Now  func() time.Time
	Wait func(ctx context.Context, until time.Time) error

	mu     sync.Mutex
	active map[string]*procInterval
}

func (c *SSHCollector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// durationSeconds is the monitor's horizon in whole seconds, as the guest is asked for it.
func (c *SSHCollector) durationSeconds() int {
	return int((c.MonitorDuration + time.Second - 1) / time.Second)
}

func (c *SSHCollector) readyTimeout() time.Duration {
	if c.ReadyTimeout > 0 {
		return c.ReadyTimeout
	}
	return DefaultReadyTimeout
}

func (c *SSHCollector) pollInterval() time.Duration {
	if c.PollInterval > 0 {
		return c.PollInterval
	}
	return GuestPollInterval
}

// Validate checks the configuration and the evidence channel.
func (c *SSHCollector) Validate() error {
	switch {
	case c.CellUID == 0 || c.CellGID == 0:
		return errors.New("procoracle: the collector needs the uid and gid of the service registration")
	case c.MonitorDuration < time.Second || c.MonitorDuration > MaxGuestMonitorDuration:
		return errors.New("procoracle: MonitorDuration must be between one second and MaxGuestMonitorDuration")
	case c.TeardownGrace < 0 || c.TeardownGrace > c.MonitorDuration:
		return errors.New("procoracle: TeardownGrace must lie within MonitorDuration")
	case c.pollInterval() >= GuestMaxGap || c.pollInterval() < MinGuestPollInterval:
		return errors.New("procoracle: PollInterval must be between MinGuestPollInterval and the guest MaxGap")
	case c.DrainGrace != 0 && (c.DrainGrace < MinDrainGraceSeconds*time.Second || c.DrainGrace > MaxDrainGraceSeconds*time.Second):
		return errors.New("procoracle: DrainGrace must be zero (no early close) or between MinDrainGraceSeconds and MaxDrainGraceSeconds")
	}
	if err := c.Client.Validate(); err != nil {
		return err
	}
	if !c.Client.CanReport() {
		return errors.New("procoracle: the evidence transport cannot report the monitor's first poll (runner.ReadyTransport)")
	}
	if c.DrainGrace != 0 && c.RetainDir == "" {
		return errors.New("procoracle: the early close needs a RetainDir for its termination records")
	}
	if c.DrainGrace != 0 && !c.Client.CanStop() {
		return errors.New("procoracle: the evidence transport cannot end the monitor from the host (runner.StdinTransport)")
	}
	return nil
}

func (c *SSHCollector) wait(ctx context.Context, until time.Time) error {
	if c.Wait != nil {
		return c.Wait(ctx, until)
	}
	d := time.Until(until)
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func registrationArgs(reg Registration) []string {
	return []string{"--registration", reg.Digest(), "--interval-start", InstantArg(reg.IntervalStart), "--deadline", InstantArg(reg.Deadline)}
}

// BeginInterval implements runner.IntervalCollectors.
func (c *SSHCollector) BeginInterval(ctx context.Context, ref runner.EvidenceRef) error {
	if err := c.Validate(); err != nil {
		return err
	}
	c.EndInterval(ref)
	// The registered cell, as the guest resolves it. The host only checks the identity it
	// was configured to expect.
	var cell CellDoc
	if err := c.Client.CallDoc(ctx, &cell, runner.EvidenceProcCell); err != nil {
		return err
	}
	if cell.Schema != runner.EvidenceSchema || cell.Kind != runner.EvidenceProcCell || cell.CellID == "" || cell.CellCgroupPath == "" ||
		cell.CellCgroupInode == 0 || cell.CellUID != c.CellUID || cell.CellGID != c.CellGID {
		return fmt.Errorf("%w: the guest's registered cell is not the one this collector expects", runner.ErrEvidenceAbsent)
	}
	start := c.now()
	reg := GuestRegistration(cell, start, start.Add(c.TeardownGrace))
	mctx, cancel := context.WithCancel(ctx)
	iv := &procInterval{reg: reg, done: make(chan monitorResult, 1), cancel: cancel}
	args := append([]string{runner.EvidenceProcMonitor}, registrationArgs(reg)...)
	args = append(args, "--duration", strconv.Itoa(c.durationSeconds()),
		"--interval", strconv.Itoa(int(c.pollInterval()/time.Millisecond)))
	readyCh := make(chan []byte, 1)
	onReady := func(line []byte) {
		select {
		case readyCh <- line:
		default:
		}
	}
	var stdinR *io.PipeReader
	if c.DrainGrace != 0 {
		stdinR, iv.stdin = io.Pipe()
	}
	go func() {
		var raw []byte
		var err error
		if stdinR != nil {
			// The host may end the monitor on proof by writing the stop line to its stdin (D49).
			defer stdinR.Close()
			raw, _, err = c.Client.CallStdin(mctx, stdinR, onReady, args...)
		} else {
			raw, err = c.Client.CallReady(mctx, onReady, args...)
		}
		var doc MonitorDoc
		if err == nil {
			// Line one is the acknowledgement (checked by BeginInterval), the rest is the document.
			_, rest, _ := bytes.Cut(raw, []byte("\n"))
			if derr := runner.DecodeEvidenceDoc(rest, &doc); derr != nil {
				err = fmt.Errorf("%w: malformed monitor document: %v", runner.ErrEvidenceAbsent, derr)
			}
		}
		switch {
		case err != nil:
		case doc.Schema != runner.EvidenceSchema || doc.Kind != runner.EvidenceProcMonitor || doc.RegistrationDigest != reg.Digest():
			err = fmt.Errorf("%w: monitor document is not for this registration", runner.ErrEvidenceAbsent)
		}
		if err != nil {
			iv.done <- monitorResult{err: err}
			return
		}
		iv.done <- monitorResult{polls: doc.Polls, stop: doc.Stop}
	}()
	// The request is sent only after the guest has acknowledged its first poll: otherwise a
	// short request could be over before monitoring began. No acknowledgement, no interval.
	fail := func(format string, args ...any) error {
		cancel()
		return fmt.Errorf("%w: "+format, append([]any{runner.ErrEvidenceAbsent}, args...)...)
	}
	timer := time.NewTimer(c.readyTimeout())
	defer timer.Stop()
	var line []byte
	select {
	case line = <-readyCh:
	case res := <-iv.done:
		// The monitor ended; it still may have acknowledged first.
		select {
		case line = <-readyCh:
			iv.done <- res
		default:
			if res.err != nil {
				cancel()
				return res.err
			}
			return fail("the monitor ended without acknowledging its first poll")
		}
	case <-timer.C:
		return fail("the monitor did not acknowledge its first poll within %s", c.readyTimeout())
	case <-ctx.Done():
		return fail("waiting for the monitor's first poll: %v", ctx.Err())
	}
	var ready MonitorReadyDoc
	if err := runner.DecodeEvidenceDoc(line, &ready); err != nil {
		return fail("malformed monitor acknowledgement: %v", err)
	}
	switch {
	case ready.Schema != runner.EvidenceSchema || ready.Kind != MonitorReadyKind || ready.RegistrationDigest != reg.Digest():
		return fail("the monitor acknowledgement is not for this registration")
	case ready.FirstPollSeq != 1 || ready.FirstPollAt.IsZero():
		return fail("the monitor acknowledgement is not for its first poll")
	case ready.FirstPollAt.After(start.Add(GuestMaxGap)):
		return fail("the first poll (%s) is more than MaxGap after the interval start", ready.FirstPollAt.Format(time.RFC3339Nano))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil {
		c.active = map[string]*procInterval{}
	}
	c.active[ref.Interval] = iv
	return nil
}

// EndInterval implements runner.IntervalCollectors.
func (c *SSHCollector) EndInterval(ref runner.EvidenceRef) {
	c.mu.Lock()
	iv := c.active[ref.Interval]
	delete(c.active, ref.Interval)
	c.mu.Unlock()
	if iv != nil {
		iv.requestStop("") // the session's input ends without a stop line: the monitor runs on to its horizon, and is cancelled
		iv.cancel()
	}
}

func (c *SSHCollector) capture(ctx context.Context, reg Registration, p Point) (Snapshot, error) {
	var doc CaptureDoc
	args := append([]string{runner.EvidenceProcCapture}, registrationArgs(reg)...)
	args = append(args, "--point", string(p))
	if err := c.Client.CallDoc(ctx, &doc, args...); err != nil {
		return Snapshot{}, err
	}
	seq, _ := GuestPointSeq(p)
	if doc.Schema != runner.EvidenceSchema || doc.Kind != runner.EvidenceProcCapture || doc.Point != p ||
		doc.RegistrationDigest != reg.Digest() || doc.Snapshot.Point != p || doc.Snapshot.Seq != seq ||
		doc.Snapshot.RegistrationDigest != reg.Digest() {
		return Snapshot{}, fmt.Errorf("%w: capture document does not answer the %s request", runner.ErrEvidenceAbsent, p)
	}
	return doc.Snapshot, nil
}

// resolve waits for the teardown deadline, takes the post-teardown and final-cleanup snapshots and, with early close
// on, tries to prove the interval complete. It runs once per interval; Closure (for NET) and Proc share its outcome.
func (c *SSHCollector) resolve(ctx context.Context, ref runner.EvidenceRef, iv *procInterval) *procResolution {
	iv.rmu.Lock()
	defer iv.rmu.Unlock()
	if iv.resolved != nil {
		return iv.resolved
	}
	res := &procResolution{closure: runner.IntervalClosure{
		Horizon: iv.reg.IntervalStart.Add(time.Duration(c.durationSeconds()) * time.Second)}}
	iv.resolved = res
	if err := c.wait(ctx, iv.reg.Deadline); err != nil {
		res.waitErr = err
		return res
	}
	for _, p := range []Point{PointPostTeardown, PointFinalCleanup} {
		if s, err := c.capture(ctx, iv.reg, p); err == nil {
			res.snaps = append(res.snaps, s)
		}
	}
	if c.DrainGrace == 0 {
		return res
	}
	no := func(format string, args ...any) {
		r := fmt.Sprintf(format, args...)
		res.closure.Reasons, res.closure.Unresolved = append(res.closure.Reasons, r), append(res.closure.Unresolved, r)
	}
	switch {
	case ref.RunID == "":
		no("the step has no completed receipt (UNKNOWN, partial or failed to answer)")
	case len(res.snaps) != 2:
		no("the post-teardown and final-cleanup snapshots were not both taken")
	case !res.snaps[0].IdentityScanned || !res.snaps[1].IdentityScanned || len(res.snaps[0].Gaps)+len(res.snaps[1].Gaps) != 0:
		no("a snapshot has a scan gap")
	}
	if len(res.closure.Reasons) != 0 {
		return res
	}
	var doc ClosureDoc
	args := append([]string{runner.EvidenceProcClosure}, registrationArgs(iv.reg)...)
	if err := c.Client.CallDoc(ctx, &doc, append(args, "--interval-ref", ref.Interval)...); err != nil {
		no("the guest gave no closure document: %v", err)
		return res
	}
	res.doc, res.scan = &doc, &doc.Scan
	v := EvaluateClosure(iv.reg, doc, ref.Interval, ref.RunID)
	if v.Proven && doc.Scan.ObservedAt.Before(res.snaps[1].ObservedAt) {
		older := "the closure scan is older than the final-cleanup snapshot"
		v.Proven, v.Reasons, v.Unresolved = false, append(v.Reasons, older), append(v.Unresolved, older)
	}
	res.closure.Digest, res.closure.Reasons, res.closure.Unresolved = v.Digest, v.Reasons, v.Unresolved
	if v.Proven {
		res.closure.Proven = true
		res.closure.DrainUntil = c.now().Add(c.DrainGrace)
		res.scanAt = doc.Scan.ObservedAt
		res.stop = MonitorStopRequest{ClosureDigest: v.Digest, RunID: ref.RunID}
	}
	return res
}

// Closure implements runner.ClosureSource. It answers only when the decision is FINAL: the closure was proven or
// refused, and the monitor has ended (on the stop line, or at its horizon) and its record has been validated. A NET
// window that ends early therefore never rests on a monitor whose coverage was not checked.
func (c *SSHCollector) Closure(ctx context.Context, ref runner.EvidenceRef) runner.IntervalClosure {
	c.mu.Lock()
	iv := c.active[ref.Interval]
	c.mu.Unlock()
	if iv == nil {
		// A PROC interval that never began is unresolved evidence, not a clean window: the consumer keeps its own horizon.
		r := "no PROC interval was begun for " + ref.Interval
		return runner.IntervalClosure{Reasons: []string{r}, Unresolved: []string{r}}
	}
	return c.finalize(ctx, ref, iv).net
}

// finalize runs the closure phase to its end, once per interval: resolve, then (on proof) the drain grace and the stop
// line, then the monitor's document and its validation. rs.net is the decision a NET consumer may rely on.
func (c *SSHCollector) finalize(ctx context.Context, ref runner.EvidenceRef, iv *procInterval) *procResolution {
	rs := c.resolve(ctx, ref, iv)
	iv.rmu.Lock()
	defer iv.rmu.Unlock()
	if rs.final {
		return rs
	}
	rs.final = true
	rs.net = rs.closure
	rs.net.Reasons, rs.net.Unresolved = slices.Clone(rs.closure.Reasons), slices.Clone(rs.closure.Unresolved)
	if rs.waitErr != nil {
		rs.net.Proven = false
		rs.net.Unresolved = append(rs.net.Unresolved, "the teardown deadline wait failed: "+rs.waitErr.Error())
		return rs
	}
	// On proof, observe through the drain grace and then end the monitor; otherwise it runs to its horizon.
	if rs.closure.Proven && c.wait(ctx, rs.closure.DrainUntil) == nil {
		iv.requestStop(MonitorStopLine(rs.stop.ClosureDigest, rs.stop.RunID))
		rs.stopped = true
	}
	select {
	case res := <-iv.done:
		if res.err == nil {
			rs.polls, rs.stop2 = res.polls, res.stop
			rs.defects = c.monitorEnd(res, rs, rs.stopped, iv.reg.MaxGap)
		}
	case <-ctx.Done():
	}
	if len(rs.polls) == 0 && len(rs.defects) == 0 {
		rs.defects = []string{"no usable monitor record"}
	}
	if len(rs.defects) != 0 || (rs.closure.Proven && !rs.stopped) {
		rs.net.Proven, rs.net.DrainUntil = false, time.Time{}
		rs.net.Reasons = append(rs.net.Reasons, rs.defects...)
		rs.net.Unresolved = append(rs.net.Unresolved, rs.defects...)
		if rs.closure.Proven && !rs.stopped {
			rs.net.Unresolved = append(rs.net.Unresolved, "the monitor was not told to stop")
		}
	}
	return rs
}

// monitorEnd lists what is wrong with how the monitor says it ended, judged against what the host asked and saw. A
// defect never discards the polls: it only invalidates the negative claim they make (a coverage gap), because after a
// stop there is no horizon left to fall back to and a valid poll that saw an escaped process stays a valid observation.
func (c *SSHCollector) monitorEnd(res monitorResult, rs *procResolution, stopped bool, maxGap time.Duration) []string {
	n := len(res.polls)
	if n == 0 {
		return []string{"the monitor record has no polls"}
	}
	var defects []string
	bad := func(format string, args ...any) { defects = append(defects, fmt.Sprintf(format, args...)) }
	first, last, st := res.polls[0].ObservedAt, res.polls[n-1].ObservedAt, res.stop
	if !st.FirstPollAt.Equal(first) || !st.LastPollAt.Equal(last) || st.StoppedAt.Before(last) {
		bad("the stated endpoints are not those of the record")
	}
	switch st.Reason {
	case StopClosedOnProof:
		switch {
		case !stopped || st.ClosureDigest != rs.stop.ClosureDigest || st.RunID != rs.stop.RunID:
			bad("the monitor closed on a proof the host did not give")
		case len(rs.snaps) != 2 || last.Before(rs.scanAt) || last.Before(rs.snaps[1].ObservedAt):
			bad("the monitor stopped before the closure scan and the final-cleanup snapshot")
		}
	case StopHorizon:
		if last.Sub(first) < time.Duration(c.durationSeconds())*time.Second-GuestMaxGap {
			bad("the monitor claims its horizon but its record is shorter")
		}
	default:
		bad("unknown stop reason %q", st.Reason)
	}
	// The evaluator checks the polls up to the post-teardown snapshot; the record is a witness through its last poll.
	if err := CoverageHealthy(res.polls, maxGap); err != nil {
		bad("coverage through the end of the record: %v", err)
	}
	return defects
}

// Proc implements runner.Collectors.
func (c *SSHCollector) Proc(ctx context.Context, ref runner.EvidenceRef) (runner.ObserverEvidence, error) {
	c.mu.Lock()
	iv := c.active[ref.Interval]
	c.mu.Unlock()
	if iv == nil {
		return runner.ObserverEvidence{}, fmt.Errorf("%w: no PROC interval was begun for %s", runner.ErrEvidenceAbsent, ref.Interval)
	}
	rs := c.finalize(ctx, ref, iv)
	if rs.waitErr != nil {
		return runner.ObserverEvidence{}, fmt.Errorf("%w: waiting for the teardown deadline: %v", runner.ErrEvidenceAbsent, rs.waitErr)
	}
	polls, stop, defects := rs.polls, rs.stop2, rs.defects
	if len(rs.snaps) == 0 && len(polls) == 0 {
		return runner.ObserverEvidence{}, fmt.Errorf("%w: the guest returned no usable PROC document", runner.ErrEvidenceAbsent)
	}
	policy := c.Policy
	policy.LingeringInit = c.InitLingers != nil && c.InitLingers(ref)
	policy.Unresolved, policy.CoverageDefects = rs.closure.Unresolved, defects
	if rs.scan != nil {
		policy.Extra = []Snapshot{*rs.scan}
	}
	rep := Evaluate(iv.reg, rs.snaps, polls, policy)
	refs := Refs(rep)
	if len(refs) == 0 {
		return runner.ObserverEvidence{}, fmt.Errorf("%w: the PROC report has no digest", runner.ErrEvidenceAbsent)
	}
	if c.DrainGrace != 0 {
		// How the interval ended is evidence: retain it, and bind it into every reference.
		term := Termination{Schema: TerminationSchema, Interval: ref.Interval, RunID: ref.RunID, Registration: iv.reg.Digest(),
			Proven: rs.closure.Proven, Reasons: rs.closure.Reasons, Unresolved: rs.closure.Unresolved, ClosureDigest: rs.closure.Digest,
			Closure: rs.doc, Stop: stop, PollCount: len(polls), CoverageDefects: defects}
		if err := retainTermination(c.RetainDir, term); err != nil {
			return runner.ObserverEvidence{}, fmt.Errorf("%w: the termination record could not be retained: %v", runner.ErrEvidenceAbsent, err)
		}
		bound := term.Digest()
		for k, r := range refs {
			refs[k] = runner.OracleRef{Version: r.Version, Report: BindDigest(r.Report, bound)}
		}
	}
	return runner.ObserverEvidence{Findings: Findings(rep), Refs: refs}, nil
}
