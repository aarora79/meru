// This file turns the [observability] section of config.toml into running
// OpenTelemetry exporters: it checks that the endpoint is loopback, builds the
// metric and trace pipelines, installs them as the process-wide providers,
// and hands back a function that flushes them on exit.

package obs

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"github.com/aarora79/meru/internal/config"
)

// scopeName names the code that produces Meru's metrics and spans. Backends
// show it as the "instrumentation scope".
const scopeName = "github.com/aarora79/meru"

// defaultInterval is how often metrics go out when config leaves
// metrics_interval empty. It matches the value in ARCHITECTURE.md.
const defaultInterval = 10 * time.Second

// setup does the work behind Setup. See Setup for the contract.
//
// It returns two values, a function and an error; Go functions may return
// several values, and by convention the error comes last.
func setup(ctx context.Context, cfg config.Observability) (func(context.Context) error, error) {
	// noop is a shutdown function with nothing to flush. A function literal
	// like this one is a value, so it can be stored and returned.
	noop := func(context.Context) error { return nil }

	if cfg.OTLPEndpoint == "" {
		// Export is off. OpenTelemetry's global providers start as no-ops,
		// so there is nothing to install. Keep capture_content anyway, so
		// CaptureContent reports what config says.
		current.Store(&state{captureContent: cfg.CaptureContent})
		return noop, nil
	}

	// Check the endpoint before anything else, so a bad address never gets
	// as far as an exporter. This is the "Local only" rule in
	// ARCHITECTURE.md, "Observability".
	u, err := loopbackURL(ctx, cfg.OTLPEndpoint)
	// `if err != nil` is Go's error check: a non-nil error means the call
	// failed, and we pass it up with context added.
	if err != nil {
		return nil, err
	}

	interval := defaultInterval
	if cfg.MetricsInterval != "" {
		interval, err = time.ParseDuration(cfg.MetricsInterval)
		if err != nil {
			return nil, fmt.Errorf("observability.metrics_interval %q: %w", cfg.MetricsInterval, err)
		}
		if interval <= 0 {
			return nil, fmt.Errorf("observability.metrics_interval %q: must be above zero", cfg.MetricsInterval)
		}
	}

	// The resource says which program sent the data. Backends turn
	// service.name into a label, so the dashboard can filter on "merud".
	// NewWithAttributes skips the environment detectors on purpose: an
	// OTEL_RESOURCE_ATTRIBUTES variable shouldn't be able to change what
	// merud reports about itself.
	res := resource.NewWithAttributes(semconv.SchemaURL,
		semconv.ServiceName("merud"),
		semconv.ServiceVersion(buildVersion()),
	)

	metricExporter, err := otlpmetrichttp.New(ctx, metricOptions(u)...)
	if err != nil {
		return nil, fmt.Errorf("create OTLP metric exporter: %w", err)
	}
	// The periodic reader collects every instrument each interval and hands
	// the batch to the exporter, which POSTs it to <endpoint>/v1/metrics.
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(interval))),
	)

	// tracerProvider stays nil when traces are off. A nil pointer is Go's
	// "no value"; the shutdown function below checks for it.
	var tracerProvider *sdktrace.TracerProvider
	if cfg.Traces {
		traceExporter, err := otlptracehttp.New(ctx, traceOptions(u)...)
		if err != nil {
			// Undo the metric side before failing, so nothing is left running.
			return nil, errors.Join(fmt.Errorf("create OTLP trace exporter: %w", err), meterProvider.Shutdown(ctx))
		}
		// The batcher holds finished spans and sends them in groups, so a
		// turn never waits on the network.
		tracerProvider = sdktrace.NewTracerProvider(
			sdktrace.WithResource(res),
			sdktrace.WithBatcher(traceExporter),
		)
	}

	inst, err := newInstruments(meterProvider.Meter(scopeName))
	if err == nil {
		// The runtime package adds heap, garbage-collection and goroutine
		// metrics, read from the Go runtime on each collection.
		err = runtime.Start(runtime.WithMeterProvider(meterProvider))
	}
	if err != nil {
		shutdownErr := meterProvider.Shutdown(ctx)
		if tracerProvider != nil {
			shutdownErr = errors.Join(shutdownErr, tracerProvider.Shutdown(ctx))
		}
		return nil, errors.Join(fmt.Errorf("start metrics: %w", err), shutdownErr)
	}

	// Install the providers process-wide. Tracer() and any library that
	// asks otel for a provider now get these.
	otel.SetMeterProvider(meterProvider)
	if tracerProvider != nil {
		otel.SetTracerProvider(tracerProvider)
	}
	current.Store(&state{inst: inst, captureContent: cfg.CaptureContent})

	shutdown := func(ctx context.Context) error {
		// Stop recording first, so nothing lands in a provider that is
		// shutting down. Shutdown on each provider sends what it still holds.
		current.Store(nil)
		err := meterProvider.Shutdown(ctx)
		if tracerProvider != nil {
			err = errors.Join(err, tracerProvider.Shutdown(ctx))
		}
		return err
	}
	return shutdown, nil
}

