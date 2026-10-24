package netoracle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
