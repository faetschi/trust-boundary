package effectoracle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// OracleVersion labels the report format and rule set. Change it whenever a
// field or rule changes meaning, because registered oracle digests depend on it.
const OracleVersion = "effectoracle/v1"

// Instrument names the event source in Report.Instrument.
const Instrument = "linux-inotify-recursive"

// Verdicts. Only VerdictOK supports a claim about transient mutations.
const (
	VerdictOK                    = "ok"
	VerdictInstrumentUnqualified = "instrument_unqualified"
	VerdictEvidenceLost          = "evidence_lost"
)

// Workspace event kinds. Each corresponds to one kernel event bit.
const (
	KindCreate     = "create"
	KindDelete     = "delete"
	KindModify     = "modify"
	KindCloseWrite = "close_write"
	KindMovedFrom  = "moved_from"
	KindMovedTo    = "moved_to"
	KindAttrib     = "attrib"
)

// Gap kinds. Each one is an explicit incompleteness record.
const (
	GapOverflow           = "overflow"            // IN_Q_OVERFLOW: the kernel dropped records
	GapReadError          = "read_error"          // read failed or a record was malformed; the reader stopped
	GapRootIdentity       = "root_identity"       // the root watch was deleted, moved, or unmounted
	GapMountBoundary      = "mount_boundary"      // a watched directory below the root was unmounted
	GapWatchLimit         = "watch_limit"         // inotify watch limit exhausted (ENOSPC)
	GapWatchError         = "watch_error"         // another failure to watch or enumerate a directory
	GapWatchAlias         = "watch_alias"         // one watched directory was reached through two paths
	GapSubdirRace         = "subdir_race"         // directory created or moved in before its watch existed
	GapUnmappedEvent      = "unmapped_event"      // an event for a watch the oracle does not map
	GapSnapshotConcurrent = "snapshot_concurrent" // workspace events overlapped a snapshot
	GapCanaryCleanup      = "canary_cleanup_failed"
	GapSnapshotUnreadable = "snapshot_unreadable"
	GapCanaryAnomaly      = "canary_anomaly"
	GapDirMovedOut        = "dir_moved_out"
)

// Transient candidate kinds.
const (
	TransientCreatedThenDeleted = "created_then_deleted"
	TransientReverted           = "reverted"
	TransientTouchedUnchanged   = "touched_unchanged"
)

// Reserved endpoint mark names. Start and Stop record them automatically.
const (
	MarkStart = "start"
	MarkStop  = "stop"
)

// PreservationFields names the report fields that must survive for an
// incompleteness claim to be well formed. Such a claim must cite a gap record
// with these fields, or a heartbeat with observed set to false.
var PreservationFields = []string{
	"gaps[].kind",
	"gaps[].path",
	"gaps[].from_seq",
	"gaps[].to_seq",
	"heartbeats[].observed",
}

var (
	// ErrUnsupported is returned on platforms other than Linux.
	ErrUnsupported = errors.New("effectoracle: inotify observation requires Linux")
	// ErrStopped is returned by Mark, and by Stop when no report exists, after Stop.
	ErrStopped = errors.New("effectoracle: observer is stopped")
	// ErrRootNotDirectory is returned when root is not a real directory.
	ErrRootNotDirectory = errors.New("effectoracle: root must be a real directory, not a symlink or a file")
	// ErrInvalidMarkName is returned for an empty name or a reserved endpoint name.
	ErrInvalidMarkName = errors.New("effectoracle: mark name must be non-empty and must not be start or stop")
)

// Report is the complete record of one observation window. Its JSON encoding
// is deterministic. Fields appear in declaration order, slices are ordered by
// sequence number or path, and no map is encoded.
type Report struct {
	OracleVersion string      `json:"oracle_version"`
	Instrument    string      `json:"instrument"`
	Masks         []string    `json:"masks"`
	Verdict       string      `json:"verdict"`
	Preservation  []string    `json:"preservation_fields"`
	Heartbeats    []Heartbeat `json:"heartbeats"`
	Gaps          []Gap       `json:"gaps"`
	Marks         []Mark      `json:"marks"`
	Events        []Event     `json:"events"`
}

