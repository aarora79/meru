// This file serves merud's metrics for Prometheus at /metrics, when
// [observability] prometheus is set. Each request collects every
// instrument once through a manual reader, then writes them in
// Prometheus's text format, version 0.0.4. The format is a few lines per
// series, so it is written here by hand rather than through the
// Prometheus client library and its dependencies. See ARCHITECTURE.md,
// "Reading the metrics".

package obs

import (
	"bufio"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
)

// MetricsHandler returns the handler for /metrics. merud mounts it only
// when [observability] prometheus is set; when Setup built no reader for
// it, every request gets 404.
//
// It answers GET and HEAD on /metrics alone. It refuses a request whose
// Host header names anything but this machine (127.0.0.1, localhost or
// ::1): a web page whose DNS name points at 127.0.0.1 would send its own
// name as the Host, so the check stops that page from reading the metrics
// through the user's browser.
func MetricsHandler(log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		if !loopbackHost(r.Host) {
			log.Debug("metrics refused", "host", r.Host)
			http.Error(w, "merud serves its metrics only to this machine", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "use GET", http.StatusMethodNotAllowed)
			return
		}
		s := current.Load()
		if s == nil || s.manual == nil {
			http.NotFound(w, r)
			return
		}
		// A ResourceMetrics is the SDK's snapshot of every instrument.
		var rm metricdata.ResourceMetrics
		if err := s.manual.Collect(r.Context(), &rm); err != nil {
			http.Error(w, "collect metrics: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if r.Method == http.MethodHead {
			return
		}
		bw := bufio.NewWriter(w)
		writeText(bw, s.res, rm, log)
		_ = bw.Flush()
	})
}

// loopbackHost reports whether a request's Host header, with or without a
// port, names this machine.
func loopbackHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

// family is one metric in the text format: its name, type and help line,
// and its series. Each series is the lines for one set of labels; a
// histogram's series holds its buckets, _sum and _count, in that order.
type family struct {
	name, kind, help string
	series           []series
}

// series is one set of labels' lines. key is the labels, which writeText
// sorts series by.
type series struct {
	key, text string
}

// writeText writes rm in Prometheus's text format: a target_info line for
// the resource, then each metric as a family, sorted by name, each family's
// lines sorted, so the same numbers always give the same text.
//
// It writes sums (as counters or gauges), gauges and histograms with
// explicit buckets, which are every kind Meru records. Any other
// aggregation, such as an exponential histogram, is left out with a debug
// line rather than written half right.
func writeText(w *bufio.Writer, res *resource.Resource, rm metricdata.ResourceMetrics, log *slog.Logger) {
	if res != nil {
		fmt.Fprintf(w, "# HELP target_info The program that reports these metrics.\n# TYPE target_info gauge\ntarget_info%s 1\n",
			labels(*res.Set(), nil))
	}
	families := map[string]*family{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			addMetric(families, m, log)
		}
	}
	names := make([]string, 0, len(families))
	for n := range families {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f := families[n]
		// Sorting by labels keeps the output the same from one scrape to
		// the next; a histogram's own lines keep their bucket order.
		sort.Slice(f.series, func(i, j int) bool { return f.series[i].key < f.series[j].key })
		if f.help != "" {
			fmt.Fprintf(w, "# HELP %s %s\n", f.name, escapeHelp(f.help))
		}
		fmt.Fprintf(w, "# TYPE %s %s\n", f.name, f.kind)
		for _, sr := range f.series {
			// A bufio.Writer keeps its first error and reports it at Flush, so
			// this loop needn't check each write.
			_, _ = w.WriteString(sr.text)
		}
	}
}

// addMetric turns one metric into lines of its family. A type switch picks
// the code for the aggregation the SDK made; Go's generic types
// (metricdata.Sum[int64] and Sum[float64]) each get their own case.
func addMetric(families map[string]*family, m metricdata.Metrics, log *slog.Logger) {
	switch d := m.Data.(type) {
	case metricdata.Sum[int64]:
		f := familyFor(families, m, sumKind(d.IsMonotonic))
		for _, p := range d.DataPoints {
			f.series = append(f.series, series{labels(p.Attributes, nil), sample(f.name, p.Attributes, nil, strconv.FormatInt(p.Value, 10))})
		}
	case metricdata.Sum[float64]:
		f := familyFor(families, m, sumKind(d.IsMonotonic))
		for _, p := range d.DataPoints {
			f.series = append(f.series, series{labels(p.Attributes, nil), sample(f.name, p.Attributes, nil, formatFloat(p.Value))})
		}
	case metricdata.Gauge[int64]:
		f := familyFor(families, m, "gauge")
		for _, p := range d.DataPoints {
			f.series = append(f.series, series{labels(p.Attributes, nil), sample(f.name, p.Attributes, nil, strconv.FormatInt(p.Value, 10))})
		}
	case metricdata.Gauge[float64]:
		f := familyFor(families, m, "gauge")
		for _, p := range d.DataPoints {
			f.series = append(f.series, series{labels(p.Attributes, nil), sample(f.name, p.Attributes, nil, formatFloat(p.Value))})
		}
	case metricdata.Histogram[int64]:
		f := familyFor(families, m, "histogram")
		for _, p := range d.DataPoints {
			f.series = append(f.series, series{labels(p.Attributes, nil), histogram(f.name, p.Attributes, p.Bounds, p.BucketCounts, p.Count, strconv.FormatInt(p.Sum, 10))})
		}
	case metricdata.Histogram[float64]:
		f := familyFor(families, m, "histogram")
		for _, p := range d.DataPoints {
			f.series = append(f.series, series{labels(p.Attributes, nil), histogram(f.name, p.Attributes, p.Bounds, p.BucketCounts, p.Count, formatFloat(p.Sum))})
		}
	default:
		log.Debug("metrics: left out a kind the text format here doesn't write", "metric", m.Name, "kind", fmt.Sprintf("%T", m.Data))
	}
}

