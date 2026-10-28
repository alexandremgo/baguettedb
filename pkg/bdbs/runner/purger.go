// Package runner contains driving adapters that translate external triggers (ticks, signals, …) into bdb service
// calls — mirroring the role of the api package, which translates HTTP requests into the same service calls.
package runner

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/service"
)

// Purger drives BdbService.PurgeExpired on a fixed interval. It owns the clock and lifecycle; all sweep semantics
// live in the service.
type Purger struct {
	svc      *service.BdbService
	interval time.Duration
	logger   zerolog.Logger
}

// NewPurger creates a Purger that ticks every interval and calls svc.PurgeExpired.
func NewPurger(svc *service.BdbService, interval time.Duration, logger zerolog.Logger) *Purger {
	return &Purger{svc: svc, interval: interval, logger: logger}
}

// Run blocks until ctx is cancelled, invoking PurgeExpired on each tick. Per-sweep errors are logged and the loop
// continues.
func (p *Purger) Run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	p.logger.Info().Stringer("interval", p.interval).Msg("bdb purge runner started")

	for {
		select {
		case <-ctx.Done():
			p.logger.Info().Msg("bdb purge runner stopped")
			return
		case t := <-ticker.C:
			stats, err := p.svc.PurgeExpired(ctx, t.UTC())
			if err != nil {
				p.logger.Error().Err(err).Msg("bdb purge sweep failed")
				continue
			}
			if stats.Scanned == 0 {
				continue
			}
			p.logger.Info().
				Int("scanned", stats.Scanned).
				Int("purged", stats.Purged).
				Int("failed", stats.Failed).
				Msg("Bdb purge sweep complete")
		}
	}
}