// Event is one kernel-observed workspace mutation. Path is slash-separated and
// relative to the root. Seq is unique and increasing across all record kinds.
type Event struct {
	Seq   uint64 `json:"seq"`
	AtNS  int64  `json:"at_ns"`
	Kind  string `json:"kind"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
}

// Heartbeat records one canary attempt. Observed is false when the canary was
// not seen within the timeout, could not be written, or was not attempted
// because the reader had stopped. Detail is a short error class, never a path.
type Heartbeat struct {
	Seq      uint64 `json:"seq"`
	AtNS     int64  `json:"at_ns"`
	Label    string `json:"label"`
	Observed bool   `json:"observed"`
	Detail   string `json:"detail,omitempty"`
}

// Gap is an explicit incompleteness record. Path scopes it to a subtree. An
// empty Path means the whole window. The loss may lie between FromSeq and ToSeq.
type Gap struct {
	Seq     uint64 `json:"seq"`
	AtNS    int64  `json:"at_ns"`
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	FromSeq uint64 `json:"from_seq"`
	ToSeq   uint64 `json:"to_seq"`
	Detail  string `json:"detail,omitempty"`
}

// Mark is a named observation point with a content snapshot of the tree. The
// root itself is not included. Quiescent is false when a snapshot overlapped
// workspace events or an entry vanished during the walk.
type Mark struct {
	Seq       uint64  `json:"seq"`
	AtNS      int64   `json:"at_ns"`
	Name      string  `json:"name"`
	Quiescent bool    `json:"quiescent"`
	Entries   []Entry `json:"entries"`
}

// Entry describes one path in a snapshot. Size, SHA256, and Target are set
// only for the types they describe. Two entries are equal when type, mode,
// owner, group, link count, size, digest, and link target all match. Inode
// numbers are not recorded, so they take no part in equality.
type Entry struct {
	Path   string `json:"path"`
	Type   string `json:"type"`  // dir, file, symlink, other, or unreadable (a path whose state could not be read in full)
	Mode   uint32 `json:"mode"`  // permission, setuid, setgid, and sticky bits
	UID    uint32 `json:"uid"`   // owner, recorded as a number
	GID    uint32 `json:"gid"`   // group, recorded as a number
	Nlink  uint64 `json:"nlink"` // hard-link count; a directory's count follows its subdirectories
	Size   int64  `json:"size"`  // regular files only
	SHA256 string `json:"sha256,omitempty"`
	Target string `json:"target,omitempty"` // symlinks only
}

// Transient is one transient-mutation candidate. It is claim-bearing only when
// the report's Verdict is VerdictOK. EventSeqs lists the sequence numbers of the
// events that establish the candidate. ObservedAtMarks names the marks whose
// snapshots showed the path in a state that differs from the start snapshot.
type Transient struct {
	Kind            string   `json:"kind"`
	Path            string   `json:"path"`
	IsDir           bool     `json:"is_dir"`
	EventSeqs       []uint64 `json:"event_seqs"`
	ObservedAtMarks []string `json:"observed_at_marks"`
}

// TransientMutations returns the candidates in the window, ordered by first
// event sequence number and then by path. A created_then_deleted candidate
// pairs a creation (create or moved_to) with the next deletion (delete or
// moved_from) of the same path. A path that has recorded events and whose start
// and stop entries are equal is a reverted candidate only when a mark observed a
// state that differs from the start. Otherwise it is touched_unchanged: it was
// touched, but nothing in the window shows it changed, because a same-mode chmod
// or an identical rewrite leaves the fingerprint unchanged. The candidates come
// from the events and the marks alone. They are claims only when Verdict is
// VerdictOK.
func (r *Report) TransientMutations() []Transient {
	var out []Transient
	pending := make(map[string]Event)
	touched := make(map[string]bool)
	for _, e := range r.Events {
		touched[e.Path] = true
		switch e.Kind {
		case KindCreate, KindMovedTo:
			if _, open := pending[e.Path]; !open {
				pending[e.Path] = e
			}
		case KindDelete, KindMovedFrom:
			if c, open := pending[e.Path]; open {
				delete(pending, e.Path)
				out = append(out, Transient{
					Kind:            TransientCreatedThenDeleted,
					Path:            e.Path,
					IsDir:           c.IsDir,
					EventSeqs:       []uint64{c.Seq, e.Seq},
					ObservedAtMarks: r.marksBetween(e.Path, c.Seq, e.Seq),
				})
			}
		}
	}
	start, stop := r.mark(MarkStart), r.mark(MarkStop)
	if start != nil && stop != nil {
		startIdx, stopIdx := start.index(), stop.index()
		for p := range touched {
			a, inStart := startIdx[p]
			b, inStop := stopIdx[p]
			if !inStart || !inStop || a.fingerprint() != b.fingerprint() {
				continue
			}
			// Equal endpoints are a revert only when a mark observed a different
			// state. Otherwise the path was touched, but nothing shows it changed.
			kind := TransientTouchedUnchanged
			observed := r.marksDiffering(p, a)
			if len(observed) > 0 {
				kind = TransientReverted
			}
			out = append(out, Transient{
				Kind:            kind,
				Path:            p,
				IsDir:           a.Type == "dir",
				EventSeqs:       r.eventSeqsFor(p),
				ObservedAtMarks: observed,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].EventSeqs[0] != out[j].EventSeqs[0] {
			return out[i].EventSeqs[0] < out[j].EventSeqs[0]
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// JSON returns the report as indented JSON. The output includes the at_ns fields.
func (r *Report) JSON() ([]byte, error) {
	c := r.canonical(false)
	return json.MarshalIndent(&c, "", "  ")
}

// Digest returns the hex SHA-256 of the report's compact JSON with every at_ns
// field set to zero. Identical workloads therefore produce identical digests.
func (r *Report) Digest() (string, error) {
	c := r.canonical(true)
	b, err := json.Marshal(&c)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// canonical returns a copy in which empty lists are non-nil, so they encode as
// [] and not null. When dropTiming is set, every at_ns field is zeroed. The
// receiver is never modified.
func (r *Report) canonical(dropTiming bool) Report {
	c := *r
	c.Masks = nonNil(r.Masks)
	c.Preservation = nonNil(r.Preservation)
	c.Heartbeats = make([]Heartbeat, 0, len(r.Heartbeats))
	for _, h := range r.Heartbeats {
		if dropTiming {
			h.AtNS = 0
		}
		c.Heartbeats = append(c.Heartbeats, h)
	}
	c.Gaps = make([]Gap, 0, len(r.Gaps))
	for _, g := range r.Gaps {
		if dropTiming {
			g.AtNS = 0
		}
		c.Gaps = append(c.Gaps, g)
	}
	c.Events = make([]Event, 0, len(r.Events))
	for _, e := range r.Events {
		if dropTiming {
			e.AtNS = 0
		}
		c.Events = append(c.Events, e)
	}
	c.Marks = make([]Mark, 0, len(r.Marks))
	for _, m := range r.Marks {
		if dropTiming {
			m.AtNS = 0
		}
		m.Entries = nonNil(m.Entries)
		c.Marks = append(c.Marks, m)
	}
	return c
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// mark returns the first mark with the given name, or nil.
func (r *Report) mark(name string) *Mark {
	for i := range r.Marks {
		if r.Marks[i].Name == name {
			return &r.Marks[i]
		}
	}
	return nil
}

// marksBetween names the marks taken strictly between sequence numbers lo and
// hi whose snapshot contains p.
func (r *Report) marksBetween(p string, lo, hi uint64) []string {
	names := []string{}
	for i := range r.Marks {
		m := &r.Marks[i]
		if m.Seq <= lo || m.Seq >= hi {
			continue
		}
		if _, ok := m.index()[p]; ok {
			names = append(names, m.Name)
		}
	}
	return names
}

// marksDiffering names the marks whose state for p, present or absent and
// fingerprint, differs from base.
func (r *Report) marksDiffering(p string, base Entry) []string {
	names := []string{}
	for i := range r.Marks {
		m := &r.Marks[i]
		e, ok := m.index()[p]
		if !ok || e.fingerprint() != base.fingerprint() {
			names = append(names, m.Name)
		}
	}
	return names
}

// eventSeqsFor returns the sequence numbers of every event recorded for p.
func (r *Report) eventSeqsFor(p string) []uint64 {
	var seqs []uint64
	for _, e := range r.Events {
		if e.Path == p {
			seqs = append(seqs, e.Seq)
		}
	}
	return seqs
}

// index maps each path in the mark to its entry.
func (m *Mark) index() map[string]Entry {
	idx := make(map[string]Entry, len(m.Entries))
	for _, e := range m.Entries {
		idx[e.Path] = e
	}
	return idx
}

// fingerprint is the equality key for an entry. It deliberately excludes the
// inode number, so a replacement with identical content is not a revert.
func (e Entry) fingerprint() string {
	return fmt.Sprintf("%s|%d|%d|%d|%d|%d|%s|%s", e.Type, e.Mode, e.UID, e.GID, e.Nlink, e.Size, e.SHA256, e.Target)
}

// verdictFor applies the precedence: any gap gives evidence_lost. Otherwise,
// any unobserved canary gives instrument_unqualified. Otherwise the verdict is ok.
func verdictFor(gaps []Gap, beats []Heartbeat) string {
	if len(gaps) > 0 {
		return VerdictEvidenceLost
	}
	for _, b := range beats {
		if !b.Observed {
			return VerdictInstrumentUnqualified
		}
	}
	return VerdictOK
}
