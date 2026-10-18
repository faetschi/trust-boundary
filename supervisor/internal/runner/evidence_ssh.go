package runner

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sync"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/wsoracle"
)

// Guest evidence collection. The guest side is the read-only `tbound evidence <kind>`
// subcommand (cmd/tbound/evidence_linux.go), reached over SSH with a key of its own that
// the guest restricts to that subcommand (infra/guest/PROVISIONING-SIGNING.md, Evidence
// collection). This file is the wire contract both sides share and the host half of the
// E04 and publication-journal evidence. The PROC half is procoracle.SSHCollector and the
// NET half netoracle.Collector, because those packages import the runner.

// EvidenceSchema marks every document `tbound evidence` writes.
const EvidenceSchema = "tbound-evidence/v1"

// The evidence kinds of the guest subcommand.
const (
	EvidenceE04Snapshot        = "e04-snapshot"
	EvidenceProcCell           = "proc-cell"
	EvidenceProcCapture        = "proc-capture"
	EvidenceProcMonitor        = "proc-monitor"
	EvidencePublicationJournal = "publication-interval-journal"
	// EvidenceNetCanary is the one evidence kind with an effect: it sends one canary request to
	// the guest NET sink (EFF-NET-01), and only to the sink. EvidenceNetJournal reads the sink journal.
	EvidenceNetCanary  = "net-canary"
	EvidenceNetJournal = "net-journal"
	// EvidenceSSHOriginalCommand is the forced-command entry: it reads
	// $SSH_ORIGINAL_COMMAND, validates it word by word (no shell) and dispatches one of
	// the kinds above.
	EvidenceSSHOriginalCommand = "ssh-original-command"
)

const (
	// EvidenceBinaryPath is the guest binary the evidence key may run (the ro bind
	// mount of the runbook).
	EvidenceBinaryPath = "/usr/local/bin/tbound"
	// EvidenceLiveRoot is the one protected root e04-snapshot accepts. The guest pins it
	// to piruntime.D09ManagedPiLiveRootPath (a test keeps the two equal).
	EvidenceLiveRoot = "/var/lib/tbound/live"

	// The guest NET sink (`tbound netsink`, infra/guest/systemd/tbound-netsink.service) and its
	// fixed places. NetsinkAddr is a loopback alias of the guest root network namespace: a cell
	// (network-none, its own namespace) can never reach it, so any arrival means the cell left
	// its namespace. NetsinkURL is the value of {{REGISTERED_SINK_URL}} in the corpus.
	NetsinkAddr        = "127.0.33.1:8080"
	NetsinkURL         = "http://" + NetsinkAddr
	NetsinkJournalPath = "/var/lib/tbound-netsink/journal.jsonl"
	NetsinkSecretPath  = "/etc/tbound/netsink.secret"
)

// DefaultEvidenceCommand is the guest-side launcher of the evidence subcommand: no
// setpriv, no client identity. The evidence key's login (root through a forced command)
// is the guest's decision, not this package's.
func DefaultEvidenceCommand() []string { return []string{EvidenceBinaryPath, "evidence"} }

// E04SnapshotDoc is the output of `tbound evidence e04-snapshot`.
type E04SnapshotDoc struct {
	Schema   string            `json:"schema"`
	Kind     string            `json:"kind"`
	Point    string            `json:"point"`
	Root     string            `json:"root"`
	Manifest wsoracle.Manifest `json:"manifest"`
}

// PublicationJournalDoc is the output of `tbound evidence publication-interval-journal`.
// Head and Count are the service's own anchor for the interval (the function the
// receipt anchor uses); JournalB64 is the interval journal in audit format.
type PublicationJournalDoc struct {
	Schema      string `json:"schema"`
	Kind        string `json:"kind"`
	IntervalRef string `json:"interval_ref"`
	JournalB64  string `json:"journal_b64"`
	Head        string `json:"head"`
	Count       int    `json:"count"`
}

// NetCanaryDoc is the output of `tbound evidence net-canary`: the canary was journaled by the
// sink at Seq, whose record hash is Hash.
type NetCanaryDoc struct {
	Schema   string `json:"schema"`
	Kind     string `json:"kind"`
	Phase    string `json:"phase"`
	Interval string `json:"interval"`
	Seq      uint64 `json:"seq"`
	Hash     string `json:"hash"`
}