// loopbackURL parses endpoint and returns it when its host is a loopback
// address. It accepts a literal loopback IP (127.0.0.0/8 or ::1) or the name
// "localhost", and only when every address localhost resolves to is
// loopback. It refuses every other host name without looking it up: a name
// that points at loopback today can point somewhere else tomorrow.
func loopbackURL(ctx context.Context, endpoint string) (*url.URL, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("observability.otlp_endpoint %q: %w", endpoint, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("observability.otlp_endpoint %q: must start with http:// or https://", endpoint)
	}
	host := u.Hostname() // strips the port and the brackets around an IPv6 address
	if host == "" {
		return nil, fmt.Errorf("observability.otlp_endpoint %q: no host", endpoint)
	}
	refuse := fmt.Errorf("observability.otlp_endpoint %q: host %q is not loopback; "+
		"Meru sends telemetry only to this machine (use 127.0.0.1, ::1 or localhost)", endpoint, host)

	if strings.EqualFold(host, "localhost") {
		addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("observability.otlp_endpoint %q: resolve localhost: %w", endpoint, err)
		}
		if len(addrs) == 0 {
			return nil, refuse
		}
		// `for _, a := range addrs` visits each element; `_` discards the index.
		for _, a := range addrs {
			if !a.IsLoopback() {
				return nil, refuse
			}
		}
		return u, nil
	}

	addr, err := netip.ParseAddr(host)
	if err != nil || !addr.IsLoopback() {
		return nil, refuse
	}
	return u, nil
}

// signalPath returns the URL path for one OTLP signal ("metrics" or
// "traces"). OTLP/HTTP puts each signal under <base>/v1/<signal>, so
// http://127.0.0.1:4318 sends metrics to /v1/metrics.
func signalPath(u *url.URL, signal string) string {
	return strings.TrimSuffix(u.Path, "/") + "/v1/" + signal
}

// noProxy tells an exporter's HTTP client never to use a proxy. Go's default
// client reads HTTPS_PROXY from the environment; the loopback check above
// would mean nothing if a proxy then carried the data somewhere else.
func noProxy(*http.Request) (*url.URL, error) { return nil, nil }

// metricOptions builds the metric exporter's options for endpoint u. Options
// set in code override any OTEL_EXPORTER_OTLP_* environment variables, so the
// endpoint that passed the loopback check is the one the exporter uses.
func metricOptions(u *url.URL) []otlpmetrichttp.Option {
	opts := []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpoint(u.Host),
		otlpmetrichttp.WithURLPath(signalPath(u, "metrics")),
		otlpmetrichttp.WithProxy(noProxy),
	}
	if u.Scheme == "http" {
		opts = append(opts, otlpmetrichttp.WithInsecure())
	}
	return opts
}

// traceOptions is metricOptions for the trace exporter. The two exporter
// packages define separate option types, so the list can't be shared.
func traceOptions(u *url.URL) []otlptracehttp.Option {
	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(u.Host),
		otlptracehttp.WithURLPath(signalPath(u, "traces")),
		otlptracehttp.WithProxy(noProxy),
	}
	if u.Scheme == "http" {
		opts = append(opts, otlptracehttp.WithInsecure())
	}
	return opts
}

// buildVersion returns merud's version as the Go toolchain recorded it in
// the binary: a tag such as "v0.1.0" for `go install …@v0.1.0`, or
// "(devel)" for a local build. It avoids a version variable that someone
// must remember to bump.
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" {
		return "(devel)"
	}
	return info.Main.Version
}
