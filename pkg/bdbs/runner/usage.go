package runner

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/metric"
)

// activeBdbCounter is the slice of the bdb service the usage gauges read through.
type activeBdbCounter interface {
	CountActiveBdbs(ctx context.Context) (int64, error)
}

// RegisterUsageGauges registers the bdb usage gauges on meter and returns their registration so they can be
// unregistered on shutdown. baguettedb.usage.bdbs reports the number of bdbs not soft-deleted, read from counter at
// every collection; a failing count is logged and leaves the gauge unobserved for that collection rather than
// re-reporting a stale value.
func RegisterUsageGauges(
	meter metric.Meter, counter activeBdbCounter, logger zerolog.Logger,
) (metric.Registration, error) {
	bdbs, err := meter.Int64ObservableGauge("baguettedb.usage.bdbs",
		metric.WithDescription("Bdbs that are not soft-deleted."),
		metric.WithUnit("{bdb}"),
	)
	if err != nil {
		return nil, fmt.Errorf("creating bdbs gauge: %w", err)
	}
	reg, err := meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		n, err := counter.CountActiveBdbs(ctx)
		if err != nil {
			logger.Error().Err(err).Msg("bdb usage gauges: counting active bdbs failed")
			return nil
		}
		o.ObserveInt64(bdbs, n)
		return nil
	}, bdbs)
	if err != nil {
		return nil, fmt.Errorf("registering bdbs gauge callback: %w", err)
	}
	return reg, nil
}
