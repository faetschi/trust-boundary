package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/wsoracle"
)

// Errors of the guest-backed executor. Each one comes with the observations that were
// established, as a partial outcome, except where noted.
var (
	// ErrGovernedFailed: the service settled the run (status completed) but the receipt
	// says success false. The client exits 2.
	ErrGovernedFailed = errors.New("runner: governed run completed without success")
	// ErrGovernedUnknown: the service answered status unknown. Neither a success nor a
	// clean negative.
	ErrGovernedUnknown = errors.New("runner: governed run outcome is unknown")
	// ErrGovernedMalformed: the client's output, or its exit code beside it, is not what
	// the service protocol produces.
	ErrGovernedMalformed = errors.New("runner: governed client output is malformed")
	// ErrAnchorMismatch: the receipt's publication anchor does not belong to this interval.
	ErrAnchorMismatch = errors.New("runner: receipt publication anchor is not for this interval")
	// ErrEvidenceAbsent is what a Collectors method returns when it has no evidence for the
	// interval. It is not an error of the run: the evidence stays missing.
	ErrEvidenceAbsent = errors.New("runner: evidence not collected")
)

const (
	governedResponseSchema = "tbound-pi-service-response/v1"
	governedReceiptSchema  = "tbound-production-pi-receipt/v1"
)

// EvidenceRef tells a collector which interval it is collecting for.
type EvidenceRef struct {
	Step     Step
	Interval string // IntervalRef(Step)
	Request  GovernedRequest
	// RunID is the receipt's run_id, which names the audit files under
	// /var/lib/tbound/audit on the guest.
	RunID string
}

// Collectors supply the host-side evidence of one governed interval, next to the
// service's own receipt. Each method returns ErrEvidenceAbsent (or nothing usable)
// when it has none: absent evidence leaves the oracle missing, so the interval is
// incomplete and never "none". A collector is trusted instrumentation, like the
// executor; other errors make the outcome partial.
type Collectors interface {
	// E04 returns the manifests of the six E04 observation points, the baseline,
	// the ledger and the scope. The executor names the threat class from the step
	// and has wsoracle.EvaluateE04 judge them. Adversarial steps only.
	E04(ctx context.Context, ref EvidenceRef) (wsoracle.EvaluateE04Input, error)
	// PublicationJournal returns the bytes of the interval's publication journal
	// (the runner_interval record, then the workflow's publication records). They are
	// evidence only together with the receipt's anchor (PublicationFree).
	PublicationJournal(ctx context.Context, ref EvidenceRef) ([]byte, error)
	// Net returns findings for EFF-NET-01 and EFF-NET-02. Adversarial steps only.
	Net(ctx context.Context, ref EvidenceRef) (map[string]Finding, error)
	// Proc returns findings for EFF-PROC-01..03. Adversarial steps only.
	Proc(ctx context.Context, ref EvidenceRef) (map[string]Finding, error)
}

// AbsentCollectors has no evidence of any kind. It is the default, and the base for
// collectors that serve only some of it.
type AbsentCollectors struct{}

func (AbsentCollectors) E04(context.Context, EvidenceRef) (wsoracle.EvaluateE04Input, error) {
	return wsoracle.EvaluateE04Input{}, ErrEvidenceAbsent
}
func (AbsentCollectors) PublicationJournal(context.Context, EvidenceRef) ([]byte, error) {
	return nil, ErrEvidenceAbsent
}
func (AbsentCollectors) Net(context.Context, EvidenceRef) (map[string]Finding, error) {
	return nil, ErrEvidenceAbsent
}
func (AbsentCollectors) Proc(context.Context, EvidenceRef) (map[string]Finding, error) {
	return nil, ErrEvidenceAbsent
}

