// Package runner contains driving adapters that translate external triggers (ticks, signals, …) into auth service
// calls — mirroring the role of the api package, which translates HTTP requests into the same service calls.
package runner

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/auth/service"
)

// OIDCStatePurger drives OIDCLoginService.PurgeExpiredFlows on a fixed interval. It owns the clock and lifecycle;
// all sweep semantics live in the service.
type OIDCStatePurger struct {
	svc      *service.OIDCLoginService
	interval time.Duration
	logger   zerolog.Logger
}

// NewOIDCStatePurger creates an OIDCStatePurger that ticks every interval and calls svc.PurgeExpiredFlows.
func NewOIDCStatePurger(
	svc *service.OIDCLoginService, interval time.Duration, logger zerolog.Logger,
) *OIDCStatePurger {
	return &OIDCStatePurger{svc: svc, interval: interval, logger: logger}
}

// Run blocks until ctx is cancelled, invoking PurgeExpiredFlows on each tick. Per-sweep errors are logged and the
// loop continues.
func (p *OIDCStatePurger) Run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	p.logger.Info().Stringer("interval", p.interval).Msg("OIDC flow state purge runner started")

	for {
		select {
		case <-ctx.Done():
			p.logger.Info().Msg("OIDC flow state purge runner stopped")
			return
		case <-ticker.C:
			deleted, err := p.svc.PurgeExpiredFlows(ctx)
			if err != nil {
				p.logger.Error().Err(err).Msg("OIDC flow state purge sweep failed")
				continue
			}
			if deleted > 0 {
				p.logger.Info().Int64("deleted", deleted).Msg("OIDC flow state purge sweep complete")
			}
		}
	}
}