// NetJournalDoc is the output of `tbound evidence net-journal`: the sink journal from its first
// record through the end canary of the interval tag (Count records, the last one hashing to
// Head), base64, with the sequence numbers of the interval's first start canary and last end
// canary.
type NetJournalDoc struct {
	Schema     string `json:"schema"`
	Kind       string `json:"kind"`
	Interval   string `json:"interval"`
	JournalB64 string `json:"journal_b64"`
	Head       string `json:"head"`
	Count      int    `json:"count"`
	StartSeq   uint64 `json:"start_seq"`
	EndSeq     uint64 `json:"end_seq"`
}

// DecodeEvidenceDoc decodes exactly one JSON document, with no unknown field and no
// trailing data.
func DecodeEvidenceDoc(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after the evidence document")
	}
	return nil
}

func evidenceAbsent(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrEvidenceAbsent, fmt.Sprintf(format, args...))
}

// EvidenceClient calls the guest evidence subcommand through a Transport. Use a
// transport of its own (its own SSH user and key), never the client transport.
type EvidenceClient struct {
	Transport Transport
}

// Validate refuses a transport that cannot be the evidence channel: none, one that
// fails its own validation, or an SSHTransport whose launcher is not the evidence
// command (the client launcher, for example). It is a configuration error, not
// absent evidence.
func (c EvidenceClient) Validate() error {
	if c.Transport == nil {
		return errors.New("evidence transport is not wired")
	}
	if v, ok := c.Transport.(interface{ Validate() error }); ok {
		if err := v.Validate(); err != nil {
			return fmt.Errorf("evidence transport: %w", err)
		}
	}
	// Both SSHTransport and *SSHTransport expose the launcher; any transport that names
	// one must name exactly the evidence command.
	if t, ok := c.Transport.(interface{ launcher() []string }); ok && !slices.Equal(t.launcher(), DefaultEvidenceCommand()) {
		return errors.New("evidence transport must launch exactly the evidence command (SSHTransport.ClientCommand = DefaultEvidenceCommand())")
	}
	return nil
}

// ReadyTransport is a Transport that can tell the caller when the remote command wrote its
// first stdout line while the command keeps running (the guest proc-monitor acknowledges its
// first poll that way). ready is called at most once, with that line, from the transport's
// own goroutine; the returned stdout still holds everything the command wrote, the first
// line included.
type ReadyTransport interface {
	Transport
	RunReady(ctx context.Context, argv []string, ready func(firstLine []byte)) (stdout, stderr []byte, exitCode int, err error)
}

// CanReport reports whether the evidence transport can confirm a first line early.
func (c EvidenceClient) CanReport() bool {
	_, ok := c.Transport.(ReadyTransport)
	return ok
}

// CallReady is Call over a ReadyTransport: ready receives the command's first stdout line as
// soon as it is written. A transport that cannot report it is a configuration error, not
// absent evidence.
func (c EvidenceClient) CallReady(ctx context.Context, ready func([]byte), argv ...string) ([]byte, error) {
	rt, ok := c.Transport.(ReadyTransport)
	if !ok {
		return nil, errors.New("evidence transport cannot report the first output line (ReadyTransport)")
	}
	stdout, stderr, exit, err := rt.RunReady(ctx, argv, ready)
	switch {
	case err != nil:
		return nil, evidenceAbsent("evidence transport: %v", err)
	case exit != 0:
		return nil, evidenceAbsent("evidence command exited %d: %s", exit, clip(stderr))
	case len(bytes.TrimSpace(stdout)) == 0:
		return nil, evidenceAbsent("evidence command wrote nothing")
	}
	return stdout, nil
}

// Call runs `tbound evidence <argv...>` and returns its one JSON document. Any failure
// (no transport, a transport error, a non-zero exit, no output) is ErrEvidenceAbsent.
func (c EvidenceClient) Call(ctx context.Context, argv ...string) ([]byte, error) {
	if c.Transport == nil {
		return nil, evidenceAbsent("no evidence transport")
	}
	stdout, stderr, exit, err := c.Transport.Run(ctx, argv)
	switch {
	case err != nil:
		return nil, evidenceAbsent("evidence transport: %v", err)
	case exit != 0:
		return nil, evidenceAbsent("evidence command exited %d: %s", exit, clip(stderr))
	case len(bytes.TrimSpace(stdout)) == 0:
		return nil, evidenceAbsent("evidence command wrote nothing")
	}
	return stdout, nil
}

// CallDoc is Call plus a strict decode into v.
func (c EvidenceClient) CallDoc(ctx context.Context, v any, argv ...string) error {
	raw, err := c.Call(ctx, argv...)
	if err != nil {
		return err
	}
	if err := DecodeEvidenceDoc(raw, v); err != nil {
		return evidenceAbsent("malformed evidence document: %v", err)
	}
	return nil
}

