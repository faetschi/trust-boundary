//go:build linux

package effectoracle

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// defaultCanaryTimeout bounds how long a canary may take to be observed. A
// canary normally completes in milliseconds.
const defaultCanaryTimeout = 5 * time.Second

// readBufferSize holds many inotify records. One record needs at most
// 16 + NAME_MAX + 1 bytes.
const readBufferSize = 64 * 1024

// watchMask is requested on every watched directory. Directories are the only
// watch targets. Files are observed through their parent directory's watch.
const watchMask = syscall.IN_CREATE | syscall.IN_DELETE | syscall.IN_MODIFY |
	syscall.IN_CLOSE_WRITE | syscall.IN_MOVED_FROM | syscall.IN_MOVED_TO |
	syscall.IN_ATTRIB | syscall.IN_DELETE_SELF | syscall.IN_MOVE_SELF |
	syscall.IN_ONLYDIR | syscall.IN_DONT_FOLLOW

// maskNames lists the requested masks, sorted, for Report.Masks.
var maskNames = []string{
	"IN_ATTRIB", "IN_CLOSE_WRITE", "IN_CREATE", "IN_DELETE", "IN_DELETE_SELF",
	"IN_DONT_FOLLOW", "IN_MODIFY", "IN_MOVED_FROM", "IN_MOVED_TO", "IN_MOVE_SELF",
	"IN_ONLYDIR",
}

// Observer records one observation window over a directory tree. Its methods
// are safe for concurrent use. Mark and Stop serialize on ctrl. The reader
// goroutine and the control path serialize on mu.
type Observer struct {
	root    string
	t0      time.Time
	nonce   string
	src     inotifyAPI
	timeout time.Duration

	ctrl sync.Mutex // serializes Mark, Stop, and canary writes
	mu   sync.Mutex // guards the state below, which the reader also updates

	seq        uint64
	wds        map[int]string           // watch descriptor -> slash-relative directory
	rootWd     int                      // -1 when the root is not watched
	rootLost   bool                     // the root was deleted, moved, or unmounted
	readerDead bool                     // the reader stopped after a read or parse failure
	issued     map[string]*canaryWaiter // every canary name issued, kept for filtering
	canaries   int
	stopped    bool
	report     *Report

	events []Event
	beats  []Heartbeat
	gaps   []Gap
	marks  []Mark

	readerDone chan struct{}
}

// canaryWaiter tracks one canary. The reader closes ch when the canary's
// close_write arrives. A waiter that gave up is abandoned, so a late observation
// is never recorded as a success.
type canaryWaiter struct {
	ch        chan struct{}
	observed  bool
	abandoned bool
}

// Start begins observing the directory tree at root. It installs watches on
// every directory below root, starts the reader, and takes the "start" mark.
// It fails when root is not a real directory, when inotify is unavailable, or
// when the initial snapshot cannot be taken. A watch that cannot be installed
// does not fail Start. It is recorded as a gap, and the verdict is evidence_lost.
func Start(root string) (*Observer, error) {
	return startWith(root, nil, defaultCanaryTimeout)
}

// startWith is Start with an injectable kernel surface and canary timeout. A
// nil src selects the kernel implementation.
func startWith(root string, src inotifyAPI, timeout time.Duration) (*Observer, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return nil, ErrRootNotDirectory
	}
	if src == nil {
		k, err := openKernelInotify()
		if err != nil {
			return nil, fmt.Errorf("effectoracle: inotify_init1: %w", err)
		}
		src = k
	}
	nonce, err := newNonce()
	if err != nil {
		_ = src.Close()
		return nil, err
	}
	o := &Observer{
		root:       abs,
		t0:         time.Now(),
		nonce:      nonce,
		src:        src,
		timeout:    timeout,
		wds:        make(map[int]string),
		rootWd:     -1,
		issued:     make(map[string]*canaryWaiter),
		readerDone: make(chan struct{}),
	}
	o.mu.Lock()
	o.watchTreeLocked(".", 0)
	o.mu.Unlock()
	go o.readLoop()

	o.ctrl.Lock()
	err = o.markLocked(MarkStart)
	o.ctrl.Unlock()
	if err != nil {
		_ = src.Close()
		<-o.readerDone
		return nil, err
	}
	return o, nil
}

// Mark records a named observation point. It first waits for every workspace
// event that completed before the call to be read. It then snapshots the tree
// and records whether any workspace event overlapped the snapshot. Names must be
// non-empty and must not be the reserved endpoint names.
func (o *Observer) Mark(name string) error {
	if name == "" || name == MarkStart || name == MarkStop {
		return ErrInvalidMarkName
	}
	o.ctrl.Lock()
	defer o.ctrl.Unlock()
	if o.stopped {
		return ErrStopped
	}
	return o.markLocked(name)
}

