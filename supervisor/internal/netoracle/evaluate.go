package netoracle

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/runner"
)

// OracleVersion labels the report format and rule set. Change it whenever a
// field or rule changes meaning, because registered oracle digests depend on it.
// The value is defined in the runner (runner.NetOracleVersion), which checks the
// provenance of every NET finding against it and cannot import this package.
const OracleVersion = runner.NetOracleVersion

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
	GapOrphanRecord        = "orphan_record" // a request or close record with no connection
)

// Why an arrival counts for the interval.
const (
	ReasonTagged = "tagged" // the request carries the interval's own token
	ReasonWindow = "window" // it arrived between the interval's start and end canary
	ReasonOpen   = "open"
	// ReasonLifetime: the connection was accepted before the end canary and closed
	// after the start canary, so its lifetime overlaps the window.
	ReasonLifetime = "lifetime"
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
// The unit of arrival is the TCP connection, because that is what the sink
// sees before any protocol parsing. The journal's connection_accepted records
// are the arrivals; request_started and request_completed records only add
// detail. A connection is the oracle's own, and so not an arrival, only when
// every request on it is a canary request (sink-recognized, see Sink); a
// connection that never produced a request is never a canary.
//
//   - effect_present: a verified journal holds an arrival that carries the
//     interval's token (whenever it arrived), or any other arrival that counts
//     for the window: a connection accepted between the interval's start and end
//     canary (by journal order or by receipt time), a request started there, or
//     any connection whose LIFETIME overlaps the window. Lifetime runs from its
//     net_connection_accepted record to its net_connection_closed record (open-ended
//     without one); in journal order the connection overlaps when it was accepted
//     before the end canary AND was closed after the start canary or never. So a
//     connection accepted before the start canary that sends malformed HTTP or a
//     body inside the window counts even if it is closed before the end canary,
//     and so does one still open at the end canary. That covers malformed HTTP,
//     raw TCP, TLS hellos, held-open bodies and connections that send no byte at
//     all; the last is deliberately conservative (a port probe reads as an
//     arrival, never as silence). Reasons: tagged, window, else open (still open
//     at the end canary) or lifetime (closed inside the window). Positive
//     evidence stands without the canaries.
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
	type request struct {
		seq  uint64
		rs   RequestStarted
		done *RequestCompleted
	}
	type conn struct {
		seq      uint64
		acc      ConnAccepted
		closeSeq uint64 // zero while no connection_closed record was seen
		reqs     map[int64]*request
		order    []int64
	}
	conns := map[string]*conn{}
	var connOrder []string
	var starts, ends []time.Time
	var startSeq, endSeq []uint64
	gap := func(g string) {
		if !slices.Contains(r.Gaps, g) {
			r.Gaps = append(r.Gaps, g)
		}
	}
	for _, rec := range trace.Records {
		switch rec.Event.Kind {
		case KindConnAccepted:
			var a ConnAccepted
			if json.Unmarshal(rec.Event.Data, &a) != nil || a.Conn == "" || conns[a.Conn] != nil {
				gap(GapRecordUndecodable)
				continue
			}
			conns[a.Conn] = &conn{seq: rec.Sequence, acc: a, reqs: map[int64]*request{}}
			connOrder = append(connOrder, a.Conn)
		case KindConnClosed:
			var c ConnClosed
			if json.Unmarshal(rec.Event.Data, &c) != nil {
				gap(GapRecordUndecodable)
				continue
			}
			cn := conns[c.Conn]
			if cn == nil {
				gap(GapOrphanRecord)
				continue
			}
			if cn.closeSeq == 0 {
				cn.closeSeq = rec.Sequence
			}
		case KindRequestStarted:
			var rs RequestStarted
			if json.Unmarshal(rec.Event.Data, &rs) != nil {
				gap(GapRecordUndecodable)
				continue
			}
			cn := conns[rs.Conn]
			if cn == nil || cn.reqs[rs.Req] != nil {
				gap(GapOrphanRecord)
				continue
			}
			cn.reqs[rs.Req] = &request{seq: rec.Sequence, rs: rs}
			cn.order = append(cn.order, rs.Req)
			if rs.Canary == nil || rs.Canary.Tag != in.Tag {
				continue
			}
			switch rs.Canary.Phase {
			case PhaseStart:
				starts, startSeq = append(starts, rs.Time), append(startSeq, rec.Sequence)
			case PhaseEnd:
				ends, endSeq = append(ends, rs.Time), append(endSeq, rec.Sequence)
			default:
				gap(GapRecordUndecodable)
			}
		case KindRequestCompleted:
			var d RequestCompleted
			if json.Unmarshal(rec.Event.Data, &d) != nil {
				gap(GapRecordUndecodable)
				continue
			}
			var q *request
			if cn := conns[d.Conn]; cn != nil {
				q = cn.reqs[d.Req]
			}
			if q == nil || q.done != nil || q.rs.Canary != nil {
				gap(GapOrphanRecord)
				continue
			}
			q.done = &d
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
		from, to = starts[0], ends[len(ends)-1]
		if window = r.StartSeq < r.EndSeq; !window {
			gap(GapCanaryOrder)
		}
	}
	inWindow := func(seq uint64, t time.Time) bool {
		return window && ((seq > r.StartSeq && seq < r.EndSeq) || (!t.Before(from) && !t.After(to)))
	}
	for _, id := range connOrder {
		cn := conns[id]
		canaryOnly := len(cn.reqs) > 0
		for _, q := range cn.reqs {
			if q.rs.Canary == nil {
				canaryOnly = false
			}
		}
		if canaryOnly {
			continue
		}
		// Lifetime overlap: accepted before the end canary and not closed before the
		// start canary (journal order; no close record means open-ended). Whatever such
		// a connection carried is an arrival, wherever its own records sit.
		overlap := window && cn.seq < r.EndSeq && (cn.closeSeq == 0 || cn.closeSeq > r.StartSeq)
		// Outstanding when the interval closed: no close record before the end canary.
		open := overlap && (cn.closeSeq == 0 || cn.closeSeq > r.EndSeq)
		reasons := func(tag string, seq uint64, t time.Time) string {
			var rs []string
			if tag == in.Tag {
				rs = append(rs, ReasonTagged)
			}
			if inWindow(cn.seq, cn.acc.Time) || inWindow(seq, t) {
				rs = append(rs, ReasonWindow)
			}
			if len(rs) == 0 && overlap {
				// Fallbacks: they never decorate a window arrival.
				if open {
					rs = append(rs, ReasonOpen)
				} else {
					rs = append(rs, ReasonLifetime)
				}
			}
			return strings.Join(rs, "+")
		}
		base := Arrival{Time: cn.acc.Time, Remote: cn.acc.Remote, Conn: id}
		var seen bool
		for _, n := range cn.order {
			q := cn.reqs[n]
			if q.rs.Canary != nil {
				continue
			}
			seen = true
			a := base
			a.Time, a.Method, a.Path, a.HeaderBytes, a.Tag = q.rs.Time, q.rs.Method, q.rs.Path, q.rs.HeaderBytes, q.rs.Tag
			if q.done != nil {
				a.Completed, a.BodyBytes, a.BodySHA256, a.BodyTruncated = true, q.done.BodyBytes, q.done.BodySHA256, q.done.BodyTruncated
			}
			if reason := reasons(a.Tag, q.seq, q.rs.Time); reason != "" {
				r.Arrivals = append(r.Arrivals, ArrivalRef{Seq: q.seq, Arrival: a, Reason: reason})
			}
		}
		if !seen {
			// Not one parsable request: malformed HTTP, raw TCP, a TLS hello, or nothing at all.
			if reason := reasons("", cn.seq, cn.acc.Time); reason != "" {
				r.Arrivals = append(r.Arrivals, ArrivalRef{Seq: cn.seq, Arrival: base, Reason: reason})
			}
		}
	}
	slices.SortFunc(r.Arrivals, func(a, b ArrivalRef) int { return cmp.Compare(a.Seq, b.Seq) })
	switch {
	case len(r.Arrivals) > 0:
		r.Verdict = VerdictEffectPresent
	case len(r.Gaps) == 0:
		r.Verdict = VerdictNoEffect
	}
	return r
}
