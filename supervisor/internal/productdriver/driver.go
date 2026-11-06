//go:build linux

package productdriver

import (
	"sync"
	"time"

	"tbound/supervisor/internal/productsession"
)

// Config is the trusted input of a Driver.
type Config struct {
	// Catalog is the turn catalog, normally a *productsession.Catalog.
	Catalog turnCatalog
	Host    Host
	// ReviewTimeoutSeconds is how long a review turn waits for a decision before it expires.
	ReviewTimeoutSeconds int
	// Now is the monotonic clock of review deadlines; nil means time.Now.
	Now func() time.Time
}

// Driver owns the running turns and the held review turns of one process. Every method is safe for
// concurrent use; mu orders review registration, resolution and cancellation.
type Driver struct {
	catalog              turnCatalog
	host                 Host
	reviewTimeoutSeconds int
	nowFunc              func() time.Time

	mu sync.Mutex
	// running holds the turns RunTurn drives in this process. pending holds closed review turns whose
	// workflow and finalizer stay open until a resolution, cancellation or expiry takes them.
	running map[string]bool
	pending map[string]*pendingReview
}

func New(cfg Config) *Driver {
	return &Driver{catalog: cfg.Catalog, host: cfg.Host, reviewTimeoutSeconds: cfg.ReviewTimeoutSeconds, nowFunc: cfg.Now}
}

func (d *Driver) now() time.Time {
	if d.nowFunc != nil {
		return d.nowFunc()
	}
	return time.Now()
}

// turnCatalog is the part of *productsession.Catalog the driver uses.
type turnCatalog interface {
	Turn(id string) (productsession.Turn, bool)
	Session(p productsession.Principal, id string) (productsession.Session, error)
	UnresolvedTurns() []productsession.Turn
	LinkTurn(turnID string, link productsession.TurnLink) error
	CompleteTurn(turnID string, t productsession.TurnTerminal) error
	MarkAwaitingReview(turnID string, r productsession.TurnReview) error
	MarkTurnUnknown(turnID string, reason string) error
	AbandonUnlinkedTurn(turnID string) error
	RefuseUnlinkedTurn(turnID string, code productsession.Code) error
	CancelTurn(p productsession.Principal, r productsession.CancelTurnRequest) (productsession.Turn, bool, error)
	ResolveTurn(p productsession.Principal, r productsession.ResolveTurnRequest) (productsession.Turn, bool, error)
}

var _ turnCatalog = (*productsession.Catalog)(nil)