// IntervalCollectors may be implemented by Collectors that must act before the request
// is sent (a baseline snapshot, a NET start canary, a PROC monitor). GovernedC calls
// BeginInterval for an adversarial step right before it sends the request, and
// EndInterval exactly once when Execute returns, whatever the outcome, so that nothing a
// collector started outlives the step. BeginInterval returning ErrEvidenceAbsent leaves
// that evidence absent; any other error makes the outcome partial. The request is sent
// either way.
type IntervalCollectors interface {
	BeginInterval(ctx context.Context, ref EvidenceRef) error
	EndInterval(ref EvidenceRef)
}

// CollectorSet assembles Collectors from several sources: each method is served by its
// own slot, and a nil slot is AbsentCollectors. BeginInterval and EndInterval reach every
// distinct slot that implements IntervalCollectors once.
type CollectorSet struct {
	E04From, JournalFrom, NetFrom, ProcFrom Collectors
}

func (s CollectorSet) slot(c Collectors) Collectors {
	if c == nil {
		return AbsentCollectors{}
	}
	return c
}

func (s CollectorSet) E04(ctx context.Context, ref EvidenceRef) (wsoracle.EvaluateE04Input, error) {
	return s.slot(s.E04From).E04(ctx, ref)
}

func (s CollectorSet) PublicationJournal(ctx context.Context, ref EvidenceRef) ([]byte, error) {
	return s.slot(s.JournalFrom).PublicationJournal(ctx, ref)
}

func (s CollectorSet) Net(ctx context.Context, ref EvidenceRef) (ObserverEvidence, error) {
	return s.slot(s.NetFrom).Net(ctx, ref)
}

func (s CollectorSet) Proc(ctx context.Context, ref EvidenceRef) (ObserverEvidence, error) {
	return s.slot(s.ProcFrom).Proc(ctx, ref)
}

// distinct returns the slots once each. Slots whose dynamic type is not comparable are
// kept as they are.
func (s CollectorSet) distinct() []Collectors {
	var out []Collectors
	for _, c := range []Collectors{s.E04From, s.JournalFrom, s.NetFrom, s.ProcFrom} {
		if c == nil {
			continue
		}
		dup := false
		if reflect.TypeOf(c).Comparable() {
			for _, o := range out {
				if reflect.TypeOf(o) == reflect.TypeOf(c) && o == c {
					dup = true
					break
				}
			}
		}
		if !dup {
			out = append(out, c)
		}
	}
	return out
}

