package netoracle

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/runner"
)

// RetainedSchema labels the file a Collector with a RetainDir writes for each interval.
const RetainedSchema = "tbound-net-interval/v1"

// Retained is the original evidence of one NET interval: the start and end canary documents and the
// sink journal document exactly as the evidence channel answered them, kept at capture time because the
// tag that names them is drawn per interval and is not recorded anywhere else. A reader recomputes the
// EFF-NET-01 report from the journal and compares its digest with the one the runner journaled.
type Retained struct {
	Schema   string               `json:"schema"`
	Interval string               `json:"interval"`
	Start    runner.NetCanaryDoc  `json:"start"`
	End      runner.NetCanaryDoc  `json:"end"`
	Journal  runner.NetJournalDoc `json:"journal"`
	// Closure is the D49 closure verdict the end canary waited on (nil without a gate).
	Closure *runner.IntervalClosure `json:"closure,omitempty"`
}

// RetainedName is the file name of the interval ref (runner.IntervalRef) in a RetainDir. The ref holds
// characters that are not portable in a file name, so the name is its digest.
func RetainedName(interval string) string {
	sum := sha256.Sum256([]byte(interval))
	return hex.EncodeToString(sum[:]) + ".json"
}

// ParseRetained decodes one retained interval file strictly: no unknown member, no trailing data.
func ParseRetained(raw []byte) (Retained, error) {
	var r Retained
	if err := protocol.ValidateEvidenceJSON(raw); err != nil {
		return Retained{}, err
	}
	if err := runner.DecodeEvidenceDoc(raw, &r); err != nil {
		return Retained{}, err
	}
	if r.Schema != RetainedSchema {
		return Retained{}, fmt.Errorf("retained net interval: schema %q, want %q", r.Schema, RetainedSchema)
	}
	return r, nil
}

// retain writes r into dir as a new file. An existing file is refused: an interval is evaluated once.
func retain(dir string, r Retained) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, RetainedName(r.Interval)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(raw, '\n'))
	serr := f.Sync()
	return errors.Join(werr, serr, f.Close())
}

// VerifyInterval checks the documents of one NET interval against each other and recomputes its
// EFF-NET-01 report from the sink journal: the journal must verify as a hash chain that ends on the head and
// count the guest named, and hold the very start and end canaries that were acknowledged, at their positions.
// The collector applies it when it captures an interval, and a reader applies it to a Retained one; the
// caller compares the report's digest with the one the runner journaled.
func VerifyInterval(interval string, start, end runner.NetCanaryDoc, doc runner.NetJournalDoc) (Report, error) {
	tag := doc.Interval
	for _, c := range []struct {
		doc   runner.NetCanaryDoc
		phase string
	}{{start, PhaseStart}, {end, PhaseEnd}} {
		d := c.doc
		if d.Schema != runner.EvidenceSchema || d.Kind != runner.EvidenceNetCanary || d.Phase != c.phase || d.Interval != tag ||
			d.Seq == 0 || !hashRE.MatchString(d.Hash) {
			return Report{}, fmt.Errorf("the %s canary document does not answer the request", c.phase)
		}
	}
	journal, err := base64.StdEncoding.DecodeString(doc.JournalB64)
	if doc.Schema != runner.EvidenceSchema || doc.Kind != runner.EvidenceNetJournal || !tagRE.MatchString(tag) || err != nil || len(journal) == 0 {
		return Report{}, errors.New("the journal document does not answer the request")
	}
	trace, err := audit.Verify(bytes.NewReader(journal))
	if err != nil || len(trace.Records) == 0 || len(trace.Records) != doc.Count || trace.Records[len(trace.Records)-1].Hash != doc.Head {
		return Report{}, errors.New("the journal does not verify against the head the guest named")
	}
	rep := EvaluateNET01(NET01Input{Journal: journal, Interval: interval, Tag: tag})
	switch {
	case rep.StartSeq != start.Seq || rep.EndSeq != end.Seq || doc.StartSeq != start.Seq || doc.EndSeq != end.Seq:
		return Report{}, errors.New("the journal does not hold the acknowledged canaries at their positions")
	case int(end.Seq) != len(trace.Records) || trace.Records[start.Seq-1].Hash != start.Hash || doc.Head != end.Hash:
		return Report{}, errors.New("the acknowledged canary hashes are not in the journal")
	}
	return rep, nil
}
