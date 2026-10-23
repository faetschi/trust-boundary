package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"tbound/supervisor/internal/evalscore"
)

// EntryKind is the type of a journal record.
type EntryKind string

const (
	EntryHeader EntryKind = "header"
	// EntryBegin is written, and synced, before an executor starts. From then on
	// the unit is spent whatever happens.
	EntryBegin EntryKind = "begin"
	// EntryStarted is written, and synced, by the runner after Prepare succeeds
	// and before it calls Execute. From then on the begin cannot be voided,
	// whatever the executor returns, and a crash leaves the step abandoned with
	// its unit spent. It does not end the step.
	EntryStarted EntryKind = "started"
	// EntryDone ends a begun step with an Outcome, or with a SetupFailure. An
	// Outcome with an ExecError is the partial outcome of a step that failed after
	// execution began.
	EntryDone EntryKind = "done"
	// EntryAbandoned ends a begun step that never finished: the process died or
	// the context ended mid-step. The unit stays spent and the step is not
	// retried. It has no outcome unless the executor returned a partial one with
	// its error; without one it scores as missing evidence.
	EntryAbandoned EntryKind = "abandoned"
	// EntryVoid cancels a begin whose executor turned out not to be wired, or
	// whose Prepare refused the step (ErrSetupRefused), so the runner never wrote
	// a started entry. The unit is not spent. A begun step with a started entry
	// cannot be voided.
	EntryVoid EntryKind = "void"
)

// Entry is one record of the raw journal. The journal holds everything the
// executors report, including the fields that evalscore does not take.
type Entry struct {
	Seq  int       `json:"seq"`
	Kind EntryKind `json:"kind"`

	// Header.
	PlanDigest   string `json:"plan_digest,omitempty"`
	PlannedUnits int    `json:"planned_units,omitempty"`
	// RunBinding is Schedule.RunBinding of the run that created the journal.
	RunBinding string `json:"run_binding,omitempty"`

	// Step records.
	StepID  string `json:"step_id,omitempty"`
	CaseID  string `json:"case_id,omitempty"`
	Index   int    `json:"index"`
	Attempt int    `json:"attempt,omitempty"`

	Outcome      *Outcome `json:"outcome,omitempty"`
	SetupFailure string   `json:"setup_failure,omitempty"`
	// ExecError is the error an executor returned together with Outcome: the step
	// failed after execution began and Outcome (marked Partial) is what it had
	// established.
	ExecError string `json:"exec_error,omitempty"`
	Reason    string `json:"reason,omitempty"`
	// ElapsedMS is the runner's own monotonic measurement around Execute.
	ElapsedMS int64 `json:"elapsed_ms,omitempty"`
}

type stepKey struct {
	id      string
	attempt int
}

type keyState struct {
	begun    bool // a begin that has not been voided
	started  bool // the executor recorded the start of execution
	terminal *Entry
}

// Journal is an append-only JSONL file. Every record is written and fsynced
// before the call returns. It is a single-writer handle.
type Journal struct {
	mu       sync.Mutex
	f        *os.File
	path     string
	sched    Schedule
	entries  []Entry
	state    map[stepKey]*keyState
	units    int // begins that were not voided
	planned  int // attempt-1 begins that were not voided
	rerun    map[string]stepKey
	poisoned error
}

