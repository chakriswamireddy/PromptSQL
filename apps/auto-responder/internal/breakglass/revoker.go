package breakglass

import (
	"context"
	"time"

	"github.com/governance-platform/pkg/logging"
)

// Revoker runs a periodic job that expires stale break-glass sessions.
type Revoker struct {
	store    *Store
	interval time.Duration
	log      logging.Logger
}

// NewRevoker creates a Revoker.
func NewRevoker(store *Store, interval time.Duration, log logging.Logger) *Revoker {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Revoker{store: store, interval: interval, log: log}
}

// Run starts the revocation loop. Blocks until ctx is cancelled.
func (r *Revoker) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	r.log.Info().Dur("interval", r.interval).Msg("break-glass auto-revoker started")
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := r.store.ExpireStale(ctx)
			if err != nil {
				r.log.Error().Err(err).Msg("break-glass revocation sweep failed")
				continue
			}
			if n > 0 {
				r.log.Info().Int64("expired", n).Msg("break-glass sessions auto-expired")
			}
		}
	}
}