// BeginInterval implements IntervalCollectors.
func (s CollectorSet) BeginInterval(ctx context.Context, ref EvidenceRef) error {
	var errs []error
	for _, c := range s.distinct() {
		if ic, ok := c.(IntervalCollectors); ok {
			if err := ic.BeginInterval(ctx, ref); err != nil && !errors.Is(err, ErrEvidenceAbsent) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// EndInterval implements IntervalCollectors.
func (s CollectorSet) EndInterval(ref EvidenceRef) {
	for _, c := range s.distinct() {
		if ic, ok := c.(IntervalCollectors); ok {
			ic.EndInterval(ref)
		}
	}
}

// Validate implements the optional configuration check GovernedC runs in Prepare.
func (s CollectorSet) Validate() error {
	var errs []error
	for _, c := range s.distinct() {
		if v, ok := c.(interface{ Validate() error }); ok {
			if err := v.Validate(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// SSHCollectors collects E04 manifests and the publication interval journal from the
// guest. NET and PROC stay absent here (see CollectorSet).
//
// E04. The seven registered points are moments inside the service run (materialization,
// settlement, teardown import, publication, cleanup). A collector outside the service
// cannot sample those moments, so it observes the protected root at two times: the
// baseline, in BeginInterval right before the request is sent, and after the run, when
// the service has answered (teardown and cleanup are over). Only those two are observed:
// the end state is the one final_cleanup snapshot and the five internal points stay absent,
// so EvaluateE04 answers evidence_lost for WS-01..04 (with any end-state difference still
// listed as a finding), never no_effect (see e04LaterPoints). The approved-delta ledger is not collected: E04 is served
// only for an interval whose receipt anchor shows no publication, since a published
// change is an approved change that only a ledger can explain; any other interval stays
// absent.
type SSHCollectors struct {
	AbsentCollectors
	Client EvidenceClient

	mu        sync.Mutex
	baselines map[string]wsoracle.Manifest
}

// NewSSHCollectors returns collectors over the evidence transport.
func NewSSHCollectors(t Transport) *SSHCollectors {
	return &SSHCollectors{Client: EvidenceClient{Transport: t}}
}

// Validate checks the evidence transport.
func (c *SSHCollectors) Validate() error { return c.Client.Validate() }

// TODO(service-lifecycle-snapshots): the six internal E04 points (post_materialization,
// post_settlement, post_teardown, pre_publication, post_publication, final_cleanup) are
// moments inside the service run. A collector outside the service sees only the state
// before the request and the state after the answer, so it must not label an end-state
// snapshot with an internal point: a file changed at post_settlement and restored before
// the answer would then read as no_effect. Only the end state is collected, as
// final_cleanup (the last point, the one the end of the run is), and the other five stay
// absent so EvaluateE04 returns evidence_lost for WS-01..04. The real fix is service-side
// manifests captured at the service's own lifecycle barriers.
var e04LaterPoints = []string{wsoracle.PointFinalCleanup}

func (c *SSHCollectors) snapshot(ctx context.Context, point string) (wsoracle.Manifest, error) {
	var doc E04SnapshotDoc
	if err := c.Client.CallDoc(ctx, &doc, EvidenceE04Snapshot, "--root", EvidenceLiveRoot, "--point", point); err != nil {
		return wsoracle.Manifest{}, err
	}
	if doc.Schema != EvidenceSchema || doc.Kind != EvidenceE04Snapshot || doc.Point != point ||
		doc.Root != EvidenceLiveRoot || doc.Manifest.Label != point || doc.Manifest.Root == nil {
		return wsoracle.Manifest{}, evidenceAbsent("e04 snapshot document does not answer the %s request", point)
	}
	return doc.Manifest, nil
}

// BeginInterval takes the baseline snapshot.
func (c *SSHCollectors) BeginInterval(ctx context.Context, ref EvidenceRef) error {
	c.mu.Lock()
	delete(c.baselines, ref.Interval)
	c.mu.Unlock()
	m, err := c.snapshot(ctx, wsoracle.PointBaseline)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.baselines == nil {
		c.baselines = map[string]wsoracle.Manifest{}
	}
	c.baselines[ref.Interval] = m
	return nil
}

// EndInterval drops the interval's baseline.
func (c *SSHCollectors) EndInterval(ref EvidenceRef) {
	c.mu.Lock()
	delete(c.baselines, ref.Interval)
	c.mu.Unlock()
}

// E04 implements Collectors.
func (c *SSHCollectors) E04(ctx context.Context, ref EvidenceRef) (wsoracle.EvaluateE04Input, error) {
	c.mu.Lock()
	baseline, ok := c.baselines[ref.Interval]
	c.mu.Unlock()
	if !ok {
		return wsoracle.EvaluateE04Input{}, evidenceAbsent("no baseline snapshot for %s", ref.Interval)
	}
	if !ref.PublicationsKnown || ref.Publications != 0 {
		return wsoracle.EvaluateE04Input{}, evidenceAbsent("the interval's publications are unknown or non-zero and no ledger is collected")
	}
	in := wsoracle.EvaluateE04Input{Baseline: baseline, Scope: wsoracle.E04Scope{Whole: true}}
	for _, p := range e04LaterPoints {
		m, err := c.snapshot(ctx, p)
		if err != nil {
			return wsoracle.EvaluateE04Input{}, err
		}
		in.Points = append(in.Points, wsoracle.E04Point{Name: p, Manifest: m, LedgerPrefixDigest: wsoracle.LedgerPrefixDigest(nil, 0)})
	}
	return in, nil
}

// PublicationJournal implements Collectors. The bytes must verify as an audit chain
// whose head is the one the guest named; whether they belong to the receipt's anchor is
// decided by PublicationFree, as for every journal.
func (c *SSHCollectors) PublicationJournal(ctx context.Context, ref EvidenceRef) ([]byte, error) {
	var doc PublicationJournalDoc
	if err := c.Client.CallDoc(ctx, &doc, EvidencePublicationJournal, "--interval-ref", ref.Interval); err != nil {
		return nil, err
	}
	if doc.Schema != EvidenceSchema || doc.Kind != EvidencePublicationJournal || doc.IntervalRef != ref.Interval {
		return nil, evidenceAbsent("journal document is not for %s", ref.Interval)
	}
	journal, err := base64.StdEncoding.DecodeString(doc.JournalB64)
	if err != nil || len(journal) == 0 {
		return nil, evidenceAbsent("journal bytes are not base64")
	}
	trace, err := audit.Verify(bytes.NewReader(journal))
	if err != nil || len(trace.Records) == 0 || trace.Records[len(trace.Records)-1].Hash != doc.Head {
		return nil, evidenceAbsent("journal does not verify against the head the guest named")
	}
	return journal, nil
}
