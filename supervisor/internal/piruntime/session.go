package piruntime

import (
	"context"
	"errors"
	"sync"
	"time"
)

const maxSessionCleanupWorkers = 32

var (
	sessionCleanupBudget      = 5 * time.Second
	ErrSessionCleanupCapacity = errors.New("runtime session cleanup worker capacity is exhausted")
	sessionCleanupWorkers     = make(chan struct{}, maxSessionCleanupWorkers)
)

type sessionCleanupAttempt struct {
	done       chan struct{}
	settlement Settlement
	err        error
}

// Session owns child lifecycle and admission. It is intentionally unaware of
// HTTP, browsers, TUI rendering, provider credentials, or publication UI.
type Session struct {
	mu        sync.Mutex
	child     *Process
	admission *Admission
	state     SettlementState
	cleanup   *sessionCleanupAttempt
}

func NewSession(child *Process) (*Session, error) {
	if child == nil {
		return nil, errors.New("Pi runtime session requires a child")
	}
	admission := NewAdmission()
	if err := admission.Activate(); err != nil {
		return nil, err
	}
	return &Session{child: child, admission: admission, state: SettlementRunning}, nil
}

func (s *Session) Admission() *Admission {
	if s == nil {
		return nil
	}
	return s.admission
}

func (s *Session) Acquire(ctx context.Context) (*Lease, error) {
	if s == nil || s.admission == nil {
		return nil, ErrAdmissionClosed
	}
	return s.admission.Acquire(ctx)
}

func (s *Session) State() SettlementState {
	if s == nil {
		return SettlementUnknown
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Close closes admission and shares one bounded cleanup attempt among
// concurrent callers. A caller deadline only controls that caller's wait. An
// UNKNOWN attempt is retryable: subsequent calls re-drain leases, observe child
// exit and descendant settlement, and replace the previous UNKNOWN only with
// new evidence. STOPPED is cached, including any observation-gap error.
func (s *Session) Close(ctx context.Context) (Settlement, error) {
	if s == nil || ctx == nil || s.child == nil || s.admission == nil {
		return Settlement{State: SettlementUnknown, Detail: "runtime session is not configured"}, ErrUnknownSettlement
	}
	s.mu.Lock()
	if current := s.cleanup; current != nil {
		select {
		case <-current.done:
			if current.settlement.State == SettlementStopped {
				s.mu.Unlock()
				return current.settlement, current.err
			}
		default:
			s.mu.Unlock()
			return s.waitCleanup(ctx, current)
		}
	}
	select {
	case sessionCleanupWorkers <- struct{}{}:
		attempt := &sessionCleanupAttempt{done: make(chan struct{})}
		s.cleanup = attempt
		s.state = SettlementUnknown
		child, admission := s.child, s.admission
		s.mu.Unlock()
		go s.performCleanup(attempt, child, admission)
		return s.waitCleanup(ctx, attempt)
	default:
		s.state = SettlementUnknown
		child, admission := s.child, s.admission
		s.mu.Unlock()
		// Refuse another cleanup goroutine, but synchronously close admission
		// and request direct-child stop so pool saturation cannot leave the Pi
		// process running. This fallback is caller-bounded and reports UNKNOWN
		// if leases or child/descendant settlement remain uncertain.
		cleanupCtx, cancel := context.WithTimeout(ctx, sessionCleanupBudget)
		defer cancel()
		settlement, err := closeAdmissionAndStop(cleanupCtx, child, admission)
		settlement.Detail = errors.Join(errors.New(ErrSessionCleanupCapacity.Error()), nonEmptyError(settlement.Detail)).Error()
		s.mu.Lock()
		s.state = settlement.State
		s.mu.Unlock()
		return settlement, errors.Join(ErrSessionCleanupCapacity, err)
	}
}

func (s *Session) performCleanup(attempt *sessionCleanupAttempt, child *Process, admission *Admission) {
	defer func() { <-sessionCleanupWorkers }()
	cleanupCtx, cancel := context.WithTimeout(context.Background(), sessionCleanupBudget)
	defer cancel()
	settlement, cleanupErr := closeAdmissionAndStop(cleanupCtx, child, admission)
	attempt.settlement, attempt.err = settlement, cleanupErr
	s.mu.Lock()
	s.state = settlement.State
	close(attempt.done)
	s.mu.Unlock()
}

func closeAdmissionAndStop(ctx context.Context, child *Process, admission *Admission) (Settlement, error) {
	drainErr := admission.CloseAndWait(ctx)
	settlement, stopErr := child.Stop(ctx)
	if drainErr != nil {
		settlement.State = SettlementUnknown
		settlement.Detail = errors.Join(errors.New("admission leases did not drain"), drainErr).Error()
	} else if settlement.State != SettlementStopped {
		settlement.State = SettlementUnknown
	}
	return settlement, errors.Join(drainErr, stopErr)
}

func (s *Session) waitCleanup(ctx context.Context, attempt *sessionCleanupAttempt) (Settlement, error) {
	select {
	case <-attempt.done:
		return attempt.settlement, attempt.err
	case <-ctx.Done():
		settlement := s.child.LastSettlement()
		settlement.State = SettlementUnknown
		settlement.Detail = errors.Join(errors.New("session cleanup did not settle before caller deadline"), ctx.Err()).Error()
		return settlement, ctx.Err()
	}
}

func nonEmptyError(message string) error {
	if message == "" {
		return nil
	}
	return errors.New(message)
}
