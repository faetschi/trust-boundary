package runner

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/wsoracle"
)

// verifies reports whether p is the publication journal of the given interval and
// records no publication. It reads no claimed count. The journal must verify as a
// package-audit hash chain (audit.Verify), its recomputed head hash must be the
// claimed JournalDigest, its first record must be the IntervalMarkerKind record of
// this interval, no later record may open another interval, and no record may be
// a publication: any kind with the prefix "publication" other than the owner-epoch
// record that opening a repository writes. The count is the runner's own; a
// claimed count is not read.
//
// The journal bytes come from the executor, so the chain proves internal
// consistency only. The anchor ties it to the governed service: AnchorHead must
// equal the recomputed head and AnchorCount the counted publications. Without an
// anchor (AnchorHead empty) the interval is not exempt. A journal that was cut at a
// record boundary, relabeled or minted fails against the service's own head and
// count. See the Executor documentation for what the anchor does and does not
// establish.
func (p *PublicationFree) verifies(interval string) bool {
	if p == nil || interval == "" || len(p.Journal) == 0 || p.AnchorHead == "" {
		return false
	}
	trace, err := audit.Verify(bytes.NewReader(p.Journal))
	if err != nil || len(trace.Records) == 0 {
		return false
	}
	head := trace.Records[len(trace.Records)-1].Hash
	if p.JournalDigest != head || p.AnchorHead != head {
		return false
	}
	publications := 0
	for i, rec := range trace.Records {
		switch kind := rec.Event.Kind; {
		case i == 0:
			if kind != IntervalMarkerKind || rec.Event.ID != interval {
				return false
			}
		case kind == IntervalMarkerKind:
			return false
		case strings.HasPrefix(kind, "publication") && kind != "publication_owner_epoch":
			publications++
		}
	}
	return publications == 0 && p.AnchorCount == publications
}

// oracleKeys are the Outcome.Oracles keys that a wsoracle report supplies.
var oracleKeys = []string{OracleWS, OracleWS01, OracleWS02, OracleWS03, OracleWS04, OracleWS05, OracleWS06}

// hasWSFindings is true when o holds a finding under any key a wsoracle report supplies.
func (o *Outcome) hasWSFindings() bool {
	return slices.ContainsFunc(oracleKeys, func(k string) bool { return o.Oracles[k] != "" })
}

// oracleVersionOK is false for an outcome whose WS findings do not come from the
// current oracle version, or lack provenance of their own. Every WS finding needs a
// report reference (OracleRefs) of the current version with a report digest, and the
// outcome as a whole must name the current version and a report: another version,
// mixed versions, a missing reference or none at all beside WS findings is refused.
func oracleVersionOK(o *Outcome) bool {
	if o == nil {
		return true
	}
	if !o.hasWSFindings() {
		return o.OracleVersion == "" || o.OracleVersion == wsoracle.OracleVersion
	}
	if o.OracleVersion != wsoracle.OracleVersion || o.OracleReport == "" {
		return false
	}
	for _, k := range oracleKeys {
		if o.Oracles[k] == "" {
			continue
		}
		if ref := o.OracleRefs[k]; ref.Report == "" || ref.Version != wsoracle.OracleVersion {
			return false
		}
	}
	return true
}

// RecordE04 stores an E04 report's findings in o together with its provenance:
// the oracle version, and the report digest, which covers the identity digest of
// every manifest the report consumed (wsoracle.ManifestRef). Each finding it
// supplies is bound to this report in OracleRefs. It returns ErrOracleProvenance, and
// changes nothing, if the report has no digest or if o already holds a WS finding
// that has no report reference of its own.
func (o *Outcome) RecordE04(r wsoracle.E04Report) error {
	d, _ := r.Digest()
	return o.record(OraclesFromE04(r), r.OracleVersion, d)
}

// RecordWS stores a WS-05/06 report's findings in o together with its provenance,
// under the same rules as RecordE04.
func (o *Outcome) RecordWS(r wsoracle.Report) error {
	d, _ := r.Digest()
	return o.record(OraclesFromWS(r), r.OracleVersion, d)
}

// record merges findings (an observed effect always stands, otherwise the weaker
// evidence wins) and binds the provenance per finding. An outcome that already holds
// a WS finding without a report reference is refused, so that a legacy or unreferenced
// finding cannot pick up the provenance of a later report. A second report of another
// version leaves the finding, and the outcome, marked "mixed", which no export
// accepts; a second report folds its digest into the first.
func (o *Outcome) record(found map[string]Finding, version, report string) error {
	if report == "" {
		return fmt.Errorf("%w: the report has no digest", ErrOracleProvenance)
	}
	for _, k := range oracleKeys {
		if o.Oracles[k] != "" && o.OracleRefs[k].Report == "" {
			return fmt.Errorf("%w: %s holds a finding with no report reference", ErrOracleProvenance, k)
		}
	}
	if o.Oracles == nil {
		o.Oracles = map[string]Finding{}
	}
	if o.OracleRefs == nil {
		o.OracleRefs = map[string]OracleRef{}
	}
	for k, f := range found {
		o.Oracles[k] = mergeFinding(o.Oracles[k], f)
		ref := o.OracleRefs[k]
		if ref.Report == "" {
			ref = OracleRef{Version: version, Report: report}
		} else {
			if ref.Version != version {
				ref.Version = "mixed"
			}
			ref.Report = foldDigest(ref.Report, report)
		}
		o.OracleRefs[k] = ref
	}
	switch {
	case o.OracleReport == "" && o.OracleVersion == "":
		o.OracleVersion, o.OracleReport = version, report
	case o.OracleVersion != version:
		o.OracleVersion = "mixed"
		fallthrough
	default:
		o.OracleReport = foldDigest(o.OracleReport, report)
	}
	return nil
}

func foldDigest(a, b string) string {
	sum := sha256.Sum256([]byte(a + "\n" + b))
	return hex.EncodeToString(sum[:])
}

// findingRank orders results from the strongest evidence to the weakest.
func findingRank(f Finding) int {
	switch f {
	case FindingNone:
		return 1
	case FindingIndeterminate:
		return 2
	case FindingLost:
		return 3
	}
	return 0
}

func mergeFinding(old, new Finding) Finding {
	switch {
	case old == FindingObserved || new == FindingObserved:
		return FindingObserved
	case old == "":
		return new
	case findingRank(new) > findingRank(old):
		return new
	}
	return old
}
