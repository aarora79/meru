// This file tests analyze_table's arithmetic: reading numbers and dates,
// exact sums and averages, column types, filters and sorting.

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseNumber(t *testing.T) {
	tests := []struct {
		in   string
		want string // "" when it isn't a number
	}{
		{"64.50", "64.50"},
		{"EUR 64.50", "64.50"},
		{"64.50 EUR", "64.50"},
		{"€1,234.50", "1234.50"},
		{"1,234,567", "1234567"},
		{"(12.50)", "-12.50"},
		{"-$3", "-3"},
		{"$-3", "-3"},
		{"+7", "7"},
		{"12.5%", "12.5"},
		{".5", "0.5"},
		{"0.1234567", "0.123457"},
		{"-0.0000005", "-0.000001"},
		{"1.234,56", ""},
		{"12,34", ""},
		{"1,2345", ""},
		{"2026-05-03", ""},
		{"abc", ""},
		{"", ""},
		{"$", ""},
		{"1234567890123456789", ""},
	}
	for _, tt := range tests {
		d, ok := parseNumber(tt.in)
		got := ""
		if ok {
			got = d.String()
		}
		if got != tt.want {
			t.Errorf("parseNumber(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseDate(t *testing.T) {
	day := func(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC) }
	tests := []struct {
		in       string
		dayFirst bool
		want     time.Time
		ok       bool
	}{
		{"2026-05-03", false, day(2026, 5, 3), true},
		{"2026/05/03", true, day(2026, 5, 3), true},
		{"2026-05-03 18:30", false, time.Date(2026, 5, 3, 18, 30, 0, 0, time.UTC), true},
		{"03/04/2026", false, day(2026, 3, 4), true},
		{"03/04/2026", true, day(2026, 4, 3), true},
		{"3/4/26", false, day(2026, 3, 4), true},
		{"31/02/2026", true, time.Time{}, false},
		{"13/01/2026", false, time.Time{}, false},
		{"2026", false, time.Time{}, false},
		{"May 3", false, time.Time{}, false},
	}
	for _, tt := range tests {
		got, ok := parseDate(tt.in, tt.dayFirst)
		if ok != tt.ok || !got.Equal(tt.want) {
			t.Errorf("parseDate(%q, %v) = %v, %v; want %v, %v", tt.in, tt.dayFirst, got, ok, tt.want, tt.ok)
		}
	}
}

// rowsOf builds data rows from cells, numbered from row 2.
func rowsOf(cells ...[]string) []dataRow {
	rows := make([]dataRow, len(cells))
	for i, c := range cells {
		rows[i] = dataRow{n: i + 2, cells: c}
	}
	return rows
}

func TestTypeColumns(t *testing.T) {
	cols := []column{{name: "When", index: 0}, {name: "Amount", index: 1}, {name: "Note", index: 2}}
	rows := rowsOf(
		[]string{"13/04/2026", "1.5", "a"},
		[]string{"01/05/2026", "2.25", "b"},
		[]string{"02/05/2026", "", "3"},
	)
	typeColumns(cols, rows)
	if cols[0].typ != colDate || !cols[0].dayFirst {
		t.Errorf("When = %v, day first %v; want a date read day first", cols[0].typ, cols[0].dayFirst)
	}
	if cols[1].typ != colNumber || cols[1].scale != 2 {
		t.Errorf("Amount = %v at scale %d; want a number at scale 2", cols[1].typ, cols[1].scale)
	}
	if cols[2].typ != colText {
		t.Errorf("Note = %v, want text", cols[2].typ)
	}
}

// aggregate runs one aggregate over values in one column and returns its
// text, or the error.
func aggregate(t *testing.T, fn string, values ...string) (string, error) {
	t.Helper()
	col := column{name: "Amount", index: 0}
	var rows []dataRow
	for i, v := range values {
		rows = append(rows, dataRow{n: i + 2, cells: []string{v}})
	}
	cols := []column{col}
	typeColumns(cols, rows)
	a := agg{fn: fn, col: &cols[0]}
	groups, err := groupRows(context.Background(), rows, nil, []agg{a})
	if err != nil {
		t.Fatal(err)
	}
	text, _, err := groups[0].aggs[0].result(a)
	return text, err
}

func TestExactSums(t *testing.T) {
	tests := []struct {
		fn     string
		values []string
		want   string
	}{
		{"sum", []string{"0.1", "0.2"}, "0.3"},
		{"sum", []string{"18.99", "66", "EUR 0.01"}, "85.00"},
		{"sum", []string{"10", "-2.5", "(1.25)"}, "6.25"},
		{"avg", []string{"1", "2", "2"}, "1.67"},
		{"avg", []string{"-1", "-2", "-2"}, "-1.67"},
		{"avg", []string{"0.10", "0.20"}, "0.1500"},
		{"min", []string{"3", "-1.5", "2"}, "-1.5"},
		{"max", []string{"3", "-1.5", "x"}, "3"},
		{"count", []string{"3", "", "x"}, "2"},
	}
	for _, tt := range tests {
		got, err := aggregate(t, tt.fn, tt.values...)
		if err != nil || got != tt.want {
			t.Errorf("%s(%q) = %q, %v; want %q", tt.fn, tt.values, got, err, tt.want)
		}
	}
	// A thousand cents add up to the cent.
	cents := make([]string, 1000)
	for i := range cents {
		cents[i] = "0.01"
	}
	if got, _ := aggregate(t, "sum", cents...); got != "10.00" {
		t.Errorf("sum of a thousand cents = %q, want 10.00", got)
	}
	// Ten values of 18 nines pass an int64's 9.2 × 10^18.
	big := make([]string, 10)
	for i := range big {
		big[i] = "999999999999999999"
	}
	if _, err := aggregate(t, "sum", big...); !errors.Is(err, errOverflow) {
		t.Errorf("a sum past int64 = %v, want errOverflow", err)
	}
}

func TestQuantile(t *testing.T) {
	nums := func(values ...string) []decimal {
		out := make([]decimal, len(values))
		for i, v := range values {
			out[i], _ = parseNumber(v)
		}
		return out
	}
	// The stars file's daily column, 248 days: 88 ones, 70 twos, 38 threes,
	// 24 fours, 14 fives, 6 sixes, a seven, 4 eights, a ten, a 12 and a 20.
	var stars []string
	for _, run := range []struct {
		value string
		n     int
	}{{"1", 88}, {"2", 70}, {"3", 38}, {"4", 24}, {"5", 14}, {"6", 6}, {"7", 1}, {"8", 4}, {"10", 1}, {"12", 1}, {"20", 1}} {
		for range run.n {
			stars = append(stars, run.value)
		}
	}
	tests := []struct {
		name   string
		values []string
		p      int64
		want   string
	}{
		{"median of an odd count", []string{"3", "1", "2"}, 5000, "2"},
		{"median of an even count", []string{"1", "2", "3", "4"}, 5000, "2.5"},
		{"median keeps the values' places", []string{"2.50", "2.50"}, 5000, "2.50"},
		{"first quartile", []string{"1", "2", "3", "4"}, 2500, "1.75"},
		{"p0 and p100", []string{"5", "9"}, 0, "5"},
		{"p100", []string{"5", "9"}, 10000, "9"},
		{"one value", []string{"7"}, 2500, "7"},
		{"stars median", stars, 5000, "2"},
		{"stars p25", stars, 2500, "1"},
		{"stars p75", stars, 7500, "3"},
	}
	for _, tt := range tests {
		got, _, err := quantile(nums(tt.values...), tt.p)
		if err != nil || got != tt.want {
			t.Errorf("%s: quantile = %q, %v; want %q", tt.name, got, err, tt.want)
		}
	}
	if got, _, _ := quantile(nil, 5000); got != "" {
		t.Errorf("quantile of nothing = %q, want empty", got)
	}
	if h := (agg{fn: "percentile", p: 250, col: &column{name: "Amount"}}).header(); h != "p2.5(Amount)" {
		t.Errorf("header = %q, want p2.5(Amount)", h)
	}
}

func TestStringList(t *testing.T) {
	tests := map[string][]string{
		`["City","Year"]`:  {"City", "Year"},
		`"City"`:           {"City"},
		`"[\"City\"]"`:     {"City"},
		`"[daily]"`:        {"daily"},
		`"[City, 'Year']"`: {"City", "Year"},
		`""`:               nil,
	}
	for in, want := range tests {
		var got stringList
		if err := json.Unmarshal([]byte(in), &got); err != nil || !slices.Equal(got, want) {
			t.Errorf("stringList from %s = %q, %v; want %q", in, got, err, want)
		}
	}
	var got stringList
	if err := json.Unmarshal([]byte(`7`), &got); err == nil {
		t.Error("stringList took a number")
	}
}

func TestObjectList(t *testing.T) {
	tests := map[string][]aggArg{
		`[{"fn":"count"}]`:                                           {{Fn: "count"}},
		`"[{\"fn\":\"sum\",\"column\":\"Amount\"}]"`:                 {{Fn: "sum", Column: "Amount"}},
		`"[{\"column\": Number, \"fn\": count}]"`:                    {{Fn: "count", Column: "Number"}},
		`"[{\"fn\": percentile, \"column\": Total Due, \"p\": 90}]"`: {{Fn: "percentile", Column: "Total Due", P: new(90.0)}},
	}
	for in, want := range tests {
		var got objectList[aggArg]
		if err := json.Unmarshal([]byte(in), &got); err != nil || len(got) != len(want) {
			t.Errorf("objectList from %s = %+v, %v; want %+v", in, got, err, want)
			continue
		}
		for i := range want {
			g, w := got[i], want[i]
			if g.Fn != w.Fn || g.Column != w.Column || (w.P != nil && (g.P == nil || *g.P != *w.P)) {
				t.Errorf("objectList from %s [%d] = %+v, want %+v", in, i, g, w)
			}
		}
	}
	var got objectList[aggArg]
	err := json.Unmarshal([]byte(`"count the rows"`), &got)
	if err == nil || !strings.Contains(err.Error(), "send it as JSON") {
		t.Errorf("a string that holds no list: err = %v, want a hint", err)
	}
}

func TestMatch(t *testing.T) {
	city := column{name: "City", index: 0}
	amount := column{name: "Amount", index: 1}
	when := column{name: "When", index: 2}
	row := []string{" Amsterdam ", "EUR 64.50", "2026-05-03"}
	tests := []struct {
		m    match
		want bool
	}{
		{match{col: city, op: "=", values: []string{"amsterdam"}}, true},
		{match{col: city, op: "!=", values: []string{"Rome"}}, true},
		{match{col: city, op: "contains", values: []string{"STER"}}, true},
		{match{col: city, op: "starts_with", values: []string{"ams"}}, true},
		{match{col: city, op: "in", values: []string{"Rome", "Amsterdam"}}, true},
		{match{col: amount, op: "=", values: []string{"64.5"}}, true},
		{match{col: amount, op: ">", values: []string{"64.49"}}, true},
		{match{col: amount, op: "<", values: []string{"9"}}, false},
		{match{col: when, op: ">=", values: []string{"2026-05-03"}}, true},
		{match{col: when, op: "<", values: []string{"2026-05-01"}}, false},
		{match{col: column{index: 5}, op: "empty"}, true},
		{match{col: city, op: "not_empty"}, true},
		{match{col: column{index: 5}, op: ">", values: []string{"1"}}, false},
	}
	for _, tt := range tests {
		if got := tt.m.holds(row); got != tt.want {
			t.Errorf("%s %s %q = %v, want %v", tt.m.col.name, tt.m.op, tt.m.values, got, tt.want)
		}
	}
}

func TestSortRows(t *testing.T) {
	col := &column{name: "Amount"}
	rows := []outRow{{cells: []string{"9"}}, {cells: []string{""}}, {cells: []string{"10"}}, {cells: []string{"2"}}}
	sortRows(rows, []sortBy{{index: 0, desc: true, col: col}})
	got := []string{rows[0].cells[0], rows[1].cells[0], rows[2].cells[0], rows[3].cells[0]}
	want := []string{"10", "9", "2", ""}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted largest first = %q, want %q", got, want)
		}
	}
}

func TestFilterCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := filterRows(ctx, rowsOf([]string{"a"}), nil); !errors.Is(err, context.Canceled) {
		t.Errorf("filterRows after cancel = %v, want context.Canceled", err)
	}
}
