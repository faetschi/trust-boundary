package procoracle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	err   error
}

type procInterval struct {
	reg    Registration
	done   chan monitorResult
	cancel context.CancelFunc
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
	}
	if err := c.Client.Validate(); err != nil {
		return err
	}
	if !c.Client.CanReport() {
		return errors.New("procoracle: the evidence transport cannot report the monitor's first poll (runner.ReadyTransport)")
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
	args = append(args, "--duration", strconv.Itoa(int((c.MonitorDuration+time.Second-1)/time.Second)),
		"--interval", strconv.Itoa(int(c.pollInterval()/time.Millisecond)))
	readyCh := make(chan []byte, 1)
	go func() {
		raw, err := c.Client.CallReady(mctx, func(line []byte) {
			select {
			case readyCh <- line:
			default:
			}
		}, args...)
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
		iv.done <- monitorResult{polls: doc.Polls}
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

// Proc implements runner.Collectors.
func (c *SSHCollector) Proc(ctx context.Context, ref runner.EvidenceRef) (runner.ObserverEvidence, error) {
	c.mu.Lock()
	iv := c.active[ref.Interval]
	c.mu.Unlock()
	if iv == nil {
		return runner.ObserverEvidence{}, fmt.Errorf("%w: no PROC interval was begun for %s", runner.ErrEvidenceAbsent, ref.Interval)
	}
	if err := c.wait(ctx, iv.reg.Deadline); err != nil {
		return runner.ObserverEvidence{}, fmt.Errorf("%w: waiting for the teardown deadline: %v", runner.ErrEvidenceAbsent, err)
	}
	var snaps []Snapshot
	for _, p := range []Point{PointPostTeardown, PointFinalCleanup} {
		if s, err := c.capture(ctx, iv.reg, p); err == nil {
			snaps = append(snaps, s)
		}
	}
	var polls []Snapshot
	select {
	case res := <-iv.done:
		if res.err == nil {
			polls = res.polls
		}
	case <-ctx.Done():
	}
	if len(snaps) == 0 && len(polls) == 0 {
		return runner.ObserverEvidence{}, fmt.Errorf("%w: the guest returned no usable PROC document", runner.ErrEvidenceAbsent)
	}
	policy := c.Policy
	policy.LingeringInit = c.InitLingers != nil && c.InitLingers(ref)
	rep := Evaluate(iv.reg, snaps, polls, policy)
	refs := Refs(rep)
	if len(refs) == 0 {
		return runner.ObserverEvidence{}, fmt.Errorf("%w: the PROC report has no digest", runner.ErrEvidenceAbsent)
	}
	return runner.ObserverEvidence{Findings: Findings(rep), Refs: refs}, nil
}
