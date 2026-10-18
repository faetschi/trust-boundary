package netoracle

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"sync"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/runner"
)

// Collector is the NET collector of the governed executor. The registered synthetic
// unauthorized sink runs on the guest (`tbound netsink`, a service of its own, listening on
// a guest loopback alias outside every cell's network namespace: cells run network-none, so
// any arrival means the cell left its namespace). The guest stays offline; the runner
// reaches the sink only through the read-only root evidence channel (`tbound evidence
// net-canary` and `net-journal`, forced-command SSH), and this collector turns the sink's
// journal into EFF-NET-01 for each adversarial interval.
//
// BeginInterval draws the interval tag and has the guest send the start canary right before
// the request goes out; Net has it send the end canary after the answer, fetches the
// journal from its first record through that end canary, checks it and evaluates it. The
// registered sink URL in the corpus is fixed and a frozen prompt cannot carry a
// per-interval tag, so the guest never sees the tag: a request reaches the sink as an
// untagged arrival and is detected by the canary window (accepted between the start and
// end canary, or a connection whose lifetime overlaps it), not by the tag. The tag only
// binds the two canaries to the interval in the journal.
//
// Any failure of the channel or of a check is runner.ErrEvidenceAbsent, never a verdict:
// a start or end canary that is not acknowledged, an SSH failure, a journal that does not
// verify, does not end on the head the guest named, or does not hold exactly the canaries
// that were acknowledged. EFF-NET-02 is not supplied (full VM-uplink capture is not built).
type Collector struct {
	runner.AbsentCollectors
	Client runner.EvidenceClient
	// NewTag draws the interval tag (32 lowercase hex digits); nil means a random one. A
	// test seam.
	NewTag func() (string, error)

	mu     sync.Mutex
	active map[string]*netInterval
}

type netInterval struct {
	tag   string
	start runner.NetCanaryDoc
}

// NewCollector returns the NET collector over the evidence transport.
func NewCollector(t runner.Transport) *Collector {
	return &Collector{Client: runner.EvidenceClient{Transport: t}}
}

// Validate checks the evidence transport.
func (c *Collector) Validate() error { return c.Client.Validate() }

func (c *Collector) canary(ctx context.Context, tag, phase string) (runner.NetCanaryDoc, error) {
	var doc runner.NetCanaryDoc
	if err := c.Client.CallDoc(ctx, &doc, runner.EvidenceNetCanary, "--phase", phase, "--interval", tag); err != nil {
		return doc, err
	}
	if doc.Schema != runner.EvidenceSchema || doc.Kind != runner.EvidenceNetCanary || doc.Phase != phase || doc.Interval != tag ||
		doc.Seq == 0 || !hashRE.MatchString(doc.Hash) {
		return doc, fmt.Errorf("%w: the %s canary document does not answer the request", runner.ErrEvidenceAbsent, phase)
	}
	return doc, nil
}

// BeginInterval implements runner.IntervalCollectors: it issues the start canary.
func (c *Collector) BeginInterval(ctx context.Context, ref runner.EvidenceRef) error {
	if err := c.Validate(); err != nil {
		return err
	}
	c.EndInterval(ref)
	newTag := c.NewTag
	if newTag == nil {
		newTag = token
	}
	tag, err := newTag()
	if err != nil || !tagRE.MatchString(tag) {
		return fmt.Errorf("%w: no interval tag", runner.ErrEvidenceAbsent)
	}
	start, err := c.canary(ctx, tag, PhaseStart)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil {
		c.active = map[string]*netInterval{}
	}
	c.active[ref.Interval] = &netInterval{tag: tag, start: start}
	return nil
}

// EndInterval implements runner.IntervalCollectors.
func (c *Collector) EndInterval(ref runner.EvidenceRef) {
	c.mu.Lock()
	delete(c.active, ref.Interval)
	c.mu.Unlock()
}

// Net implements runner.Collectors: it issues the end canary, fetches the journal and
// evaluates it.
func (c *Collector) Net(ctx context.Context, ref runner.EvidenceRef) (runner.ObserverEvidence, error) {
	if err := c.Validate(); err != nil {
		return runner.ObserverEvidence{}, err
	}
	c.mu.Lock()
	iv := c.active[ref.Interval]
	c.mu.Unlock()
	if iv == nil {
		return runner.ObserverEvidence{}, fmt.Errorf("%w: no NET interval was begun for %s", runner.ErrEvidenceAbsent, ref.Interval)
	}
	end, err := c.canary(ctx, iv.tag, PhaseEnd)
	if err != nil {
		return runner.ObserverEvidence{}, err
	}
	var doc runner.NetJournalDoc
	if err := c.Client.CallDoc(ctx, &doc, runner.EvidenceNetJournal, "--interval", iv.tag); err != nil {
		return runner.ObserverEvidence{}, err
	}
	journal, err := base64.StdEncoding.DecodeString(doc.JournalB64)
	absent := func(format string, args ...any) (runner.ObserverEvidence, error) {
		return runner.ObserverEvidence{}, fmt.Errorf("%w: "+format, append([]any{runner.ErrEvidenceAbsent}, args...)...)
	}
	if doc.Schema != runner.EvidenceSchema || doc.Kind != runner.EvidenceNetJournal || doc.Interval != iv.tag || err != nil || len(journal) == 0 {
		return absent("the journal document does not answer the request")
	}
	trace, err := audit.Verify(bytes.NewReader(journal))
	if err != nil || len(trace.Records) == 0 || len(trace.Records) != doc.Count || trace.Records[len(trace.Records)-1].Hash != doc.Head {
		return absent("the journal does not verify against the head the guest named")
	}
	// The journal must hold the very canaries the guest acknowledged, at their positions.
	rep := EvaluateNET01(NET01Input{Journal: journal, Interval: ref.Interval, Tag: iv.tag})
	switch {
	case rep.StartSeq != iv.start.Seq || rep.EndSeq != end.Seq || doc.StartSeq != iv.start.Seq || doc.EndSeq != end.Seq:
		return absent("the journal does not hold the acknowledged canaries at their positions")
	case int(end.Seq) != len(trace.Records) || trace.Records[iv.start.Seq-1].Hash != iv.start.Hash || doc.Head != end.Hash:
		return absent("the acknowledged canary hashes are not in the journal")
	}
	digest, err := rep.Digest()
	if err != nil || digest == "" {
		return absent("the NET report has no digest")
	}
	return runner.ObserverEvidence{
		Findings: OraclesFromNET01(rep),
		Refs:     map[string]runner.OracleRef{runner.OracleNet01: {Version: rep.OracleVersion, Report: digest}},
	}, nil
}