// GovernedC is the guest-backed adapter for Configuration C (and the governed replays
// of C).
//
// The mapping:
//
//   - one C step is one `tbound serve --pi --task-id <case_id> --role <role> --attempt
//     <1|2> --task-request-id <RequestID> --bash-no-outputs` client invocation against
//     /run/tbound/serve-pi.sock (Request and GovernedRequest.Argv), carried to the guest
//     by Transport (SSHTransport in production);
//   - the response line gives the terminal state. status completed with receipt
//     success true and exit 0 is the only success. Completed with success false (exit 2)
//     is ErrGovernedFailed, status unknown (exit 2) is ErrGovernedUnknown, refused and
//     busy are setup failures (ErrSetupRefused), and anything that is not exactly
//     that (bad JSON, other request id, wrong schema, a receipt that does not parse, an
//     exit code that does not fit the status) is ErrGovernedMalformed. Failed, unknown
//     and malformed output with a response line come back as a partial outcome, never
//     a success; output without a response line is a setup failure;
//   - E01 identity is the digest of the response line (the service's terminal digest,
//     piServiceTerminalDigest), never marked verified here: the host has not checked it
//     against the registered pins. Latency is the wall time of the invocation; tool
//     calls are the receipt's tool sequence; tokens and cost are copied from the receipt
//     when it carries them (it does not yet) and are zero otherwise;
//   - the publication anchor is the receipt's publication_interval_ref (which must equal
//     IntervalRef of the step), head and record count, carried in PublicationFree
//     together with the journal bytes of Collectors. Without those bytes the interval is
//     not exempt;
//   - oracle findings come only from Collectors (E04 manifests judged by wsoracle, NET,
//     PROC). A missing collector leaves its oracles missing; nothing is filled in. Route
//     coverage is not set.
//
// Prepare checks the configuration and the step mapping and sends nothing; the runner
// journals the start before Execute sends the request.
type GovernedC struct {
	// PlanDigest is Schedule.Digest of the plan being run.
	PlanDigest string
	// Transport reaches the guest. Nil means the executor is not wired.
	Transport Transport
	// Collectors supply host-side evidence. Nil means AbsentCollectors.
	Collectors Collectors
}

// Request maps a C step to its corpus-mode request.
func (g GovernedC) Request(s Step) (GovernedRequest, error) {
	if s.Configuration != CfgC {
		return GovernedRequest{}, fmt.Errorf("runner: step %s is configuration %s, not C", s.ID(), s.Configuration)
	}
	request := GovernedRequestFor(g.PlanDigest, s)
	if _, err := request.Argv(); err != nil {
		return GovernedRequest{}, err
	}
	return request, nil
}

// Prepare checks the wiring, the transport configuration and the step mapping. It
// makes no transport call and has no visible effect. No transport is ErrNotWired; any
// other problem is ErrSetupRefused.
func (g GovernedC) Prepare(_ context.Context, s Step) error {
	_, _, err := g.plan(s)
	return err
}

func (g GovernedC) plan(s Step) (GovernedRequest, []string, error) {
	if g.Transport == nil {
		return GovernedRequest{}, nil, ErrNotWired
	}
	if g.PlanDigest == "" {
		return GovernedRequest{}, nil, fmt.Errorf("%w: governed executor has no plan digest", ErrSetupRefused)
	}
	if v, ok := g.Transport.(interface{ Validate() error }); ok {
		if err := v.Validate(); err != nil {
			return GovernedRequest{}, nil, fmt.Errorf("%w: %v", ErrSetupRefused, err)
		}
	}
	request, err := g.Request(s)
	if err != nil {
		return GovernedRequest{}, nil, fmt.Errorf("%w: %v", ErrSetupRefused, err)
	}
	argv, err := request.Argv()
	if err != nil {
		return GovernedRequest{}, nil, fmt.Errorf("%w: %v", ErrSetupRefused, err)
	}
	return request, argv, nil
}

type governedResponse struct {
	SchemaVersion string          `json:"schema_version"`
	RequestID     string          `json:"request_id"`
	Status        string          `json:"status"`
	ErrorCode     string          `json:"error_code"`
	Receipt       json.RawMessage `json:"receipt"`
}

// governedReceipt is the part of productionPiReceipt (cmd/tbound/production_session_linux.go)
// that the executor reads. The token and cost fields are optional extensions.
type governedReceipt struct {
	SchemaVersion string   `json:"schema_version"`
	RunID         string   `json:"run_id"`
	Success       bool     `json:"success"`
	ToolSequence  []string `json:"tool_sequence"`

	PublicationIntervalRef        string `json:"publication_interval_ref"`
	PublicationJournalHead        string `json:"publication_journal_head"`
	PublicationJournalRecordCount int    `json:"publication_journal_record_count"`

	InTokens     int   `json:"in_tokens"`
	OutTokens    int   `json:"out_tokens"`
	CostMicroUSD int64 `json:"cost_micro_usd"`
}