// OpenJournal opens or creates the journal at path for the schedule s. The
// returned handle holds an exclusive nonblocking OS lock on the file until
// Close; a second open while it is live fails with ErrJournalLocked instead of
// reading, repairing or appending behind the first writer. A new file gets a
// header. An existing file is replayed under the lock: it must belong to s
// (ErrPlanMismatch otherwise) and must be consistent (ErrJournalCorrupt). A
// final line cut short by a crash is dropped and the file truncated to the last
// complete record.
func OpenJournal(path string, s Schedule) (*Journal, error) {
	if s.Digest == "" {
		return nil, errors.New("runner: schedule has no digest")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("runner: journal directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("runner: open journal: %w", err)
	}
	// The lock is taken before the file is read: replay, torn-tail repair and
	// every append below are exclusive to this handle. Until the handle is
	// returned, every error path closes it (which releases the lock).
	keepOpen := false
	defer func() {
		if !keepOpen {
			_ = f.Close()
		}
	}()
	if err := lockJournal(f); err != nil {
		return nil, fmt.Errorf("runner: open journal %s: %w", path, err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("runner: read journal: %w", err)
	}
	j := &Journal{path: path, sched: s, state: map[stepKey]*keyState{}, rerun: map[string]stepKey{}}

	keep, needNL := 0, false // bytes of valid prefix; whether its last record lacks a newline
	for off := 0; off < len(data); {
		line, next, complete := data[off:], len(data), true
		if nl := bytes.IndexByte(data[off:], '\n'); nl >= 0 {
			line, next = data[off:off+nl], off+nl+1
		} else {
			complete = false
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			if !complete { // a torn tail: nothing follows it
				break
			}
			return nil, fmt.Errorf("%w: record %d: %v", ErrJournalCorrupt, len(j.entries), err)
		}
		if err := j.apply(e); err != nil {
			return nil, err
		}
		off, keep, needNL = next, next, !complete
	}

	j.f = f
	if keep != len(data) { // drop a torn record
		if err := f.Truncate(int64(keep)); err != nil {
			return nil, fmt.Errorf("runner: repair journal: %w", err)
		}
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return nil, fmt.Errorf("runner: repair journal: %w", err)
	}
	if needNL {
		if _, err := f.Write([]byte{'\n'}); err != nil {
			return nil, fmt.Errorf("runner: repair journal: %w", err)
		}
	}
	if keep != len(data) || needNL {
		if err := f.Sync(); err != nil {
			return nil, fmt.Errorf("runner: repair journal: %w", err)
		}
	}
	if len(j.entries) == 0 {
		if err := j.append(Entry{Kind: EntryHeader, PlanDigest: s.Digest, PlannedUnits: s.Units(), RunBinding: s.RunBinding}); err != nil {
			return nil, err
		}
		syncDir(filepath.Dir(path))
	}
	keepOpen = true
	return j, nil
}

// syncDir makes the creation of the journal file durable where the platform
// allows it. Failure is ignored: not every platform can sync a directory.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// Close releases the exclusive lock and closes the file. Every record is
// already durable.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return nil
	}
	f := j.f
	j.f = nil
	unlockErr := unlockJournal(f)
	closeErr := f.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

// Entries returns a copy of the records, in order.
func (j *Journal) Entries() []Entry {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]Entry(nil), j.entries...)
}

// UnitsUsed is the number of units spent: begun steps and reruns, not voided.
func (j *Journal) UnitsUsed() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.units
}

func (j *Journal) plannedBegun() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.planned
}

// terminalOf returns the terminal record of a step attempt, if it has one.
func (j *Journal) terminalOf(id string, attempt int) *Entry {
	j.mu.Lock()
	defer j.mu.Unlock()
	if st := j.state[stepKey{id, attempt}]; st != nil {
		return st.terminal
	}
	return nil
}

// begunOf is true when the step attempt has a begin that was not voided.
func (j *Journal) begunOf(id string, attempt int) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	st := j.state[stepKey{id, attempt}]
	return st != nil && st.begun
}

// startedOf is true when the step attempt has a started entry.
func (j *Journal) startedOf(id string, attempt int) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	st := j.state[stepKey{id, attempt}]
	return st != nil && st.started
}

// rerunOf returns the step of the case that was rerun, if any.
func (j *Journal) rerunOf(caseID string) (stepKey, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	k, ok := j.rerun[caseID]
	return k, ok
}

// append validates, writes and syncs one record.
func (j *Journal) append(e Entry) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.poisoned != nil {
		return j.poisoned
	}
	if j.f == nil {
		return errors.New("runner: journal is closed")
	}
	e.Seq = len(j.entries)
	if err := j.check(e); err != nil {
		return err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("runner: encode journal record: %w", err)
	}
	b = append(b, '\n')
	if _, err := j.f.Write(b); err != nil {
		j.poisoned = fmt.Errorf("runner: journal write: %w", err)
		return j.poisoned
	}
	if err := j.f.Sync(); err != nil {
		j.poisoned = fmt.Errorf("runner: journal sync: %w", err)
		return j.poisoned
	}
	return j.commit(e)
}

// apply validates and applies a replayed record.
func (j *Journal) apply(e Entry) error {
	if e.Seq != len(j.entries) {
		return fmt.Errorf("%w: record %d has seq %d", ErrJournalCorrupt, len(j.entries), e.Seq)
	}
	if err := j.check(e); err != nil {
		return err
	}
	return j.commit(e)
}

func (j *Journal) corrupt(format string, args ...any) error {
	return fmt.Errorf("%w: record %d: %s", ErrJournalCorrupt, len(j.entries), fmt.Sprintf(format, args...))
}

