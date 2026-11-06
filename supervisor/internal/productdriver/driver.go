//go:build linux

package productdriver

import (
	"sync"
	"time"

	"tbound/supervisor/internal/productsession"
)

// Config is the trusted input of a Driver.
type Config struct {
	Catalog *productsession.Catalog
	Host    Host
	// ReviewTimeoutSeconds is how long a review turn waits for a decision before it expires.
	ReviewTimeoutSeconds int
	// Now is the monotonic clock of review deadlines; nil means time.Now.
	Now func() time.Time
}

// Driver owns the running turns and the held review turns of one process. Every method is safe for
// concurrent use; mu orders review registration, resolution and cancellation.
type Driver struct {
	catalog              *productsession.Catalog
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