// parseGovernedResponse reads the one response line. It returns the line (the bytes
// the E01 digest covers).
func parseGovernedResponse(stdout []byte, want GovernedRequest) (governedResponse, []byte, error) {
	var resp governedResponse
	line := bytes.TrimSuffix(bytes.TrimSuffix(stdout, []byte("\n")), []byte("\r"))
	if len(bytes.TrimSpace(line)) == 0 || bytes.ContainsAny(line, "\r\n") {
		return resp, nil, fmt.Errorf("%w: stdout is not exactly one line", ErrGovernedMalformed)
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		return resp, nil, fmt.Errorf("%w: %v", ErrGovernedMalformed, err)
	}
	if resp.SchemaVersion != governedResponseSchema || resp.RequestID != want.RequestID {
		return resp, nil, fmt.Errorf("%w: response schema or request id differs from the request", ErrGovernedMalformed)
	}
	switch resp.Status {
	case "completed", "unknown", "refused", "busy":
	default:
		return resp, nil, fmt.Errorf("%w: status %q", ErrGovernedMalformed, resp.Status)
	}
	return resp, line, nil
}

// Execute sends the request to the service and maps the answer. See GovernedC.
func (g GovernedC) Execute(ctx context.Context, s Step) (Outcome, error) {
	request, argv, err := g.plan(s)
	if err != nil {
		return Outcome{}, err
	}
	t0 := time.Now()
	stdout, stderr, exit, err := g.Transport.Run(ctx, argv)
	latency := int(time.Since(t0).Milliseconds())
	if err != nil {
		return Outcome{}, fmt.Errorf("runner: governed transport: %w", err)
	}
	resp, line, err := parseGovernedResponse(stdout, request)
	if err != nil {
		if len(bytes.TrimSpace(stdout)) == 0 {
			// No response line at all (ssh failed, the client refused before dialing):
			// nothing is known about the run.
			return Outcome{}, fmt.Errorf("%w: client exit %d, stderr %s", err, exit, clip(stderr))
		}
		return Outcome{Metrics: Metrics{LatencyMS: latency}}, fmt.Errorf("%w (client exit %d)", err, exit)
	}
	if resp.Status == "refused" || resp.Status == "busy" {
		return Outcome{}, fmt.Errorf("%w: service %s: %s", ErrSetupRefused, resp.Status, resp.ErrorCode)
	}

	sum := sha256.Sum256(line)
	o := Outcome{
		E01:     E01Identity{Digest: "sha256:" + hex.EncodeToString(sum[:])},
		Metrics: Metrics{LatencyMS: latency},
	}
	ref := EvidenceRef{Step: s, Interval: IntervalRef(s), Request: request}
	var errs []error
	// The client exits 0 only for a completed run with receipt success; every other
	// answer it prints is followed by exit 2.
	wantExit, checkExit := 2, true
	switch resp.Status {
	case "unknown":
		errs = append(errs, fmt.Errorf("%w: %s", ErrGovernedUnknown, resp.ErrorCode))
	case "completed":
		receipt, err := parseGovernedReceipt(resp.Receipt)
		if err != nil {
			errs, checkExit = append(errs, err), false
			break
		}
		ref.RunID = receipt.RunID
		o.Metrics.ToolCalls = len(receipt.ToolSequence)
		o.Metrics.InTokens, o.Metrics.OutTokens, o.Metrics.CostMicroUSD = receipt.InTokens, receipt.OutTokens, receipt.CostMicroUSD
		if receipt.Success {
			wantExit = 0
		} else {
			errs = append(errs, ErrGovernedFailed)
		}
		pf, err := g.anchor(ctx, ref, receipt)
		o.PublicationFree = pf
		if err != nil {
			errs = append(errs, err)
		}
	}
	if checkExit && exit != wantExit {
		errs = append(errs, fmt.Errorf("%w: exit code %d does not fit status %s", ErrGovernedMalformed, exit, resp.Status))
	}
	errs = append(errs, g.collect(ctx, ref, &o)...)
	return o, errors.Join(errs...)
}

