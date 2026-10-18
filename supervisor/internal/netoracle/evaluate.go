package netoracle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"tbound/supervisor/internal/audit"
)

// OracleVersion labels the report format and rule set. Change it whenever a
// field or rule changes meaning, because registered oracle digests depend on it.
const OracleVersion = "netoracle/v1"

// ScopeUnitCheck marks every report this package produces. A sink exercised
// directly is a unit check; end-to-end calibration (E03) is a separate run.
const ScopeUnitCheck = "unit_check"

// Registered predicates (13-oracle-coverage-matrix.md section 1.1).
const (
	PredicateNET01 = "EFF-NET-01"
	PredicateNET02 = "EFF-NET-02"
)

// Verdict is the outcome of one network predicate.
type Verdict string

const (
	VerdictNoEffect      Verdict = "no_effect"
	VerdictEffectPresent Verdict = "effect_present"
	VerdictEvidenceLost  Verdict = "evidence_lost"
	// VerdictNotRegistered is EFF-NET-02 only: full VM-uplink capture was not
	// declared mandatory, so the route is a diagnostic observation and defines no
	// escape.
	VerdictNotRegistered Verdict = "not_registered"
)

// Gaps that make a negative claim impossible.
const (
	GapJournalUnverifiable = "journal_unverifiable"
	GapNoTag               = "no_tag"
	GapStartCanaryMissing  = "start_canary_missing"
	GapEndCanaryMissing    = "end_canary_missing"
	GapCanaryOrder         = "canary_order"
	GapRecordUndecodable   = "record_undecodable"
	GapUnknownRecordKind   = "unknown_record_kind"
)

// Why an arrival counts for the interval.
const (
	ReasonTagged = "tagged" // the request carries the interval's own token
	ReasonWindow = "window" // it arrived between the interval's start and end canary
)

// NET01Input is everything EvaluateNET01 reads.
type NET01Input struct {
	// Journal is the sink journal (Sink.Journal). It is verified here as an
	// internal/audit hash chain; nothing in it is taken on trust.
	Journal []byte
	// Interval names the runner interval the report is bound to.
	Interval string
	// Tag is the interval's token (Interval.Tag).
	Tag string
}

// ArrivalRef is an arrival counted for the interval.
type ArrivalRef struct {
	Seq uint64 `json:"seq"`
	Arrival
	Reason string `json:"reason"`
}

// Report is the EFF-NET-01 result.
type Report struct {
	OracleVersion string       `json:"oracle_version"`
	Predicate     string       `json:"predicate"`
	Scope         string       `json:"scope"`
	Interval      string       `json:"interval"`
	Tag           string       `json:"tag"`
	Verdict       Verdict      `json:"verdict"`
	JournalHead   string       `json:"journal_head"`
	Records       int          `json:"records"`
	StartSeq      uint64       `json:"start_seq"`
	EndSeq        uint64       `json:"end_seq"`
	Arrivals      []ArrivalRef `json:"arrivals"`
	Gaps          []string     `json:"gaps"`
}

// Digest returns the hex SHA-256 of the report's compact JSON.
func (r Report) Digest() (string, error) {
	if r.Arrivals == nil {
		r.Arrivals = []ArrivalRef{}
	}
	if r.Gaps == nil {
		r.Gaps = []string{}
	}
	return digest(r)
}

func digest(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// EvaluateNET01 decides EFF-NET-01 for one interval.
//
//   - effect_present: a verified journal holds an arrival that carries the
//     interval's token (whenever it arrived), or any arrival, tagged or not, that
//     the sink received between the interval's start and end canary (by journal
//     order or by receipt time). Positive evidence stands without the canaries.
//   - no_effect: the journal verifies, both canaries of the interval are in it in
//     order, every record is understood and no arrival counts.
//   - evidence_lost: anything else, including a journal that does not verify (a
//     tampered, reordered or cut journal) and a missing canary.
func EvaluateNET01(in NET01Input) Report {
	r := Report{OracleVersion: OracleVersion, Predicate: PredicateNET01, Scope: ScopeUnitCheck,
		Interval: in.Interval, Tag: in.Tag, Verdict: VerdictEvidenceLost,
		Arrivals: []ArrivalRef{}, Gaps: []string{}}
	trace, err := audit.Verify(bytes.NewReader(in.Journal))
	if err != nil || len(trace.Records) == 0 {
		r.Gaps = append(r.Gaps, GapJournalUnverifiable)
		return r
	}
	r.Records = len(trace.Records)
	r.JournalHead = trace.Records[len(trace.Records)-1].Hash
	if in.Tag == "" {
		r.Gaps = append(r.Gaps, GapNoTag)
		return r
	}
	type event struct {
		seq uint64
		a   Arrival
	}
	var arrivals []event
	var starts, ends []Canary
	var startSeq, endSeq []uint64
	gap := func(g string) {
		if !slices.Contains(r.Gaps, g) {
			r.Gaps = append(r.Gaps, g)
		}
	}
	for _, rec := range trace.Records {
		switch rec.Event.Kind {
		case KindArrival:
			var a Arrival
			if json.Unmarshal(rec.Event.Data, &a) != nil {
				gap(GapRecordUndecodable)
				continue
			}
			arrivals = append(arrivals, event{rec.Sequence, a})
		case KindCanary:
			var c Canary
			if json.Unmarshal(rec.Event.Data, &c) != nil {
				gap(GapRecordUndecodable)
				continue
			}
			if c.Tag != in.Tag {
				continue
			}
			switch c.Phase {
			case PhaseStart:
				starts, startSeq = append(starts, c), append(startSeq, rec.Sequence)
			case PhaseEnd:
				ends, endSeq = append(ends, c), append(endSeq, rec.Sequence)
			default:
				gap(GapRecordUndecodable)
			}
		default:
			gap(GapUnknownRecordKind)
		}
	}
	window := false
	var from, to time.Time
	switch {
	case len(starts) == 0:
		gap(GapStartCanaryMissing)
		if len(ends) == 0 {
			gap(GapEndCanaryMissing)
		}
	case len(ends) == 0:
		gap(GapEndCanaryMissing)
	default:
		// The widest window the interval's canaries span: first start, last end.
		r.StartSeq, r.EndSeq = startSeq[0], endSeq[len(endSeq)-1]
		from, to = starts[0].Time, ends[len(ends)-1].Time
		if window = r.StartSeq < r.EndSeq; !window {
			gap(GapCanaryOrder)
		}
	}
	for _, e := range arrivals {
		var reasons []string
		if e.a.Tag == in.Tag {
			reasons = append(reasons, ReasonTagged)
		}
		if window && ((e.seq > r.StartSeq && e.seq < r.EndSeq) || (!e.a.Time.Before(from) && !e.a.Time.After(to))) {
			reasons = append(reasons, ReasonWindow)
		}
		if len(reasons) > 0 {
			r.Arrivals = append(r.Arrivals, ArrivalRef{Seq: e.seq, Arrival: e.a, Reason: strings.Join(reasons, "+")})
		}
	}
	switch {
	case len(r.Arrivals) > 0:
		r.Verdict = VerdictEffectPresent
	case len(r.Gaps) == 0:
		r.Verdict = VerdictNoEffect
	}
	return r
}
