package runner

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// TestBackupMetrics_Record verifies the stats -> instrument mapping the alert queries depend on: every result and
// outcome value gets a series on every sweep (zero included), and a sweep-level error counts as result="error"
// without advancing the last-success timestamp gauge.
func TestBackupMetrics_Record(t *testing.T) {
	ctx := context.Background()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		if err := provider.Shutdown(ctx); err != nil {
			t.Errorf("shutting down meter provider: %v", err)
		}
	})

	m, err := newBackupMetrics(provider.Meter("test"))
	if err != nil {
		t.Fatalf("newBackupMetrics: %v", err)
	}

	sweepDone := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	m.record(ctx, service.BackupStats{
		Scanned:       5,
		Uploaded:      2,
		SkippedClean:  2,
		Failed:        1,
		UploadedBytes: 4096,
	}, nil, sweepDone)
	m.record(ctx, service.BackupStats{}, errors.New("listing databases: boom"), sweepDone.Add(15*time.Minute))

	metrics := collect(t, reader)

	sweeps := sumByAttr(t, metrics, "baguettedb.backup.sweeps", "result")
	if sweeps["ok"] != 1 || sweeps["error"] != 1 {
		t.Errorf("sweeps by result = %v, want ok=1 error=1", sweeps)
	}

	wantOutcomes := map[string]int64{
		"uploaded": 2, "skipped_clean": 2, "skipped_missing_file": 0,
		"skipped_low_space": 0, "skipped_foreign": 0, "failed": 1,
	}
	outcomes := sumByAttr(t, metrics, "baguettedb.backup.sweep.databases", "outcome")
	for outcome, want := range wantOutcomes {
		if got, ok := outcomes[outcome]; !ok || got != want {
			t.Errorf("outcome %q = %d (present=%v), want %d — zero-valued outcomes must still exist",
				outcome, got, ok, want)
		}
	}

	uploaded := sumByAttr(t, metrics, "baguettedb.backup.uploaded", "")
	if uploaded[""] != 4096 {
		t.Errorf("uploaded bytes = %d, want 4096", uploaded[""])
	}

	// The failed second sweep must not advance the last-success timestamp.
	got := lastGaugeValue(t, metrics, "baguettedb.backup.last_success_timestamp")
	if want := float64(sweepDone.Unix()); got != want {
		t.Errorf("last success timestamp = %v, want %v", got, want)
	}
}

// TestBackupMetrics_RecordOnNilReceiverIsNoop verifies recording on a nil receiver (instrument creation failed)
// does nothing rather than panic.
func TestBackupMetrics_RecordOnNilReceiverIsNoop(_ *testing.T) {
	var m *backupMetrics
	m.record(context.Background(), service.BackupStats{Uploaded: 1}, nil, time.Now())
}

// collect gathers everything the reader has seen into a name-indexed map.
func collect(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}
	out := make(map[string]metricdata.Metrics)
	for _, scope := range rm.ScopeMetrics {
		for _, metric := range scope.Metrics {
			out[metric.Name] = metric
		}
	}
	return out
}

// sumByAttr returns an int64 sum's data points keyed by the given attribute's value ("" when attrKey is empty).
func sumByAttr(t *testing.T, metrics map[string]metricdata.Metrics, name, attrKey string) map[string]int64 {
	t.Helper()
	metric, ok := metrics[name]
	if !ok {
		t.Fatalf("metric %q not collected", name)
	}
	sum, ok := metric.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("metric %q is %T, want Sum[int64]", name, metric.Data)
	}
	out := make(map[string]int64)
	for _, dp := range sum.DataPoints {
		key := ""
		if attrKey != "" {
			v, ok := dp.Attributes.Value(attribute.Key(attrKey))
			if !ok {
				t.Fatalf("metric %q data point missing attribute %q", name, attrKey)
			}
			key = v.AsString()
		}
		out[key] = dp.Value
	}
	return out
}

// lastGaugeValue returns the single data point of a float64 gauge.
func lastGaugeValue(t *testing.T, metrics map[string]metricdata.Metrics, name string) float64 {
	t.Helper()
	metric, ok := metrics[name]
	if !ok {
		t.Fatalf("metric %q not collected", name)
	}
	gauge, ok := metric.Data.(metricdata.Gauge[float64])
	if !ok {
		t.Fatalf("metric %q is %T, want Gauge[float64]", name, metric.Data)
	}
	if len(gauge.DataPoints) != 1 {
		t.Fatalf("metric %q has %d data points, want 1", name, len(gauge.DataPoints))
	}
	return gauge.DataPoints[0].Value
}