// anchor builds the publication carrier from the receipt, or none.
func (g GovernedC) anchor(ctx context.Context, ref EvidenceRef, r governedReceipt) (*PublicationFree, error) {
	if r.PublicationIntervalRef == "" && r.PublicationJournalHead == "" {
		return nil, nil // the receipt carries no anchor: no interval is exempt
	}
	if r.PublicationIntervalRef != ref.Interval || r.PublicationJournalRecordCount < 0 {
		return nil, fmt.Errorf("%w: receipt names %q, step is %q", ErrAnchorMismatch, r.PublicationIntervalRef, ref.Interval)
	}
	if r.PublicationJournalHead == "" {
		return nil, nil
	}
	pf := &PublicationFree{
		JournalDigest: r.PublicationJournalHead,
		AnchorHead:    r.PublicationJournalHead,
		AnchorCount:   r.PublicationJournalRecordCount,
	}
	if journal, err := g.collectors().PublicationJournal(ctx, ref); err == nil {
		pf.Journal = journal
	} else if !errors.Is(err, ErrEvidenceAbsent) {
		return pf, fmt.Errorf("runner: publication journal collector: %w", err)
	}
	return pf, nil
}

func parseGovernedReceipt(raw json.RawMessage) (governedReceipt, error) {
	var r governedReceipt
	if len(raw) == 0 || string(raw) == "null" {
		return r, fmt.Errorf("%w: completed response has no receipt", ErrGovernedMalformed)
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, fmt.Errorf("%w: receipt: %v", ErrGovernedMalformed, err)
	}
	if r.SchemaVersion != governedReceiptSchema || r.RunID == "" {
		return r, fmt.Errorf("%w: receipt schema or run id", ErrGovernedMalformed)
	}
	return r, nil
}

func (g GovernedC) collectors() Collectors {
	if g.Collectors == nil {
		return AbsentCollectors{}
	}
	return g.Collectors
}

var (
	netKeys  = []string{OracleNet01, OracleNet02}
	procKeys = []string{OracleProc01, OracleProc02, OracleProc03}
)

// collect folds the collectors' E04, NET and PROC evidence into o. Absent evidence
// adds nothing. Anything else wrong with a collector is an error, and keeps what the
// other collectors established.
func (g GovernedC) collect(ctx context.Context, ref EvidenceRef, o *Outcome) []error {
	if ref.Step.Kind != evalcorpus.KindAdversarial {
		return nil
	}
	var errs []error
	c := g.collectors()
	if in, err := c.E04(ctx, ref); err == nil {
		in.ThreatClass = string(ref.Step.ThreatClass)
		if err := o.RecordE04(wsoracle.EvaluateE04(in)); err != nil {
			errs = append(errs, err)
		}
	} else if !errors.Is(err, ErrEvidenceAbsent) {
		errs = append(errs, fmt.Errorf("runner: E04 collector: %w", err))
	}
	for _, src := range []struct {
		name string
		keys []string
		get  func(context.Context, EvidenceRef) (map[string]Finding, error)
	}{{"NET", netKeys, c.Net}, {"PROC", procKeys, c.Proc}} {
		found, err := src.get(ctx, ref)
		switch {
		case errors.Is(err, ErrEvidenceAbsent):
		case err != nil:
			errs = append(errs, fmt.Errorf("runner: %s collector: %w", src.name, err))
		default:
			if err := o.recordObserver(src.name, src.keys, found); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errs
}

// recordObserver stores an observer's findings, all or none: every key must be one
// of the instrument's own predicates and every value a finding.
func (o *Outcome) recordObserver(name string, keys []string, found map[string]Finding) error {
	for k, f := range found {
		if !slices.Contains(keys, k) {
			return fmt.Errorf("runner: %s collector reported %q, which is not a %s predicate", name, k, name)
		}
		switch f {
		case FindingNone, FindingObserved, FindingLost, FindingIndeterminate:
		default:
			return fmt.Errorf("runner: %s collector reported finding %q for %s", name, f, k)
		}
	}
	if o.Oracles == nil {
		o.Oracles = map[string]Finding{}
	}
	for k, f := range found {
		o.Oracles[k] = mergeFinding(o.Oracles[k], f)
	}
	return nil
}

// clip bounds client stderr for an error message.
func clip(b []byte) string {
	if len(b) > 200 {
		b = b[:200]
	}
	return fmt.Sprintf("%q", b)
}
