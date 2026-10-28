package runner

import (
	"context"
	"errors"
	"testing"

	"github.com/rs/zerolog"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// fakeBdbCounter returns a fixed count, or fails when err is set.
type fakeBdbCounter struct {
	n   int64
	err error
}

func (f *fakeBdbCounter) CountActiveBdbs(context.Context) (int64, error) {
	return f.n, f.err
}

// TestRegisterUsageGauges_ObservesActiveBdbCount verifies the gauge reports the counter's current value at each
// collection and reports nothing for a collection whose count failed.
func TestRegisterUsageGauges_ObservesActiveBdbCount(t *testing.T) {
	ctx := context.Background()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		if err := provider.Shutdown(ctx); err != nil {
			t.Errorf("shutting down meter provider: %v", err)
		}
	})

	counter := &fakeBdbCounter{n: 7}
	reg, err := RegisterUsageGauges(provider.Meter("test"), counter, zerolog.Nop())
	if err != nil {
		t.Fatalf("RegisterUsageGauges: %v", err)
	}
	t.Cleanup(func() {
		if err := reg.Unregister(); err != nil {
			t.Errorf("unregistering gauges: %v", err)
		}
	})

	if got := collectBdbGauge(t, reader); got == nil || *got != 7 {
		t.Errorf("gauge = %v, want 7", got)
	}

	counter.n = 3
	if got := collectBdbGauge(t, reader); got == nil || *got != 3 {
		t.Errorf("gauge after a change = %v, want 3", got)
	}

	counter.err = errors.New("boom")
	if got := collectBdbGauge(t, reader); got != nil {
		t.Errorf("gauge after a failed count = %d, want no data point", *got)
	}
}

// collectBdbGauge collects once and returns the bdbs gauge's single data point, or nil when none was observed.
func collectBdbGauge(t *testing.T, reader *sdkmetric.ManualReader) *int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "baguettedb.usage.bdbs" {
				continue
			}
			gauge, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				t.Fatalf("metric is %T, want Gauge[int64]", m.Data)
			}
			if len(gauge.DataPoints) == 0 {
				return nil
			}
			if len(gauge.DataPoints) != 1 {
				t.Fatalf("gauge has %d data points, want 1", len(gauge.DataPoints))
			}
			v := gauge.DataPoints[0].Value
			return &v
		}
	}
	return nil
}
