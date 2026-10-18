//go:build linux

package procoracle

import (
	"errors"
	"sync"
	"time"
)

// MonitorConfig configures a Monitor.
type MonitorConfig struct {
	// Registration is the registered record (its digest is carried by every poll,
	// its CellUID drives the identity scan, its MaxGap is the bound the record is
	// evaluated against).
	Registration Registration
	// Interval is the polling period. It must be positive and shorter than
	// Registration.MaxGap, or the record could never satisfy the coverage rule.
	Interval time.Duration
	// Registry returns the launcher's current registry. It is called on the
	// monitor's goroutine, so it must be safe for concurrent use.
	Registry func() Registry
}

// Monitor is the continuous-coverage witness of EFF-PROC-02. A goroutine runs
// the identity scan of Capture (cell members, registry and every process of the
// cell identity anywhere in /proc, without sockets) at a fixed interval and
// appends each poll to an append-only record, numbered 1..n and timed.
//
// What it proves, and what it does not. Evaluate accepts the record as a
// witness when the largest gap between consecutive polls (counting the gap from
// the interval start to the first poll and from the last poll to the
// post-teardown snapshot) is at most Registration.MaxGap, the numbering has no
// hole and no poll has a gap. A process of the cell identity that was outside the
// cell cgroup during a poll is a PROC-02 finding. A crossing that begins and ends
// between two polls (shorter than the poll spacing, at most MaxGap) is NOT seen:
// that is the stated detection limit of this instrument, and MaxGap is the
// registered bound of it.
//
// The cell reader is called from the monitor's goroutine while the executor may
// use it too; HeldCell is safe for that (it only reads).
type Monitor struct {
	mu      sync.Mutex
	records []Snapshot
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
}

// StartMonitor takes the first poll synchronously (so the record is never empty
// once it returns) and then keeps polling until Stop.
func StartMonitor(cell CellReader, view HostView, cfg MonitorConfig) (*Monitor, error) {
	switch {
	case cfg.Registry == nil:
		return nil, errors.New("procoracle: a monitor needs the registry source")
	case cfg.Interval <= 0:
		return nil, errors.New("procoracle: the monitor interval must be positive")
	case cfg.Registration.MaxGap <= 0 || cfg.Interval >= cfg.Registration.MaxGap:
		return nil, errors.New("procoracle: the monitor interval must be shorter than the registered MaxGap")
	}
	m := &Monitor{stop: make(chan struct{}), done: make(chan struct{})}
	poll := func() {
		s := capture(cell, view, CaptureParams{
			Point: PointMonitor, Seq: len(m.Records()) + 1, Registration: cfg.Registration, Registry: cfg.Registry(),
		}, false)
		m.mu.Lock()
		m.records = append(m.records, s)
		m.mu.Unlock()
	}
	poll()
	go func() {
		defer close(m.done)
		t := time.NewTicker(cfg.Interval)
		defer t.Stop()
		for {
			select {
			case <-m.stop:
				return
			case <-t.C:
				poll()
			}
		}
	}()
	return m, nil
}

// Records returns a copy of the polls so far, in sequence order.
func (m *Monitor) Records() []Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Snapshot, len(m.records))
	copy(out, m.records)
	return out
}

// Stop ends the polling, waits for the goroutine (a poll in progress completes
// and is recorded) and returns the final record. It may be called more than once.
func (m *Monitor) Stop() []Snapshot {
	m.once.Do(func() { close(m.stop) })
	<-m.done
	return m.Records()
}
