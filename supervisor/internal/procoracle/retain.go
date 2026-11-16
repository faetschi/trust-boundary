package procoracle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/runner"
)

// InputsSchema and InputsSuffix name the retained evaluation inputs of an interval, written beside its termination record.
const (
	InputsSchema = "tbound-proc-interval/v1"
	InputsSuffix = ".proc-interval.json"
)

// Inputs is everything the collector passed to Evaluate for one interval. With the interval's termination record it
// lets a reader recompute the bound PROC reference the runner journaled (VerifyRetained).
type Inputs struct {
	Schema       string       `json:"schema"`
	Interval     string       `json:"interval"`
	Registration Registration `json:"registration"`
	Snapshots    []Snapshot   `json:"snapshots"`
	Polls        []Snapshot   `json:"polls"`
	Policy       Policy       `json:"policy"`
}

// InputsName is the file name of an interval's retained inputs.
func InputsName(interval string) string {
	sum := sha256.Sum256([]byte(interval))
	return hex.EncodeToString(sum[:]) + InputsSuffix
}

// ParseInputs decodes one retained inputs file strictly: no duplicate or unknown member, no trailing data.
func ParseInputs(raw []byte) (Inputs, error) {
	var in Inputs
	if err := protocol.ValidateEvidenceJSON(raw); err != nil {
		return Inputs{}, err
	}
	if err := runner.DecodeEvidenceDoc(raw, &in); err != nil {
		return Inputs{}, err
	}
	if in.Schema != InputsSchema {
		return Inputs{}, fmt.Errorf("retained proc inputs: schema %q, want %q", in.Schema, InputsSchema)
	}
	return in, nil
}

// Expected is what the run configuration and the case fix about an interval, independently of what was retained.
type Expected struct {
	CellUID, CellGID  uint32
	TeardownGrace     time.Duration
	AuthorizedSockets []string
	// LingeringInit is true only for the P half of a no-reap/v1 case.
	LingeringInit bool
}

// VerifyRetained recomputes the PROC report of an interval from its retained inputs and returns it with the reference
// digest the collector bound to the termination record (BindDigest), for the caller to compare with the journal. It
// refuses inputs that disagree with the termination record or with want, a registration that is invalid or that a
// snapshot or poll was not captured under, and a cell-identity flag that the user-namespace chains of the same capture contradict.
func VerifyRetained(in Inputs, term Termination, want Expected) (Report, string, error) {
	reg, p := in.Registration, in.Policy
	switch {
	case term.Registration != reg.Digest():
		return Report{}, "", errors.New("the termination record names another registration digest")
	case reg.CellUID != want.CellUID || reg.CellGID != want.CellGID || reg.MaxGap != GuestMaxGap:
		return Report{}, "", errors.New("the registration's cell identity or MaxGap is not the configured one")
	case !reg.Deadline.Equal(reg.IntervalStart.Add(want.TeardownGrace)):
		return Report{}, "", errors.New("the registered deadline is not the interval start plus the configured teardown grace")
	case p.LingeringInit != want.LingeringInit:
		return Report{}, "", fmt.Errorf("lingering_init is %t, but the case and configuration call for %t", p.LingeringInit, want.LingeringInit)
	case !slices.Equal(slices.Sorted(slices.Values(p.AuthorizedSockets)), slices.Sorted(slices.Values(want.AuthorizedSockets))):
		return Report{}, "", errors.New("the authorized sockets are not the configured ones")
	}
	for _, s := range slices.Concat(in.Snapshots, in.Polls, p.Extra, p.Tail) {
		if err := identityConsistent(s); err != nil {
			return Report{}, "", err
		}
	}
	rep := Evaluate(reg, in.Snapshots, in.Polls, p)
	for _, r := range rep.Results {
		if slices.ContainsFunc(r.Gaps, func(g Gap) bool { return g.Domain == DomainRegistration }) {
			return Report{}, "", fmt.Errorf("%s has a registration gap: the registration is invalid or a capture was not taken under it", r.Predicate)
		}
	}
	digest, err := rep.Digest()
	if err != nil {
		return Report{}, "", err
	}
	return rep, BindDigest(digest, term.Digest()), nil
}

// identityConsistent checks the cell-identity flags of one capture against its user-namespace chains (cellUserNS): a cell
// process is two or more levels below the scanner's namespace, and the top of its chain is a namespace of the service.
// Owners are not retained, but a namespace has one owner, so any process at that depth under a top the capture credited
// to the service must be flagged too. Inodes are compared within one capture only: the kernel reuses them.
func identityConsistent(s Snapshot) error {
	tops := map[uint64]bool{}
	for _, p := range s.Processes {
		if p.CellIdentity {
			if len(p.UserNS) < 2 {
				return fmt.Errorf("%s %d: process %d has the cell identity without a user namespace two levels deep", s.Point, s.Seq, p.PID)
			}
			tops[p.UserNS[len(p.UserNS)-1]] = true
		}
	}
	for _, p := range s.Processes {
		if !p.CellIdentity && len(p.UserNS) >= 2 && tops[p.UserNS[len(p.UserNS)-1]] {
			return fmt.Errorf("%s %d: process %d is in a cell user namespace but lacks the cell identity", s.Point, s.Seq, p.PID)
		}
	}
	return nil
}

// writeRetained writes v into dir as a new file. An existing file is refused: an interval is evaluated once.
func writeRetained(dir, name string, v any) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	werr := error(nil)
	if _, werr = f.Write(append(raw, '\n')); werr == nil {
		werr = f.Sync()
	}
	return errors.Join(werr, f.Close())
}
