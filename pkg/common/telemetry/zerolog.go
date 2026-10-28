package telemetry

import (
	"context"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/trace"
)

// WithTrace adds trace_id and span_id fields from ctx to the zerolog event.
// Returns the event unchanged if the span context is not valid.
func WithTrace(ctx context.Context, e *zerolog.Event) *zerolog.Event {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return e
	}
	return e.
		Str("trace_id", sc.TraceID().String()).
		Str("span_id", sc.SpanID().String())
}