// Stop takes the "stop" mark, closes the watch descriptor, and returns the
// report. Stop is idempotent. Once a report exists, every later call returns it.
func (o *Observer) Stop() (*Report, error) {
	o.ctrl.Lock()
	defer o.ctrl.Unlock()
	if o.stopped {
		if o.report == nil {
			return nil, ErrStopped
		}
		return o.report, nil
	}
	err := o.markLocked(MarkStop)
	o.stopped = true
	_ = o.src.Close()
	<-o.readerDone
	if err != nil {
		return nil, err
	}
	o.report = o.buildReport()
	return o.report, nil
}

// readLoop applies kernel records in queue order until the source is closed or
// a read or parse fails. A failure is recorded as a read_error gap. The reader
// then stops, so later canaries fail fast.
func (o *Observer) readLoop() {
	defer close(o.readerDone)
	buf := make([]byte, readBufferSize)
	for {
		n, err := o.src.Read(buf)
		if err != nil {
			if errors.Is(err, os.ErrClosed) {
				return
			}
			o.mu.Lock()
			o.gapLocked(GapReadError, "", o.seq, "read: "+errnoName(err))
			o.readerDead = true
			o.mu.Unlock()
			return
		}
		records, err := parseEvents(buf[:n])
		if err != nil {
			o.mu.Lock()
			o.gapLocked(GapReadError, "", o.seq, "malformed inotify record")
			o.readerDead = true
			o.mu.Unlock()
			return
		}
		for _, rec := range records {
			o.mu.Lock()
			o.applyLocked(rec)
			o.mu.Unlock()
		}
	}
}

// applyLocked records one kernel record. The caller holds mu.
func (o *Observer) applyLocked(rec rawEvent) {
	if rec.mask&syscall.IN_Q_OVERFLOW != 0 {
		o.gapLocked(GapOverflow, "", o.seq, "kernel queue overflowed; records were dropped")
		o.rebuildLocked(o.seq)
		return
	}
	dir, known := o.wds[rec.wd]
	if !known {
		// Self events for a watch this oracle removed are expected. Anything else
		// for an unmapped watch is recorded, never silently dropped.
		if rec.mask&(syscall.IN_IGNORED|syscall.IN_MOVE_SELF|syscall.IN_DELETE_SELF) == 0 {
			o.gapLocked(GapUnmappedEvent, "", o.seq, fmt.Sprintf("mask %#x for an unmapped watch", rec.mask))
		}
		return
	}
	if rec.mask&syscall.IN_IGNORED != 0 {
		delete(o.wds, rec.wd)
		if rec.wd == o.rootWd {
			o.rootLostLocked("root watch removed by the kernel")
		}
		return
	}
	if rec.mask&syscall.IN_UNMOUNT != 0 {
		if rec.wd == o.rootWd {
			o.rootLostLocked("root unmounted")
		} else {
			o.gapLocked(GapMountBoundary, dir, o.seq, "directory below the root unmounted")
		}
		return
	}
	if rec.mask&(syscall.IN_DELETE_SELF|syscall.IN_MOVE_SELF) != 0 {
		// A subdirectory's own removal or move is recorded by its parent's event.
		if rec.wd == o.rootWd {
			o.rootLostLocked("root deleted or moved")
		}
		return
	}
	rel := dir
	if rec.name != "" {
		if dir == "." {
			if w, ours := o.issued[rec.name]; ours {
				if rec.mask&syscall.IN_CLOSE_WRITE != 0 && !w.abandoned && !w.observed {
					w.observed = true
					close(w.ch)
				}
				return // heartbeat traffic is filtered from the workspace log
			}
		}
		rel = joinRel(dir, rec.name)
	}
	kind, ok := kindOf(rec.mask)
	if !ok {
		o.gapLocked(GapUnmappedEvent, rel, o.seq, fmt.Sprintf("unexpected mask %#x", rec.mask))
		return
	}
	isDir := rec.mask&syscall.IN_ISDIR != 0
	ev := Event{Seq: o.nextSeqLocked(), AtNS: o.sinceLocked(), Kind: kind, Path: rel, IsDir: isDir}
	o.events = append(o.events, ev)
	if isDir {
		switch kind {
		case KindCreate, KindMovedTo:
			o.raceAddLocked(rel, ev.Seq)
		case KindMovedFrom:
			o.unwatchTreeLocked(rel)
		}
	}
}

