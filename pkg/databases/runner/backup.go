// Package runner contains driving adapters that translate external triggers (ticks, signals, …) into databases
// service calls — mirroring the role of the api package, which translates HTTP requests into the same service
// calls.
package runner

import (
	"context"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// BackupRunner drives BackupService.SweepOnce on a fixed interval. It owns the clock and lifecycle; all backup
// semantics live in the service.
type BackupRunner struct {
	svc      *service.BackupService
	interval time.Duration
	logger   zerolog.Logger
	metrics  *backupMetrics
}

// NewBackupRunner creates a BackupRunner that ticks every interval and calls svc.SweepOnce.
func NewBackupRunner(svc *service.BackupService, interval time.Duration, logger zerolog.Logger) *BackupRunner {
	metrics, err := newBackupMetrics(otel.Meter("github.com/alexandremgo/baguettedb/pkg/databases/runner"))
	if err != nil {
		logger.Error().Err(err).Msg("database backup runner: creating sweep metric instruments; sweep metrics disabled")
	}
	return &BackupRunner{svc: svc, interval: interval, logger: logger, metrics: metrics}
}

// Run blocks until ctx is cancelled, invoking SweepOnce on each tick. Per-sweep errors are logged and the loop
// continues.
func (r *BackupRunner) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	r.logger.Info().Stringer("interval", r.interval).Msg("database backup runner started")

	for {
		select {
		case <-ctx.Done():
			r.logger.Info().Msg("database backup runner stopped")
			return
		case t := <-ticker.C:
			r.logger.Debug().Msg("database backup sweep starting")
			stats, err := r.svc.SweepOnce(ctx, t.UTC())
			r.metrics.record(ctx, stats, err, t.UTC())
			if err != nil {
				r.logger.Error().Err(err).Msg("database backup sweep failed")
				continue
			}
			if stats.Uploaded == 0 && stats.Failed == 0 && stats.SkippedLowSpace == 0 {
				continue
			}
			r.logger.Info().
				Int("scanned", stats.Scanned).
				Int("uploaded", stats.Uploaded).
				Int64("uploaded_bytes", stats.UploadedBytes).
				Int("skipped_clean", stats.SkippedClean).
				Int("skipped_missing_file", stats.SkippedMissingFile).
				Int("skipped_low_space", stats.SkippedLowSpace).
				Int("skipped_foreign", stats.SkippedForeign).
				Int("failed", stats.Failed).
				Msg("Database backup sweep complete")
		}
	}
}

// backupMetrics holds the sweep instruments. Instruments are no-ops until OpenTelemetry is initialized with a
// real meter provider.
type backupMetrics struct {
	// sweeps counts completed sweeps by result ("ok" or "error", where error is a sweep-level failure such as
	// the database listing failing — per-database failures count as sweep result "ok" with outcome "failed").
	sweeps metric.Int64Counter
	// databases counts databases handled per sweep by outcome ("uploaded", "skipped_clean",
	// "skipped_missing_file", "skipped_low_space", "skipped_foreign", "failed").
	databases metric.Int64Counter
	// uploadedBytes totals the size of the snapshots uploaded.
	uploadedBytes metric.Int64Counter
	// lastSuccessUnix reports the unix-seconds completion time of the newest sweep that finished without a
	// sweep-level error, so freshness alerting can treat metric silence itself as an incident.
	lastSuccessUnix metric.Float64Gauge
}

// newBackupMetrics creates the sweep instruments on meter.
func newBackupMetrics(meter metric.Meter) (*backupMetrics, error) {
	var m backupMetrics
	var err error

	if m.sweeps, err = meter.Int64Counter("baguettedb.backup.sweeps",
		metric.WithDescription("Completed backup sweeps by result."),
		metric.WithUnit("{sweep}"),
	); err != nil {
		return nil, err
	}
	if m.databases, err = meter.Int64Counter("baguettedb.backup.sweep.databases",
		metric.WithDescription("Databases handled per backup sweep, by outcome."),
		metric.WithUnit("{database}"),
	); err != nil {
		return nil, err
	}
	if m.uploadedBytes, err = meter.Int64Counter("baguettedb.backup.uploaded",
		metric.WithDescription("Total size of the snapshots uploaded to the store."),
		metric.WithUnit("By"),
	); err != nil {
		return nil, err
	}
	if m.lastSuccessUnix, err = meter.Float64Gauge("baguettedb.backup.last_success_timestamp",
		metric.WithDescription("Completion time of the newest sweep without a sweep-level error, as unix seconds."),
		metric.WithUnit("s"),
	); err != nil {
		return nil, err
	}
	return &m, nil
}

// record publishes one sweep's stats. Every result and outcome value is recorded on every sweep — zero included —
// so all series exist from the first sweep and rate queries never confuse "never happened" with "no data". Safe on
// a nil receiver (instrument creation failed): it records nothing.
func (m *backupMetrics) record(ctx context.Context, stats service.BackupStats, sweepErr error, finishedAt time.Time) {
	if m == nil {
		return
	}

	okSweeps, errSweeps := int64(1), int64(0)
	if sweepErr != nil {
		okSweeps, errSweeps = 0, 1
	}
	m.sweeps.Add(ctx, okSweeps, metric.WithAttributes(attribute.String("result", "ok")))
	m.sweeps.Add(ctx, errSweeps, metric.WithAttributes(attribute.String("result", "error")))

	outcomes := []struct {
		outcome string
		count   int
	}{
		{"uploaded", stats.Uploaded},
		{"skipped_clean", stats.SkippedClean},
		{"skipped_missing_file", stats.SkippedMissingFile},
		{"skipped_low_space", stats.SkippedLowSpace},
		{"skipped_foreign", stats.SkippedForeign},
		{"failed", stats.Failed},
	}
	for _, o := range outcomes {
		m.databases.Add(ctx, int64(o.count), metric.WithAttributes(attribute.String("outcome", o.outcome)))
	}

	m.uploadedBytes.Add(ctx, stats.UploadedBytes)

	if sweepErr == nil {
		m.lastSuccessUnix.Record(ctx, float64(finishedAt.Unix()))
	}
}