// sumKind is the text format's type for a sum: a monotonic sum only goes
// up, which Prometheus calls a counter; any other sum is a gauge.
func sumKind(monotonic bool) string {
	if monotonic {
		return "counter"
	}
	return "gauge"
}

// familyFor returns the family m's samples go in, making it on first use.
func familyFor(families map[string]*family, m metricdata.Metrics, kind string) *family {
	name := promName(m.Name, m.Unit, kind)
	f, ok := families[name]
	if !ok {
		f = &family{name: name, kind: kind, help: m.Description}
		families[name] = f
	}
	return f
}

// unitSuffixes maps OpenTelemetry units to the suffix a Prometheus name
// carries, the same rule the OpenTelemetry Collector follows, so names such
// as gen_ai_server_time_to_first_token_seconds stay what they were. A unit
// in braces, such as {token}, is a count and adds nothing.
var unitSuffixes = map[string]string{
	"s":  "seconds",
	"ms": "milliseconds",
	"By": "bytes",
	"1":  "ratio",
}

// promName turns an OpenTelemetry name and unit into a Prometheus name:
// characters a Prometheus name can't hold become "_", the unit adds its
// suffix, and a counter ends in "_total". So meru.tool.calls becomes
// meru_tool_calls_total, and meru.turn.duration (seconds) becomes
// meru_turn_duration_seconds.
func promName(name, unit, kind string) string {
	n := sanitize(name)
	if s, ok := unitSuffixes[unit]; ok && !strings.HasSuffix(n, "_"+s) {
		n += "_" + s
	}
	if kind == "counter" && !strings.HasSuffix(n, "_total") {
		n += "_total"
	}
	return n
}

// sanitize keeps letters, digits, "_" and ":" and turns anything else into
// "_", so "gen_ai.tool.name" becomes "gen_ai_tool_name". A name can't start
// with a digit, so one that does gets a leading "_".
func sanitize(s string) string {
	var b strings.Builder
	for i, r := range s {
		ok := r == '_' || r == ':' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (i > 0 && r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// sample writes one line: name, labels and value. extra holds labels added
// after the attributes, such as a bucket's le.
func sample(name string, attrs attribute.Set, extra [][2]string, value string) string {
	return name + labels(attrs, extra) + " " + value + "\n"
}

// histogram writes a histogram's lines: one _bucket per bound, in order,
// each counting every value at or under it, then +Inf, _sum and _count. The
// SDK gives a count per bucket, so the loop keeps a running total.
func histogram(name string, attrs attribute.Set, bounds []float64, counts []uint64, count uint64, sum string) string {
	var lines []string
	var total uint64
	for i, b := range bounds {
		if i < len(counts) {
			total += counts[i]
		}
		lines = append(lines, sample(name+"_bucket", attrs, [][2]string{{"le", formatFloat(b)}}, strconv.FormatUint(total, 10)))
	}
	lines = append(lines,
		sample(name+"_bucket", attrs, [][2]string{{"le", "+Inf"}}, strconv.FormatUint(count, 10)),
		sample(name+"_sum", attrs, nil, sum),
		sample(name+"_count", attrs, nil, strconv.FormatUint(count, 10)),
	)
	return strings.Join(lines, "")
}

// labels writes {k="v",...} from attrs and then extra, or "" when there are
// none. An attribute.Set is already sorted by key.
func labels(attrs attribute.Set, extra [][2]string) string {
	var parts []string
	for _, kv := range attrs.ToSlice() {
		parts = append(parts, sanitize(string(kv.Key))+`="`+escapeValue(kv.Value.String())+`"`)
	}
	for _, kv := range extra {
		parts = append(parts, kv[0]+`="`+escapeValue(kv[1])+`"`)
	}
	if len(parts) == 0 {
		return ""
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// escapeValue escapes a label value as the text format requires: a
// backslash, a double quote and a newline each get a backslash.
func escapeValue(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

// escapeHelp escapes a HELP line: a backslash and a newline.
func escapeHelp(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(s)
}

// formatFloat writes a float the way Prometheus reads one, with +Inf, -Inf
// and NaN spelled out.
func formatFloat(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "+Inf"
	case math.IsInf(f, -1):
		return "-Inf"
	case math.IsNaN(f):
		return "NaN"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}