// kindOf maps a kernel mask to a workspace event kind.
func kindOf(mask uint32) (string, bool) {
	switch {
	case mask&syscall.IN_CREATE != 0:
		return KindCreate, true
	case mask&syscall.IN_DELETE != 0:
		return KindDelete, true
	case mask&syscall.IN_MOVED_FROM != 0:
		return KindMovedFrom, true
	case mask&syscall.IN_MOVED_TO != 0:
		return KindMovedTo, true
	case mask&syscall.IN_CLOSE_WRITE != 0:
		return KindCloseWrite, true
	case mask&syscall.IN_MODIFY != 0:
		return KindModify, true
	case mask&syscall.IN_ATTRIB != 0:
		return KindAttrib, true
	}
	return "", false
}

// watchTreeLocked installs a watch on rel and then on every real directory
// below it. Failures are recorded as gaps that cite from. A directory already
// watched under the same path is left alone. The caller holds mu.
func (o *Observer) watchTreeLocked(rel string, from uint64) {
	abs := filepath.Join(o.root, filepath.FromSlash(rel))
	wd, err := o.src.AddWatch(abs, watchMask)
	if err != nil {
		kind := GapWatchError
		if errors.Is(err, syscall.ENOSPC) {
			kind = GapWatchLimit
		}
		o.gapLocked(kind, rel, from, errnoName(err))
		return
	}
	if dir, dup := o.wds[wd]; dup {
		if dir != rel {
			o.gapLocked(GapWatchAlias, rel, from, "directory reached through two paths")
		}
		return
	}
	o.wds[wd] = rel
	if rel == "." {
		o.rootWd = wd
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		o.gapLocked(GapWatchError, rel, from, "enumerate: "+errnoName(err))
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			o.watchTreeLocked(joinRel(rel, e.Name()), from)
		}
	}
}

// raceAddLocked handles a directory that appeared, or was moved in, after its
// creation was recorded. The new subtree is watched and enumerated. Children
// created in the gap cannot be observed, so the gap is recorded explicitly.
func (o *Observer) raceAddLocked(rel string, from uint64) {
	o.watchTreeLocked(rel, from)
	o.gapLocked(GapSubdirRace, rel, from, "directory appeared before its watch; children created in the gap are not observed")
}

// unwatchTreeLocked removes the watches on rel and below. A directory that
// moves out of the tree stops being observed. A directory that moves within the
// tree is re-watched when its moved_to event arrives.
func (o *Observer) unwatchTreeLocked(rel string) {
	for wd, dir := range o.wds {
		if dir == rel || strings.HasPrefix(dir, rel+"/") {
			_ = o.src.RmWatch(wd)
			delete(o.wds, wd)
		}
	}
}

// rebuildLocked re-enumerates the tree after a queue overflow. A watch that
// survives keeps its descriptor, so records already queued for it still map
// correctly. A watch for a vanished or moved-out directory is removed. The
// caller holds mu.
func (o *Observer) rebuildLocked(from uint64) {
	if o.rootLost {
		return
	}
	old := o.wds
	o.wds = make(map[int]string, len(old))
	o.rootWd = -1
	o.watchTreeLocked(".", from)
	for wd := range old {
		if _, kept := o.wds[wd]; !kept {
			_ = o.src.RmWatch(wd)
		}
	}
}

// rootLostLocked records that the root is gone and stops canary attempts. The
// caller holds mu.
func (o *Observer) rootLostLocked(detail string) {
	if o.rootLost {
		return
	}
	o.rootLost = true
	o.rootWd = -1
	o.gapLocked(GapRootIdentity, "", o.seq, detail)
}

// markLocked brackets a snapshot with canaries. The first canary is a barrier:
// once it is observed, every workspace event that completed earlier has been
// recorded. The second canary bounds the snapshot. The caller holds ctrl.
func (o *Observer) markLocked(name string) error {
	o.canaryLocked("mark:" + name + ":pre")
	o.mu.Lock()
	pre := o.seq
	o.mu.Unlock()

	entries, concurrent, err := snapshot(o.root)
	if err != nil {
		return fmt.Errorf("effectoracle: snapshot at %q: %w", name, err)
	}

	o.canaryLocked("mark:" + name + ":post")
	o.mu.Lock()
	defer o.mu.Unlock()
	quiescent := !concurrent && !o.eventsBetweenLocked(pre, o.seq)
	if !quiescent {
		o.gapLocked(GapSnapshotConcurrent, "", pre, "workspace events overlapped the snapshot at mark "+name)
	}
	o.marks = append(o.marks, Mark{
		Seq:       o.nextSeqLocked(),
		AtNS:      o.sinceLocked(),
		Name:      name,
		Quiescent: quiescent,
		Entries:   entries,
	})
	return nil
}

