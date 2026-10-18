package netoracle

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"tbound/supervisor/internal/runner"
)

// Collector is the host-local NET collector of the governed executor. It owns nothing but
// a Sink (the registered synthetic unauthorized sink, listening on the host next to the
// runner at the address the guest can reach, the Default Switch host IP) and turns
// the sink's journal into EFF-NET-01 for each adversarial interval.
//
// BeginInterval issues the interval tag and sends the start canary right before the
// request goes out; Net sends the end canary after the answer, reads the journal and
// evaluates it. The registered sink URL in the corpus is fixed and a frozen prompt cannot
// carry a per-interval tag, so the guest never sees the tag: a request reaches the sink as
// an untagged arrival and is detected by the canary window (accepted between the start
// and end canary, or a connection whose lifetime overlaps it), not by the tag. The tag
// still binds the canaries to the interval in the journal. Tagged arrivals are only
// possible for a prompt that was given the interval's own URL.
//
// A failed start or end canary does not hide an arrival (positive evidence stands) but
// leaves a clean journal evidence_lost (no canary pair), never none. A journal the sink
// can no longer vouch for is ErrEvidenceAbsent. EFF-NET-02 is not supplied (full
// VM-uplink capture is not built).
type Collector struct {
	runner.AbsentCollectors
	Sink *Sink

	mu     sync.Mutex
	active map[string]*netInterval
}

type netInterval struct {
	tag         string
	startCanary error
}

// Validate checks that the collector has a sink.
func (c *Collector) Validate() error {
	if c.Sink == nil {
		return errors.New("netoracle: the NET collector has no sink")
	}
	return nil
}

// BeginInterval implements runner.IntervalCollectors.
func (c *Collector) BeginInterval(ctx context.Context, ref runner.EvidenceRef) error {
	if err := c.Validate(); err != nil {
		return err
	}
	iv, err := c.Sink.NewInterval()
	if err != nil {
		return fmt.Errorf("%w: %v", runner.ErrEvidenceAbsent, err)
	}
	state := &netInterval{tag: iv.Tag, startCanary: c.Sink.Canary(ctx, iv.Tag, PhaseStart)}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil {
		c.active = map[string]*netInterval{}
	}
	c.active[ref.Interval] = state
	return nil
}

// EndInterval implements runner.IntervalCollectors.
func (c *Collector) EndInterval(ref runner.EvidenceRef) {
	c.mu.Lock()
	delete(c.active, ref.Interval)
	c.mu.Unlock()
}

// Net implements runner.Collectors.
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
	// The end canary is best effort: without it the report is evidence_lost unless an
	// arrival stands on its own.
	_ = c.Sink.Canary(ctx, iv.tag, PhaseEnd)
	journal, err := c.Sink.Journal()
	if err != nil {
		return runner.ObserverEvidence{}, fmt.Errorf("%w: %v", runner.ErrEvidenceAbsent, err)
	}
	rep := EvaluateNET01(NET01Input{Journal: journal, Interval: ref.Interval, Tag: iv.tag})
	digest, err := rep.Digest()
	if err != nil || digest == "" {
		return runner.ObserverEvidence{}, fmt.Errorf("%w: the NET report has no digest", runner.ErrEvidenceAbsent)
	}
	return runner.ObserverEvidence{
		Findings: OraclesFromNET01(rep),
		Refs:     map[string]runner.OracleRef{runner.OracleNet01: {Version: rep.OracleVersion, Report: digest}},
	}, nil
}
