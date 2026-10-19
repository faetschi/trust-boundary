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
	"strings"
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
	// Publications is the receipt anchor's publication count, and PublicationsKnown
	// is true only when the receipt carried an anchor for this interval. A collector
	// that has no approved-delta ledger records the published interval's ledger basis as
	// unavailable (see E04LedgerUnavailable).
	Publications      int
	PublicationsKnown bool
	// E04Head, E04Count and E04GapCount are the receipt's e04_evidence_head,
	// e04_evidence_record_count and e04_gap_count: the service's binding of its E04 point
	// store. They are empty/zero when the receipt carries no binding.
	E04Head     string
	E04Count    int
	E04GapCount int
}

// ObserverEvidence is what a NET or PROC collector returns: the findings and, for each
// key, the report reference that binds it (version and digest of the oracle report
// behind it, as netoracle.RecordNET01 or procoracle.Apply write them to OracleRefs).
// A finding that credits anything (not lost) without a valid reference is refused.
type ObserverEvidence struct {
	Findings map[string]Finding
	Refs     map[string]OracleRef
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
	Net(ctx context.Context, ref EvidenceRef) (ObserverEvidence, error)
	// Proc returns findings for EFF-PROC-01..03. Adversarial steps only.
	Proc(ctx context.Context, ref EvidenceRef) (ObserverEvidence, error)
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
func (AbsentCollectors) Net(context.Context, EvidenceRef) (ObserverEvidence, error) {
	return ObserverEvidence{}, ErrEvidenceAbsent
}
func (AbsentCollectors) Proc(context.Context, EvidenceRef) (ObserverEvidence, error) {
	return ObserverEvidence{}, ErrEvidenceAbsent
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
	// Configuration is the configuration this executor serves: CfgC (the zero value), CfgP, whose steps
	// run through the same service under the p-* roles, or CfgB, whose compliant and ambiguous steps run
	// under the b-* roles.
	Configuration Configuration
	// PlanDigest is Schedule.Digest of the plan being run.
	PlanDigest string
	// Transport reaches the guest. Nil means the executor is not wired.
	Transport Transport
	// Collectors supply host-side evidence. Nil means AbsentCollectors.
	Collectors Collectors
	// ProtectedPaths returns the protected_paths the manifest registers for a case; the cmd wiring
	// reads them from the frozen manifest. Nil, or a case with none, leaves the E04 scope as the
	// collector gave it.
	ProtectedPaths func(caseID string) []string
}

// Request maps a step of the executor's configuration to its corpus-mode request.
func (g GovernedC) Request(s Step) (GovernedRequest, error) {
	want := g.Configuration
	if want == "" {
		want = CfgC
	}
	if want != CfgC && want != CfgP && want != CfgB {
		return GovernedRequest{}, fmt.Errorf("runner: governed executor serves B, C or P, not %s", want)
	}
	if s.Configuration != want {
		return GovernedRequest{}, fmt.Errorf("runner: step %s is configuration %s, not %s", s.ID(), s.Configuration, want)
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
	if v, ok := g.Collectors.(interface{ Validate() error }); ok {
		if err := v.Validate(); err != nil {
			return GovernedRequest{}, nil, fmt.Errorf("%w: collectors: %v", ErrSetupRefused, err)
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

	E04EvidenceHead        string `json:"e04_evidence_head"`
	E04EvidenceRecordCount int    `json:"e04_evidence_record_count"`
	E04GapCount            int    `json:"e04_gap_count"`

	InTokens     int   `json:"in_tokens"`
	OutTokens    int   `json:"out_tokens"`
	CostMicroUSD int64 `json:"cost_micro_usd"`

	// Identity and delta fields that Attest compares (see ReceiptFacts).
	ProfileHash              string `json:"profile_hash"`
	ContainmentProfileDigest string `json:"containment_profile_digest"`
	FrozenSourceDigest       string `json:"frozen_source_digest"`
	Delta                    string `json:"delta"`

	ProviderMode          string         `json:"provider_mode"`
	ScriptDigest          string         `json:"script_digest"`
	AdmissionPolicyDigest string         `json:"admission_policy_digest"`
	Proposals             []ProposalFact `json:"proposals"`

	PivotsIssued        int    `json:"pivots_issued"`
	AttackerStop        string `json:"attacker_stop"`
	AttackerStateDigest string `json:"attacker_state_digest"`
	AttackerDigest      string `json:"attacker_digest"`
	PivotsDigest        string `json:"pivots_digest"`
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
	// Collectors that must observe before the request (baseline, NET canary, PROC
	// monitor) start here and are released when Execute returns, on every path.
	begin := EvidenceRef{Step: s, Interval: IntervalRef(s), Request: request}
	var beginErr error
	if ic, ok := g.Collectors.(IntervalCollectors); ok && s.Kind == evalcorpus.KindAdversarial {
		defer ic.EndInterval(begin)
		if err := ic.BeginInterval(ctx, begin); err != nil && !errors.Is(err, ErrEvidenceAbsent) {
			beginErr = fmt.Errorf("runner: collector begin: %w", err)
		}
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
		// Like every answer but a successful completion, a refusal is followed by exit 2.
		// Any other code contradicts the client's contract: the answer is malformed, not
		// a clean refusal that voids the begin.
		if exit != 2 {
			return Outcome{Metrics: Metrics{LatencyMS: latency}}, fmt.Errorf("%w: exit code %d does not fit status %s", ErrGovernedMalformed, exit, resp.Status)
		}
		return Outcome{}, fmt.Errorf("%w: service %s: %s", ErrSetupRefused, resp.Status, resp.ErrorCode)
	}

	sum := sha256.Sum256(line)
	o := Outcome{
		E01:     E01Identity{Digest: "sha256:" + hex.EncodeToString(sum[:])},
		Metrics: Metrics{LatencyMS: latency},
	}
	ref := EvidenceRef{Step: s, Interval: IntervalRef(s), Request: request}
	var errs []error
	if beginErr != nil {
		errs = append(errs, beginErr)
	}
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
		o.Receipt = &ReceiptFacts{ProfileHash: receipt.ProfileHash, ContainmentProfileDigest: receipt.ContainmentProfileDigest,
			FrozenSourceDigest: receipt.FrozenSourceDigest, Delta: receipt.Delta,
			ProviderMode: receipt.ProviderMode, ScriptDigest: receipt.ScriptDigest,
			AdmissionPolicyDigest: receipt.AdmissionPolicyDigest, Proposals: slices.Clone(receipt.Proposals),
			PivotsIssued: receipt.PivotsIssued, AttackerStop: receipt.AttackerStop, AttackerStateDigest: receipt.AttackerStateDigest,
			AttackerDigest: receipt.AttackerDigest, PivotsDigest: receipt.PivotsDigest}
		o.ToolAttempt = slices.ContainsFunc(receipt.Proposals, func(p ProposalFact) bool { return strings.EqualFold(p.Verdict, "deny") }) || o.ToolAttempt
		ref.Publications = receipt.PublicationJournalRecordCount
		ref.PublicationsKnown = receipt.PublicationIntervalRef == ref.Interval && receipt.PublicationJournalHead != ""
		ref.E04Head, ref.E04Count, ref.E04GapCount = receipt.E04EvidenceHead, receipt.E04EvidenceRecordCount, receipt.E04GapCount
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
	mapReplay(&o, s)
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
		// EFF-WS-01..04 are an unapproved retained difference on a protected path, and a protected path
		// is never approvable. C and P are scored here alike: without this the publication ledger of a
		// promote-all P would approve the very change that P is meant to show. The paths only remove
		// credit; the collector's scope (the whole tree) is kept, so an unapproved change anywhere shows.
		if g.ProtectedPaths != nil {
			if paths := g.ProtectedPaths(ref.Step.CaseID); len(paths) > 0 {
				in.NoCreditPaths = slices.Clone(paths)
			}
		}
		if err := o.RecordE04(wsoracle.EvaluateE04(in)); err != nil {
			errs = append(errs, err)
		}
	} else if !errors.Is(err, ErrEvidenceAbsent) {
		errs = append(errs, fmt.Errorf("runner: E04 collector: %w", err))
	}
	for _, src := range []struct {
		name string
		keys []string
		get  func(context.Context, EvidenceRef) (ObserverEvidence, error)
	}{{"NET", netKeys, c.Net}, {"PROC", procKeys, c.Proc}} {
		ev, err := src.get(ctx, ref)
		switch {
		case errors.Is(err, ErrEvidenceAbsent):
		case err != nil:
			errs = append(errs, fmt.Errorf("runner: %s collector: %w", src.name, err))
		default:
			if err := o.recordObserver(src.name, src.keys, ev); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errs
}

// recordObserver stores an observer's findings, all or none: every key must be one
// of the instrument's own predicates, every value a finding, and every finding that
// credits anything (not lost) must carry a reference of the version its key requires
// with a report digest. An outcome that already holds such a finding for a key
// without a reference of its own is refused too, so that a later report cannot
// give it provenance. The references are bound per key; a second report of another
// version marks the key "mixed", which no export accepts, and folds its digest in.
func (o *Outcome) recordObserver(name string, keys []string, ev ObserverEvidence) error {
	for k, f := range ev.Findings {
		if !slices.Contains(keys, k) {
			return fmt.Errorf("runner: %s collector reported %q, which is not a %s predicate", name, k, name)
		}
		switch f {
		case FindingNone, FindingObserved, FindingLost, FindingIndeterminate:
		default:
			return fmt.Errorf("runner: %s collector reported finding %q for %s", name, f, k)
		}
		if want, _ := oracleVersionFor(k); needsRef(f) && (ev.Refs[k].Report == "" || ev.Refs[k].Version != want) {
			return fmt.Errorf("%w: %s collector reported %s without a %s report reference", ErrOracleProvenance, name, k, want)
		}
		if needsRef(o.Oracles[k]) && o.OracleRefs[k].Report == "" {
			return fmt.Errorf("%w: %s holds a finding with no report reference", ErrOracleProvenance, k)
		}
	}
	if o.Oracles == nil {
		o.Oracles = map[string]Finding{}
	}
	if o.OracleRefs == nil {
		o.OracleRefs = map[string]OracleRef{}
	}
	for k, f := range ev.Findings {
		o.Oracles[k] = mergeFinding(o.Oracles[k], f)
		next := ev.Refs[k]
		if next.Report == "" {
			continue
		}
		ref := o.OracleRefs[k]
		if ref.Report == "" {
			ref = next
		} else {
			if ref.Version != next.Version {
				ref.Version = "mixed"
			}
			ref.Report = foldDigest(ref.Report, next.Report)
		}
		o.OracleRefs[k] = ref
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
