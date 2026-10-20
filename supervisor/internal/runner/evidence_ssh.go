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

// StdinTransport is a ReadyTransport that also gives the remote command a stdin the caller controls: the command
// sees what is written to stdin and its end of file when the caller closes it, or when the session is gone (the
// guest transient-watch ends on a stop line and treats an end of file without one as an abort).
type StdinTransport interface {
	ReadyTransport
	RunReadyStdin(ctx context.Context, argv []string, stdin io.Reader, ready func(firstLine []byte)) (stdout, stderr []byte, exitCode int, err error)
}

// CanStop reports whether the evidence transport can hand the remote command a controlled stdin.
func (c EvidenceClient) CanStop() bool {
	_, ok := c.Transport.(StdinTransport)
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
	return evidenceOutput(stdout, stderr, exit, err)
}

func evidenceOutput(stdout, stderr []byte, exit int, err error) ([]byte, error) {
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
	E04From, JournalFrom, PublicationFrom, NetFrom, ProcFrom, TransientFrom Collectors
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

func (s CollectorSet) Publication(ctx context.Context, ref EvidenceRef) (PublicationInputs, error) {
	return s.slot(s.PublicationFrom).Publication(ctx, ref)
}

func (s CollectorSet) Net(ctx context.Context, ref EvidenceRef) (ObserverEvidence, error) {
	return s.slot(s.NetFrom).Net(ctx, ref)
}

func (s CollectorSet) Proc(ctx context.Context, ref EvidenceRef) (ObserverEvidence, error) {
	return s.slot(s.ProcFrom).Proc(ctx, ref)
}

func (s CollectorSet) Transient(ctx context.Context, ref EvidenceRef) (ObserverEvidence, error) {
	return s.slot(s.TransientFrom).Transient(ctx, ref)
}

// distinct returns the slots once each. Slots whose dynamic type is not comparable are
// kept as they are.
func (s CollectorSet) distinct() []Collectors {
	var out []Collectors
	for _, c := range []Collectors{s.E04From, s.JournalFrom, s.PublicationFrom, s.NetFrom, s.ProcFrom, s.TransientFrom} {
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

// SSHCollectors collects E04 manifests, the publication interval journal and the EFF-WS-05 and
// EFF-WS-06 inputs from the guest. NET and PROC stay absent here (see CollectorSet), and so does
// the transient observer (TransientCollector).
//
// E04. The seven registered points are moments inside the service run (materialization,
// settlement, teardown import, publication, cleanup). A collector outside the service
// cannot sample those moments, so the service takes the manifest itself at each of them and
// journals it in a per-workflow store whose head and count the receipt binds
// (cmd/tbound/production_e04_points_linux.go). E04 fetches that store with the guest kind
// e04-points and E04PointsInput verifies it against the receipt binding. A receipt without
// the binding, a store that does not match it, or any transport or decode failure is
// ErrEvidenceAbsent, never a partial input. A point the service could not snapshot is a
// gap and stays missing, so its verdicts are evidence_lost; a published interval's ledger
// basis is unavailable for the same reason (see E04LedgerUnavailable).
type SSHCollectors struct {
	AbsentCollectors
	Client EvidenceClient
}

// NewSSHCollectors returns collectors over the evidence transport.
func NewSSHCollectors(t Transport) *SSHCollectors {
	return &SSHCollectors{Client: EvidenceClient{Transport: t}}
}

// Validate checks the evidence transport.
func (c *SSHCollectors) Validate() error { return c.Client.Validate() }

// E04 implements Collectors.
func (c *SSHCollectors) E04(ctx context.Context, ref EvidenceRef) (wsoracle.EvaluateE04Input, error) {
	doc, err := c.Client.e04Doc(ctx, ref)
	if err != nil {
		return wsoracle.EvaluateE04Input{}, err
	}
	return E04PointsInput(doc, ref.Interval, ref.E04Head, ref.E04Count)
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

// Publication implements Collectors. EFF-WS-05 and EFF-WS-06 are publication predicates, so their inputs are the
// ones publication itself keeps: the service's baseline, pre-publication and post-publication manifests, the
// approved-delta ledger it applied and, in the post-publication record, the sealed generation's manifest, the
// intended affected set and the lock identity (E04PublicationEvidence). They come from the same store the receipt
// binds as E04 does (e04-points), so a document that does not match the receipt is absent, and the interval journal
// stays the only judge of whether the interval published at all (PublicationFree).
func (c *SSHCollectors) Publication(ctx context.Context, ref EvidenceRef) (PublicationInputs, error) {
	doc, err := c.Client.e04Doc(ctx, ref)
	if err != nil {
		return PublicationInputs{}, err
	}
	in, err := E04PublicationInputs(doc, ref.Interval, ref.E04Head, ref.E04Count)
	if err != nil {
		return PublicationInputs{}, err
	}
	// The transaction the store says it recorded must be one the anchored publication journal holds.
	journal, err := c.PublicationJournal(ctx, ref)
	if err != nil {
		return PublicationInputs{}, err
	}
	if err := bindPublicationTransaction(journal, ref.PublicationHead, in); err != nil {
		return PublicationInputs{}, err
	}
	return in, nil
}

// e04Doc fetches the E04 point store of the interval the receipt binds and checks that it is the store of the run the
// receipt names: the store of a workflow can hold the points of earlier runs, and the receipt's head and count bind
// the whole store, not one run.
func (c EvidenceClient) e04Doc(ctx context.Context, ref EvidenceRef) (E04PointsDoc, error) {
	if ref.E04Head == "" || ref.E04Count <= 0 {
		return E04PointsDoc{}, evidenceAbsent("the receipt carries no E04 evidence binding for %s", ref.Interval)
	}
	var doc E04PointsDoc
	if err := c.CallDoc(ctx, &doc, EvidenceE04Points, "--interval-ref", ref.Interval); err != nil {
		return E04PointsDoc{}, err
	}
	// An empty RunID on either side never matches (e04DocRecords refuses a store with none).
	if doc.RunID != ref.RunID {
		return E04PointsDoc{}, evidenceAbsent("the E04 store is for run %q, the receipt names %q", doc.RunID, ref.RunID)
	}
	return doc, nil
}

// CallStdin is CallReady with a stdin for the remote command (a transport that cannot give one is a configuration
// error, not absent evidence). It also says whether the remote command ended with an exit status of its own: the
// transport reported one (it did not fail, and the command was not killed by a signal or the end of its session), and
// it is not the 255 ssh itself exits with.
func (c EvidenceClient) CallStdin(ctx context.Context, stdin io.Reader, ready func([]byte), argv ...string) ([]byte, bool, error) {
	st, ok := c.Transport.(StdinTransport)
	if !ok {
		return nil, false, errors.New("evidence transport cannot give the command a controlled stdin (StdinTransport)")
	}
	stdout, stderr, exit, err := st.RunReadyStdin(ctx, argv, stdin, ready)
	raw, oerr := evidenceOutput(stdout, stderr, exit, err)
	return raw, err == nil && exit >= 0 && exit != 255, oerr
}
