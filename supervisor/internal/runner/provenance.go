package runner

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/wsoracle"
)

// verifies reports whether p is the publication journal of the given interval and
// records no publication. It trusts nothing in p. The journal must verify as a
// package-audit hash chain (audit.Verify), its recomputed head hash must be the
// claimed JournalDigest, its first record must be the IntervalMarkerKind record of
// this interval, no later record may open another interval, and no record may be
// a publication: any kind with the prefix "publication" other than the owner-epoch
// record that opening a repository writes. The count is the runner's own; a
// claimed count is not read.
func (p *PublicationFree) verifies(interval string) bool {
	if p == nil || interval == "" || len(p.Journal) == 0 {
		return false
	}
	trace, err := audit.Verify(bytes.NewReader(p.Journal))
	if err != nil || len(trace.Records) == 0 {
		return false
	}
	if p.JournalDigest != trace.Records[len(trace.Records)-1].Hash {
		return false
	}
	for i, rec := range trace.Records {
		switch kind := rec.Event.Kind; {
		case i == 0:
			if kind != IntervalMarkerKind || rec.Event.ID != interval {
				return false
			}
		case kind == IntervalMarkerKind:
			return false
		case strings.HasPrefix(kind, "publication") && kind != "publication_owner_epoch":
			return false
		}
	}
	return true
}

// oracleKeys are the Outcome.Oracles keys that a wsoracle report supplies.
var oracleKeys = []string{OracleWS, OracleWS01, OracleWS02, OracleWS03, OracleWS04, OracleWS05, OracleWS06}

// oracleVersionOK is false for an outcome whose WS findings do not come from the
// current oracle version: it names another version, mixed versions, or none at all
// beside WS findings.
func oracleVersionOK(o *Outcome) bool {
	if o == nil || o.OracleVersion == wsoracle.OracleVersion {
		return true
	}
	if o.OracleVersion != "" {
		return false
	}
	for _, k := range oracleKeys {
		if o.Oracles[k] != "" {
			return false
		}
	}
	return true
}

// RecordE04 stores an E04 report's findings in o together with its provenance:
// the oracle version, and the report digest, which covers the identity digest of
// every manifest the report consumed (wsoracle.ManifestRef).
func (o *Outcome) RecordE04(r wsoracle.E04Report) {
	d, _ := r.Digest()
	o.record(OraclesFromE04(r), r.OracleVersion, d)
}

// RecordWS stores a WS-05/06 report's findings in o together with its provenance.
func (o *Outcome) RecordWS(r wsoracle.Report) {
	d, _ := r.Digest()
	o.record(OraclesFromWS(r), r.OracleVersion, d)
}

// record merges findings (an observed effect always stands, otherwise the weaker
// evidence wins) and binds the provenance. A second report of another version
// leaves the outcome marked "mixed", which no export accepts; a second report
// folds its digest into the first.
func (o *Outcome) record(found map[string]Finding, version, report string) {
	if o.Oracles == nil {
		o.Oracles = map[string]Finding{}
	}
	for k, f := range found {
		o.Oracles[k] = mergeFinding(o.Oracles[k], f)
	}
	switch {
	case o.OracleReport == "" && o.OracleVersion == "":
		o.OracleVersion, o.OracleReport = version, report
	case o.OracleVersion != version:
		o.OracleVersion = "mixed"
		fallthrough
	default:
		sum := sha256.Sum256([]byte(o.OracleReport + "\n" + report))
		o.OracleReport = hex.EncodeToString(sum[:])
	}
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
