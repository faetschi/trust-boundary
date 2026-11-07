package netoracle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

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
	// RetainDir, if set, receives one file per evaluated interval (Retained, named RetainedName): the
	// original canary and journal documents. A file that cannot be written makes the interval evidence
	// absent, so a verdict is never journaled without the evidence behind it.
	RetainDir string
	// Gate is D49's closure source (the PROC collector). With it, the end canary is sent only after the drain deadline
	// of a proven closure, or after the original horizon when there is no proof; a gate that knows no interval leaves
	// the end canary where it was. Nil sends it right after the answer.
	Gate runner.ClosureSource
	// Wait is a test seam: it returns when until has passed (a timer by default).
	Wait func(ctx context.Context, until time.Time) error

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

func (c *Collector) wait(ctx context.Context, until time.Time) error {
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
	var closure *runner.IntervalClosure
	if c.Gate != nil {
		cl := c.Gate.Closure(ctx, ref)
		closure = &cl
		until := cl.Horizon
		if cl.Proven {
			until = cl.DrainUntil
		}
		if !until.IsZero() {
			if err := c.wait(ctx, until); err != nil {
				return runner.ObserverEvidence{}, fmt.Errorf("%w: waiting to end the NET window: %v", runner.ErrEvidenceAbsent, err)
			}
		}
	}
	end, err := c.canary(ctx, iv.tag, PhaseEnd)
	if err != nil {
		return runner.ObserverEvidence{}, err
	}
	var doc runner.NetJournalDoc
	if err := c.Client.CallDoc(ctx, &doc, runner.EvidenceNetJournal, "--interval", iv.tag); err != nil {
		return runner.ObserverEvidence{}, err
	}
	absent := func(format string, args ...any) (runner.ObserverEvidence, error) {
		return runner.ObserverEvidence{}, fmt.Errorf("%w: "+format, append([]any{runner.ErrEvidenceAbsent}, args...)...)
	}
	rep, err := VerifyInterval(ref.Interval, iv.start, end, doc)
	if err != nil {
		return absent("%v", err)
	}
	digest, err := rep.Digest()
	if err != nil || digest == "" {
		return absent("the NET report has no digest")
	}
	if c.RetainDir != "" {
		if err := retain(c.RetainDir, Retained{Schema: RetainedSchema, Interval: ref.Interval, Start: iv.start, End: end, Journal: doc, Closure: closure}); err != nil {
			return absent("the interval evidence could not be retained: %v", err)
		}
	}
	findings, digest := BindClosure(OraclesFromNET01(rep), digest, closure)
	return runner.ObserverEvidence{
		Findings: findings,
		Refs:     map[string]runner.OracleRef{runner.OracleNet01: {Version: rep.OracleVersion, Report: digest}},
	}, nil
}

// BindClosure applies the D49 closure the end canary waited on to a NET report: the closure is folded into the
// reference digest, and unresolved closure evidence turns a clean result into lost (an observed arrival stands). The
// collector and the verifier of retained intervals both use it. A nil closure changes nothing.
func BindClosure(findings map[string]runner.Finding, digest string, closure *runner.IntervalClosure) (map[string]runner.Finding, string) {
	if closure == nil {
		return findings, digest
	}
	raw, _ := json.Marshal(closure)
	sum := sha256.Sum256(append([]byte(digest+"|"), raw...))
	if len(closure.Unresolved) != 0 && findings[runner.OracleNet01] == runner.FindingNone {
		findings[runner.OracleNet01] = runner.FindingLost
	}
	return findings, hex.EncodeToString(sum[:])
}
