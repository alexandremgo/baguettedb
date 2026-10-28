// Package telemetry sets up OpenTelemetry for a process: Init installs the W3C trace-context and baggage propagators
// and global trace and metric providers exporting over OTLP gRPC, or leaves the no-op defaults when telemetry is
// disabled. WithTrace adds the current trace and span IDs to a zerolog event, tying a log line to its trace.
package telemetry

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/alexandremgo/baguettedb/pkg/common/config"
)

// Init initializes OpenTelemetry tracing and metrics providers.
// When cfg.OtelEnabled is false, it returns a no-op shutdown function and
// the global OTel providers remain as no-ops.
func Init(
	ctx context.Context, serviceName string, cfg config.Config,
) (shutdown func(context.Context) error, err error) {
	noop := func(context.Context) error { return nil }

	if !cfg.OtelEnabled {
		return noop, nil
	}

	attrs := []attribute.KeyValue{
		semconv.ServiceName(serviceName),
		semconv.DeploymentEnvironmentName(cfg.OtelEnvironment),
	}
	// The hostname (container runtimes give each container a unique one) distinguishes replicas of the same
	// service: without it, overlapping replicas emit metrics under identical series identities and their
	// cumulative counters interleave into one corrupted remote series.
	if host, hostErr := os.Hostname(); hostErr == nil && host != "" {
		attrs = append(attrs, semconv.ServiceInstanceID(host))
	}

	res, err := resource.New(ctx, resource.WithAttributes(attrs...))
	if err != nil {
		return noop, fmt.Errorf("creating otel resource: %w", err)
	}

	grpcOpts, err := buildGRPCOpts(cfg)
	if err != nil {
		return noop, err
	}

	traceOpts := make([]otlptracegrpc.Option, len(grpcOpts.trace))
	copy(traceOpts, grpcOpts.trace)

	metricOpts := make([]otlpmetricgrpc.Option, len(grpcOpts.metric))
	copy(metricOpts, grpcOpts.metric)

	traceExporter, err := otlptracegrpc.New(ctx, traceOpts...)
	if err != nil {
		return noop, fmt.Errorf("creating trace exporter: %w", err)
	}

	metricExporter, err := otlpmetricgrpc.New(ctx, metricOpts...)
	if err != nil {
		return noop, fmt.Errorf("creating metric exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return func(ctx context.Context) error {
		tpErr := tp.Shutdown(ctx)
		mpErr := mp.Shutdown(ctx)
		if tpErr != nil {
			return tpErr
		}
		return mpErr
	}, nil
}

type grpcOptions struct {
	trace  []otlptracegrpc.Option
	metric []otlpmetricgrpc.Option
}

func buildGRPCOpts(cfg config.Config) (grpcOptions, error) {
	var opts grpcOptions

	opts.trace = append(opts.trace, otlptracegrpc.WithEndpoint(cfg.OtelEndpoint))
	opts.metric = append(opts.metric, otlpmetricgrpc.WithEndpoint(cfg.OtelEndpoint))

	if cfg.OtelInsecure {
		opts.trace = append(opts.trace, otlptracegrpc.WithTLSCredentials(insecure.NewCredentials()))
		opts.metric = append(opts.metric, otlpmetricgrpc.WithTLSCredentials(insecure.NewCredentials()))
	} else {
		creds := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
		opts.trace = append(opts.trace, otlptracegrpc.WithTLSCredentials(creds))
		opts.metric = append(opts.metric, otlpmetricgrpc.WithTLSCredentials(creds))
	}

	if cfg.OtelHeaders != "" {
		headers, err := parseHeaders(cfg.OtelHeaders)
		if err != nil {
			return opts, err
		}
		opts.trace = append(opts.trace, otlptracegrpc.WithHeaders(headers))
		opts.metric = append(opts.metric, otlpmetricgrpc.WithHeaders(headers))
	}

	return opts, nil
}

// parseHeaders parses a comma-separated list of key=value pairs into a map.
func parseHeaders(raw string) (map[string]string, error) {
	headers := make(map[string]string)
	for pair := range strings.SplitSeq(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("invalid otel header %q: expected key=value", pair)
		}
		headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return headers, nil
}
