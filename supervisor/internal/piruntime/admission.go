package piruntime

import (
	"context"
	"errors"
	"sync"
)

var (
	ErrAdmissionClosed   = errors.New("Pi runtime admission is closed")
	ErrUnknownSettlement = errors.New("Pi runtime child settlement is UNKNOWN")
)

// Lease is one admitted proposal/effect scope. Closing the admission cancels
// every live lease context before waiting for release, so a blocked provider or
// executor cannot keep a session looking stopped merely because admission was
// closed.
type Lease struct {
	ctx     context.Context
	cancel  context.CancelFunc
	release func()
	once    sync.Once
}

func (l *Lease) Context() context.Context {
	if l == nil || l.ctx == nil {
		return context.Background()
	}
	return l.ctx
}

func (l *Lease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		if l.cancel != nil {
			l.cancel()
		}
		if l.release != nil {
			l.release()
		}
	})
}

// Admission is a small reusable close-and-drain barrier. It starts closed;
// callers must Activate only after host/profile recovery has succeeded.
type Admission struct {
	mu       sync.Mutex
	open     bool
	closed   bool
	inFlight int
	leases   map[*Lease]struct{}
	drained  chan struct{}
}

func NewAdmission() *Admission { return &Admission{leases: make(map[*Lease]struct{})} }

func (a *Admission) Activate() error {
	if a == nil {
		return ErrAdmissionClosed
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return ErrAdmissionClosed
	}
	a.open = true
	return nil
}

func (a *Admission) Acquire(parent context.Context) (*Lease, error) {
	if a == nil || parent == nil {
		return nil, ErrAdmissionClosed
	}
	if err := parent.Err(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	if !a.open || a.closed {
		a.mu.Unlock()
		return nil, ErrAdmissionClosed
	}
	ctx, cancel := context.WithCancel(parent)
	l := &Lease{ctx: ctx, cancel: cancel}
	a.inFlight++
	a.leases[l] = struct{}{}
	l.release = func() {
		a.mu.Lock()
		if _, ok := a.leases[l]; ok {
			delete(a.leases, l)
			a.inFlight--
			if a.closed && a.inFlight == 0 && a.drained != nil {
				close(a.drained)
				a.drained = nil
			}
		}
		a.mu.Unlock()
	}
	a.mu.Unlock()
	return l, nil
}

// CloseAndWait prevents new admission, cancels all admitted lease contexts,
// and waits until every lease has released. A context deadline leaves the
// barrier unresolved; callers must classify the session UNKNOWN.
func (a *Admission) CloseAndWait(ctx context.Context) error {
	if a == nil || ctx == nil {
		return ErrAdmissionClosed
	}
	a.mu.Lock()
	if !a.closed {
		a.open, a.closed = false, true
		for lease := range a.leases {
			if lease.cancel != nil {
				lease.cancel()
			}
		}
	}
	if a.inFlight == 0 {
		a.mu.Unlock()
		return nil
	}
	if a.drained == nil {
		a.drained = make(chan struct{})
	}
	drained := a.drained
	a.mu.Unlock()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *Admission) Closed() bool {
	if a == nil {
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.closed
}