// canaryLocked writes a uniquely named file into the root, waits until the
// reader observes its close_write, and removes the file. The outcome is
// recorded as a heartbeat. The caller holds ctrl.
func (o *Observer) canaryLocked(label string) bool {
	o.mu.Lock()
	if o.readerDead || o.rootLost || o.rootWd < 0 {
		detail := "root not watched"
		if o.readerDead {
			detail = "reader stopped"
		}
		o.beatLocked(label, false, detail)
		o.mu.Unlock()
		return false
	}
	o.canaries++
	name := fmt.Sprintf(".effectoracle-canary-%s-%d", o.nonce, o.canaries)
	w := &canaryWaiter{ch: make(chan struct{})}
	o.issued[name] = w
	o.mu.Unlock()

	p := filepath.Join(o.root, name)
	detail := ""
	if err := writeCanary(p, label); err != nil {
		detail = "write " + errnoName(err)
	} else {
		timer := time.NewTimer(o.timeout)
		select {
		case <-w.ch:
		case <-timer.C:
			detail = "timeout"
		case <-o.readerDone:
			detail = "reader stopped"
		}
		timer.Stop()
	}
	removeErr := os.Remove(p)

	o.mu.Lock()
	defer o.mu.Unlock()
	if w.observed {
		detail = ""
	} else {
		w.abandoned = true
	}
	if removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
		o.gapLocked(GapCanaryCleanup, "", o.seq, errnoName(removeErr))
	}
	o.beatLocked(label, w.observed, detail)
	return w.observed
}

// writeCanary creates p exclusively. O_EXCL refuses to follow an existing
// symlink at p.
func writeCanary(p, label string) error {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString("effectoracle canary " + label + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// eventsBetweenLocked reports whether a workspace event has a sequence number
// strictly between lo and hi. The caller holds mu.
func (o *Observer) eventsBetweenLocked(lo, hi uint64) bool {
	for i := len(o.events) - 1; i >= 0 && o.events[i].Seq > lo; i-- {
		if o.events[i].Seq < hi {
			return true
		}
	}
	return false
}

// nextSeqLocked advances the record sequence. The caller holds mu.
func (o *Observer) nextSeqLocked() uint64 {
	o.seq++
	return o.seq
}

// sinceLocked returns the monotonic offset from Start. The caller holds mu.
func (o *Observer) sinceLocked() int64 {
	return time.Since(o.t0).Nanoseconds()
}

// gapLocked records an explicit gap at the next sequence number. from is the last
// sequence number the loss may follow. The caller holds mu.
func (o *Observer) gapLocked(kind, rel string, from uint64, detail string) {
	s := o.nextSeqLocked()
	o.gaps = append(o.gaps, Gap{
		Seq:     s,
		AtNS:    o.sinceLocked(),
		Kind:    kind,
		Path:    rel,
		FromSeq: from,
		ToSeq:   s,
		Detail:  detail,
	})
}

// beatLocked records one canary attempt. The caller holds mu.
func (o *Observer) beatLocked(label string, observed bool, detail string) {
	o.beats = append(o.beats, Heartbeat{
		Seq:      o.nextSeqLocked(),
		AtNS:     o.sinceLocked(),
		Label:    label,
		Observed: observed,
		Detail:   detail,
	})
}

// buildReport copies the recorded state into a report and computes the verdict.
func (o *Observer) buildReport() *Report {
	o.mu.Lock()
	defer o.mu.Unlock()
	r := &Report{
		OracleVersion: OracleVersion,
		Instrument:    Instrument,
		Masks:         append([]string(nil), maskNames...),
		Preservation:  append([]string(nil), PreservationFields...),
		Heartbeats:    append([]Heartbeat(nil), o.beats...),
		Gaps:          append([]Gap(nil), o.gaps...),
		Marks:         append([]Mark(nil), o.marks...),
		Events:        append([]Event(nil), o.events...),
	}
	r.Verdict = verdictFor(r.Gaps, r.Heartbeats)
	return r
}

// joinRel joins a slash-relative directory and a name.
func joinRel(dir, name string) string {
	if dir == "." {
		return name
	}
	return dir + "/" + name
}

// newNonce returns 128 random bits, hex-encoded. Canary names carry it so that
// only this observer's canaries are filtered.
func newNonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
