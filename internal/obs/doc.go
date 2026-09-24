// Package obs is Meru's observability: OpenTelemetry metrics and traces,
// exported over OTLP/HTTP to a loopback endpoint the user runs.
//
// Export is off until config sets an endpoint, and Setup refuses any endpoint
// that isn't loopback, so no telemetry leaves the machine. Prompt and response
// text stay out of spans unless capture_content is on. Metric attributes are
// small fixed sets (model, tier, route, outcome), never IDs, paths or text;
// a value outside its set is recorded as "other".
//
// The package doesn't export logs. merud's slog file stays on disk, and
// LogHandler stamps each of its lines with the turn's trace ID, so a log line
// leads to its trace. With trace export off, spans record nothing but still
// get a trace ID for that purpose.
// See ARCHITECTURE.md, "Observability".
package obs
