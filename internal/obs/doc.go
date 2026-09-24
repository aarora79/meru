// Package obs is Meru's observability: OpenTelemetry metrics and traces,
// exported over OTLP/HTTP to a loopback endpoint the user runs.
//
// Export is off until config sets an endpoint, and Setup refuses any endpoint
// that isn't loopback, so no telemetry leaves the machine. Prompt and response
// text stay out of spans unless capture_content is on. Metric attributes are
// small fixed sets (model, tier, route, outcome), never IDs, paths or text.
// See ARCHITECTURE.md, "Observability".
package obs
