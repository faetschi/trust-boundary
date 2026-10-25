package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/wsoracle"
)

const MaxWSRetainedBytes = 256 << 20

// RetainedWS holds the complete input actually passed to EvaluateE04, after host scope adjustments.
type RetainedWS struct {
	Schema   string                    `json:"schema"`
	Interval string                    `json:"interval"`
	Input    wsoracle.EvaluateE04Input `json:"input"`
}

func RetainedWSName(interval string) string {
	sum := sha256.Sum256([]byte(interval))
	return hex.EncodeToString(sum[:]) + ".json"
}

func RetainWS(dir, interval string, in wsoracle.EvaluateE04Input) error {
	raw, err := json.Marshal(RetainedWS{Schema: "tbound-ws-interval/v1", Interval: interval, Input: in})
	if err != nil {
		return err
	}
	if len(raw)+1 > MaxWSRetainedBytes {
		return errors.New("workspace evidence exceeds retention limit")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, RetainedWSName(interval)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(raw, '\n'))
	serr := f.Sync()
	return errors.Join(werr, serr, f.Close())
}

// VerifyWS recomputes the report and compares both its digest and finding with the journal.
func VerifyWS(raw []byte, interval, pred string, ref OracleRef, finding Finding) error {
	if len(raw) > MaxWSRetainedBytes {
		return errors.New("workspace evidence exceeds retention limit")
	}
	if err := protocol.ValidateEvidenceJSON(raw); err != nil {
		return err
	}
	var r RetainedWS
	if err := DecodeEvidenceDoc(raw, &r); err != nil {
		return err
	}
	if r.Schema != "tbound-ws-interval/v1" || r.Interval != interval {
		return errors.New("workspace interval identity mismatch")
	}
	rep := wsoracle.EvaluateE04(r.Input)
	digest, err := rep.Digest()
	if err != nil || ref.Version != wsoracle.OracleVersion || digest != ref.Report {
		return errors.New("workspace report digest differs from the journaled one")
	}
	if OraclesFromE04(rep)[pred] != finding {
		return errors.New("workspace input contradicts P oracle outcome")
	}
	return nil
}