// check validates e against the current state without changing it.
func (j *Journal) check(e Entry) error {
	if len(j.entries) == 0 {
		if e.Kind != EntryHeader {
			return j.corrupt("first record is %q, want header", e.Kind)
		}
		if e.PlanDigest != j.sched.Digest {
			return fmt.Errorf("%w: journal %s, plan %s", ErrPlanMismatch, e.PlanDigest, j.sched.Digest)
		}
		if e.PlannedUnits != j.sched.Units() {
			return fmt.Errorf("%w: journal plans %d units, plan has %d", ErrPlanMismatch, e.PlannedUnits, j.sched.Units())
		}
		if e.RunBinding != j.sched.RunBinding {
			return fmt.Errorf("%w: journal run binding %q, run binding %q", ErrPlanMismatch, e.RunBinding, j.sched.RunBinding)
		}
		return nil
	}
	if e.Kind == EntryHeader {
		return j.corrupt("second header")
	}
	step, ok := j.sched.Lookup(e.StepID)
	if !ok {
		return j.corrupt("unknown step %q", e.StepID)
	}
	if e.Attempt != 1 && e.Attempt != 2 {
		return j.corrupt("attempt %d", e.Attempt)
	}
	if e.Index != step.Index || (e.CaseID != "" && e.CaseID != step.CaseID) {
		return j.corrupt("step %q identity does not match the plan", e.StepID)
	}
	k := stepKey{e.StepID, e.Attempt}
	st := j.state[k]
	switch e.Kind {
	case EntryBegin:
		if st != nil && (st.begun || st.terminal != nil) {
			return j.corrupt("step %q attempt %d begun twice", e.StepID, e.Attempt)
		}
		if e.Attempt == 2 {
			orig := j.state[stepKey{e.StepID, 1}]
			if orig == nil || orig.terminal == nil || !outageEvidenced(orig.terminal.Outcome) {
				return j.corrupt("rerun of %q without an evidenced outage in the original", e.StepID)
			}
			if prev, dup := j.rerun[rerunCase(step)]; dup {
				return j.corrupt("case %q has a second rerun (%q after %q)", step.CaseID, e.StepID, prev.id)
			}
		}
	case EntryStarted:
		if st == nil || !st.begun || st.terminal != nil {
			return j.corrupt("started for %q attempt %d without an open begin", e.StepID, e.Attempt)
		}
		if st.started {
			return j.corrupt("%q attempt %d started twice", e.StepID, e.Attempt)
		}
		if e.Outcome != nil || e.SetupFailure != "" || e.ExecError != "" {
			return j.corrupt("started record of %q carries a result", e.StepID)
		}
	case EntryDone, EntryAbandoned, EntryVoid:
		if st == nil || !st.begun {
			return j.corrupt("%s for %q attempt %d without a begin", e.Kind, e.StepID, e.Attempt)
		}
		if st.terminal != nil {
			return j.corrupt("%q attempt %d already ended", e.StepID, e.Attempt)
		}
		if e.Kind == EntryDone && (e.Outcome == nil) == (e.SetupFailure == "") {
			return j.corrupt("done record of %q needs exactly one of outcome and setup_failure", e.StepID)
		}
		if e.Kind == EntryVoid && e.Outcome != nil {
			return j.corrupt("void record of %q carries an outcome", e.StepID)
		}
		if e.Kind == EntryVoid && st.started {
			return j.corrupt("void record of %q attempt %d after execution started", e.StepID, e.Attempt)
		}
		if e.ExecError != "" && (e.Kind != EntryDone || e.Outcome == nil) {
			return j.corrupt("exec_error of %q without a done outcome", e.StepID)
		}
	default:
		return j.corrupt("unknown kind %q", e.Kind)
	}
	return nil
}

// commit applies e, which check has accepted.
func (j *Journal) commit(e Entry) error {
	j.entries = append(j.entries, e)
	if e.Kind == EntryHeader {
		return nil
	}
	k := stepKey{e.StepID, e.Attempt}
	st := j.state[k]
	if st == nil {
		st = &keyState{}
		j.state[k] = st
	}
	switch e.Kind {
	case EntryBegin:
		st.begun = true
		j.units++
		if e.Attempt == 1 {
			j.planned++
		} else {
			step, _ := j.sched.Lookup(e.StepID)
			j.rerun[rerunCase(step)] = k
		}
	case EntryStarted:
		st.started = true
	case EntryVoid:
		st.begun = false
		j.units--
		if e.Attempt == 1 {
			j.planned--
		} else {
			step, _ := j.sched.Lookup(e.StepID)
			delete(j.rerun, rerunCase(step))
		}
	case EntryDone, EntryAbandoned:
		ec := e
		st.terminal = &ec
	}
	return nil
}

// outageEvidenced is the rerun condition: the original outcome reports an
// external outage and carries its evidence digest.
func outageEvidenced(o *Outcome) bool {
	return o != nil && o.EvidenceLoss == evalscore.LossExternalOutage && o.OutageEvidenceDigest != ""
}
