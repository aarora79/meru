// This file holds analyze_table's arithmetic: reading a cell as a number,
// a date or text; exact decimal sums; the filters; grouping; the five
// aggregates; sorting; and the Markdown table the tool returns. table.go
// holds the tool itself.

package builtin

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// maxScale is the most decimal places a number keeps. A value with more is
// rounded to six, which still keeps a column of cents exact.
const maxScale = 6

// pow10 holds 10 to the power i, for i up to 18, the largest that fits in
// an int64.
var pow10 = [19]int64{1, 10, 100, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9, 1e10, 1e11, 1e12,
	1e13, 1e14, 1e15, 1e16, 1e17, 1e18}

// decimal is an exact number: units divided by 10 to the scale, so 64.50
// is {6450, 2}. Money added up in float64 picks up errors: 0.1 + 0.2 is
// 0.30000000000000004 there. Adding whole units of the smallest decimal
// place, as integers, gives 0.30.
type decimal struct {
	units int64
	scale int
}

// errOverflow means a sum passed the range an int64 holds.
var errOverflow = errors.New("the total is too large to add up exactly")

// float returns d as a float64, for ordering only.
func (d decimal) float() float64 {
	return float64(d.units) / float64(pow10[d.scale])
}

// rescale returns d's units at scale s, which must be d.scale or more. It
// fails with errOverflow when they don't fit an int64.
func (d decimal) rescale(s int) (int64, error) {
	f := pow10[s-d.scale]
	if f == 1 {
		return d.units, nil
	}
	if d.units > math.MaxInt64/f || d.units < math.MinInt64/f {
		return 0, errOverflow
	}
	return d.units * f, nil
}

// cmpDecimal compares a and b exactly, and falls back to their float
// values when lining up their scales would overflow.
func cmpDecimal(a, b decimal) int {
	s := max(a.scale, b.scale)
	x, err1 := a.rescale(s)
	y, err2 := b.rescale(s)
	if err1 != nil || err2 != nil {
		return cmpFloat(a.float(), b.float())
	}
	return cmpInt(x, y)
}

// cmpInt and cmpFloat return -1, 0 or 1 as a is less than, equal to or
// more than b.
func cmpInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// String writes d with its decimal places, such as "-1234.50".
func (d decimal) String() string {
	return formatUnits(d.units, d.scale)
}

// formatUnits writes units at scale s as a decimal number.
func formatUnits(units int64, s int) string {
	// FormatInt writes the sign itself; take it off, and put it back in
	// front once the decimal point is in.
	digits := strconv.FormatInt(units, 10)
	neg := strings.HasPrefix(digits, "-")
	digits = strings.TrimPrefix(digits, "-")
	if s > 0 {
		for len(digits) <= s {
			digits = "0" + digits
		}
		digits = digits[:len(digits)-s] + "." + digits[len(digits)-s:]
	}
	if neg {
		return "-" + digits
	}
	return digits
}

// currencyCodes are three-letter codes a number may carry before or after
// it, such as "EUR 64.50". Any three capital letters count, so an unlisted
// currency reads too.
func isCurrencyCode(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// parseNumber reads s as a number. It accepts a currency symbol ($ € £ ¥ ₹)
// or a three-letter code before or after the digits, thousands commas in
// groups of three, a minus sign or parentheses for a negative, a leading
// plus, and a trailing percent sign, which keeps the number as written
// (12.5% reads 12.5). A dot is the decimal point, so "1.234,56" stays text.
// It reports false for anything else.
func parseNumber(s string) (decimal, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return decimal{}, false
	}
	neg := false
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		neg, s = true, strings.TrimSpace(s[1:len(s)-1])
	}
	// A code first or last, with a space: "EUR 64.50", "64.50 EUR".
	if f := strings.Fields(s); len(f) == 2 {
		switch {
		case isCurrencyCode(f[0]):
			s = f[1]
		case isCurrencyCode(f[1]):
			s = f[0]
		}
	}
	s = strings.TrimSuffix(s, "%")
	if strings.HasPrefix(s, "-") || strings.HasPrefix(s, "−") {
		neg = !neg
		s = strings.TrimPrefix(strings.TrimPrefix(s, "-"), "−")
	}
	s = strings.TrimPrefix(s, "+")
	s = strings.TrimLeft(s, "$€£¥₹")
	s = strings.TrimRight(s, "$€£¥₹")
	if strings.HasPrefix(s, "-") && !neg {
		neg, s = true, s[1:] // "$-12" as well as "-$12"
	}
	s = strings.TrimSpace(s)
	whole, frac, hasDot := strings.Cut(s, ".")
	if whole == "" && frac == "" {
		return decimal{}, false
	}
	whole, ok := dropThousands(whole)
	if !ok || !allDigits(whole) || !allDigits(frac) || (hasDot && frac == "" && whole == "") {
		return decimal{}, false
	}
	if whole == "" {
		whole = "0"
	}
	// Round past maxScale places, half away from zero.
	roundUp := false
	if len(frac) > maxScale {
		roundUp = frac[maxScale] >= '5'
		frac = frac[:maxScale]
	}
	if len(whole)+len(frac) > 18 {
		return decimal{}, false // past what an int64 holds exactly
	}
	units, err := strconv.ParseInt(whole+frac, 10, 64)
	if err != nil {
		return decimal{}, false
	}
	if roundUp {
		units++
	}
	if neg {
		units = -units
	}
	return decimal{units: units, scale: len(frac)}, true
}

// dropThousands removes the commas from whole when they sit between groups
// of three digits, as in 1,234,567, and reports false when a comma sits
// anywhere else.
func dropThousands(whole string) (string, bool) {
	if !strings.Contains(whole, ",") {
		return whole, true
	}
	groups := strings.Split(whole, ",")
	if len(groups[0]) == 0 || len(groups[0]) > 3 {
		return "", false
	}
	for _, g := range groups[1:] {
		if len(g) != 3 {
			return "", false
		}
	}
	return strings.Join(groups, ""), true
}

// allDigits reports whether s holds only the digits 0 to 9.
func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// dateLayouts are the forms a date may take with its year first.
var dateLayouts = []string{
	"2006-01-02", "2006-01-02 15:04", "2006-01-02 15:04:05", "2006-01-02T15:04:05",
	"2006-01-02T15:04:05Z07:00", "2006/01/02", "2006/01/02 15:04",
}

// parseDate reads s as a date. A year-first date, such as 2026-05-03 or
// 2026/05/03, with or without a time, always reads. A slash date with the
// year last, such as 03/04/2026, reads day first when dayFirst is true and
// month first otherwise; a two-digit year counts from 2000.
func parseDate(s string, dayFirst bool) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if len(s) < 6 || !unicode.IsDigit(rune(s[0])) {
		return time.Time{}, false
	}
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	a, b, y, ok := slashParts(s)
	if !ok {
		return time.Time{}, false
	}
	if y < 100 {
		y += 2000
	}
	day, month := b, a
	if dayFirst {
		day, month = a, b
	}
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return time.Time{}, false
	}
	t := time.Date(y, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if t.Day() != day {
		return time.Time{}, false // 31/02 rolled over into March
	}
	return t, true
}

// slashParts splits a date such as 03/04/2026 into its three numbers, and
// reports false for anything else.
func slashParts(s string) (a, b, y int, ok bool) {
	parts := strings.Split(s, "/")
	if len(parts) != 3 || len(parts[2]) != 2 && len(parts[2]) != 4 {
		return 0, 0, 0, false
	}
	var n [3]int
	for i, p := range parts {
		if p == "" || len(p) > 4 || !allDigits(p) {
			return 0, 0, 0, false
		}
		n[i], _ = strconv.Atoi(p)
	}
	return n[0], n[1], n[2], true
}

// formatDate writes t as 2006-01-02, with the time when it has one.
func formatDate(t time.Time) string {
	if t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 {
		return t.Format("2006-01-02")
	}
	return t.Format("2006-01-02 15:04")
}

// colType is what a column holds, for the description and for min and max.
type colType int

// The column types. iota counts up from 0 inside a const block.
const (
	colText colType = iota
	colNumber
	colDate
)

// String names t for the description.
func (t colType) String() string {
	switch t {
	case colNumber:
		return "number"
	case colDate:
		return "date"
	}
	return "text"
}

// column is one column of the sheet, as the query sees it.
type column struct {
	name     string
	letter   string
	index    int
	typ      colType
	dayFirst bool // slash dates in this column read day first
	scale    int  // the most decimal places among its numbers
}

// cell returns column c's text in row, or "" past the row's end.
func (c column) cell(row []string) string {
	if c.index < len(row) {
		return strings.TrimSpace(row[c.index])
	}
	return ""
}

// typeColumns works out each column's type from its values in rows: a
// date or a number when at least nine in ten of its non-empty values read
// as one, and text otherwise. A column whose slash dates have a first part
// above 12 reads them day first.
func typeColumns(cols []column, rows []dataRow) {
	for i := range cols {
		c := &cols[i]
		n, dates, numbers := 0, 0, 0
		for _, r := range rows {
			v := c.cell(r.cells)
			if v == "" {
				continue
			}
			n++
			if a, _, _, ok := slashParts(v); ok && a > 12 {
				c.dayFirst = true
			}
			if d, ok := parseNumber(v); ok {
				numbers++
				c.scale = max(c.scale, d.scale)
			}
		}
		for _, r := range rows {
			if v := c.cell(r.cells); v != "" {
				if _, ok := parseDate(v, c.dayFirst); ok {
					dates++
				}
			}
		}
		switch {
		case n == 0:
		case dates*10 >= n*9:
			c.typ = colDate
		case numbers*10 >= n*9:
			c.typ = colNumber
		}
	}
}

// dataRow is one row of the sheet below the header: its number, counting
// from 1 as Excel does, and its cells.
type dataRow struct {
	n     int
	cells []string
}

// match is one compiled condition.
type match struct {
	col    column
	op     string
	values []string
}

// ops are the condition operators analyze_table takes.
var ops = []string{"=", "!=", "<", "<=", ">", ">=", "contains", "starts_with", "empty", "not_empty", "in"}

// holds reports whether row satisfies m.
func (m match) holds(row []string) bool {
	v := m.col.cell(row)
	switch m.op {
	case "empty":
		return v == ""
	case "not_empty":
		return v != ""
	case "=", "in":
		for _, want := range m.values {
			if equal(v, want, m.col) {
				return true
			}
		}
		return false
	case "!=":
		return !equal(v, m.values[0], m.col)
	case "contains":
		return strings.Contains(strings.ToLower(v), strings.ToLower(m.values[0]))
	case "starts_with":
		return strings.HasPrefix(strings.ToLower(v), strings.ToLower(m.values[0]))
	}
	if v == "" {
		return false
	}
	c := compare(v, m.values[0], m.col)
	switch m.op {
	case "<":
		return c < 0
	case "<=":
		return c <= 0
	case ">":
		return c > 0
	case ">=":
		return c >= 0
	}
	return false
}

// equal compares a cell with a wanted value: as numbers when both read as
// numbers, as dates when both read as dates, and otherwise as text,
// ignoring case and the spaces around it.
func equal(v, want string, c column) bool {
	want = strings.TrimSpace(want)
	if a, ok := parseNumber(v); ok {
		if b, ok := parseNumber(want); ok {
			return cmpDecimal(a, b) == 0
		}
	}
	if a, ok := parseDate(v, c.dayFirst); ok {
		if b, ok := parseDate(want, c.dayFirst); ok {
			return a.Equal(b)
		}
	}
	return strings.EqualFold(v, want)
}

// compare orders a cell against a value: numbers first, then dates, then
// text, ignoring case.
func compare(v, want string, c column) int {
	want = strings.TrimSpace(want)
	if a, ok := parseNumber(v); ok {
		if b, ok := parseNumber(want); ok {
			return cmpDecimal(a, b)
		}
	}
	if a, ok := parseDate(v, c.dayFirst); ok {
		if b, ok := parseDate(want, c.dayFirst); ok {
			return a.Compare(b)
		}
	}
	return strings.Compare(strings.ToLower(v), strings.ToLower(want))
}

// agg is one aggregate the query computes per group.
type agg struct {
	fn  string
	col *column // nil for a count of rows
	p   int64   // for percentile: hundredths of a percent, 0 to 10,000
}

// header is the aggregate's column title in the result, such as
// "sum(Amount)", "count" or, for a percentile, "p25(Amount)".
func (a agg) header() string {
	fn := a.fn
	if fn == "percentile" {
		fn = "p" + formatUnits(a.p, 2)
		fn = strings.TrimSuffix(strings.TrimRight(fn, "0"), ".")
	}
	if a.col == nil {
		return fn
	}
	return fn + "(" + a.col.name + ")"
}

// aggState adds up one aggregate for one group as rows arrive.
type aggState struct {
	count   int     // rows, or non-empty values for count(column)
	sum     int64   // the sum's units at scale
	scale   int     // the scale sum counts in
	n       int     // numbers added to sum
	best    decimal // min or max so far, for numbers
	bestT   time.Time
	hasBest bool
	skipped int       // non-empty values that weren't numbers (or dates)
	values  []decimal // every number, for median and percentile
	err     error
}

// add takes one row's value into the state for a.
func (s *aggState) add(a agg, row []string) {
	if a.col == nil {
		s.count++
		return
	}
	v := a.col.cell(row)
	if v == "" {
		return
	}
	if a.fn == "count" {
		s.count++
		return
	}
	if (a.fn == "min" || a.fn == "max") && a.col.typ == colDate {
		t, ok := parseDate(v, a.col.dayFirst)
		if !ok {
			s.skipped++
			return
		}
		if !s.hasBest || (a.fn == "min" && t.Before(s.bestT)) || (a.fn == "max" && t.After(s.bestT)) {
			s.bestT, s.hasBest = t, true
		}
		return
	}
	d, ok := parseNumber(v)
	if !ok {
		s.skipped++
		return
	}
	switch a.fn {
	case "median", "percentile":
		s.values = append(s.values, d)
	case "sum", "avg":
		if s.err != nil {
			return
		}
		units, err := d.rescale(s.scale)
		if err == nil {
			next := s.sum + units
			// Signed overflow wraps around; the sign of the result shows it.
			if (units > 0 && next < s.sum) || (units < 0 && next > s.sum) {
				err = errOverflow
			}
			s.sum = next
		}
		s.err = err
		s.n++
	case "min", "max":
		c := 0
		if s.hasBest {
			c = cmpDecimal(d, s.best)
		}
		if !s.hasBest || (a.fn == "min" && c < 0) || (a.fn == "max" && c > 0) {
			s.best, s.hasBest = d, true
		}
	}
}

// result returns the aggregate's value as text, and a number for sorting.
// It fails when a sum overflowed.
func (s *aggState) result(a agg) (string, float64, error) {
	switch a.fn {
	case "count":
		return strconv.Itoa(s.count), float64(s.count), nil
	case "sum":
		if s.err != nil {
			return "", 0, s.err
		}
		return formatUnits(s.sum, s.scale), float64(s.sum) / float64(pow10[s.scale]), nil
	case "avg":
		if s.err != nil {
			return "", 0, s.err
		}
		if s.n == 0 {
			return "", math.Inf(-1), nil
		}
		// Two more decimal places than the column, rounded half away
		// from zero, all in integers.
		scale := min(s.scale+2, 18)
		num, err := decimal{units: s.sum, scale: s.scale}.rescale(scale)
		if err != nil {
			return "", 0, err
		}
		q, r := num/int64(s.n), num%int64(s.n)
		if 2*abs(r) >= int64(s.n) {
			if num < 0 {
				q--
			} else {
				q++
			}
		}
		return formatUnits(q, scale), float64(q) / float64(pow10[scale]), nil
	case "median":
		return quantile(s.values, 5000)
	case "percentile":
		return quantile(s.values, a.p)
	case "min", "max":
		if !s.hasBest {
			return "", math.Inf(-1), nil
		}
		if a.col.typ == colDate {
			return formatDate(s.bestT), float64(s.bestT.Unix()), nil
		}
		return s.best.String(), s.best.float(), nil
	}
	return "", 0, nil
}

// quantile returns the value at p hundredths of a percent through values,
// so 5,000 is the median and 2,500 the first quartile, with its number for
// sorting. It sorts values in place. Between two values it interpolates as
// Python's statistics.quantiles(method="inclusive") and most spreadsheets'
// PERCENTILE do: the position is (n-1) × p, and a fraction of the way from
// one value to the next gives that fraction of the gap. The arithmetic stays
// in integers, four decimal places past the values' own, so the result is
// exact; trailing zeros come off. It fails when the numbers are too large
// to line up.
func quantile(values []decimal, p int64) (string, float64, error) {
	if len(values) == 0 {
		return "", math.Inf(-1), nil
	}
	slices.SortFunc(values, cmpDecimal)
	scale := 0
	for _, v := range values {
		scale = max(scale, v.scale)
	}
	pos := int64(len(values)-1) * p
	lo, frac := pos/10_000, pos%10_000
	x, err := values[lo].rescale(scale)
	if err != nil {
		return "", 0, err
	}
	units, outScale := x, scale
	if frac > 0 {
		y, err := values[lo+1].rescale(scale)
		if err != nil {
			return "", 0, err
		}
		gap := y - x
		if abs(x) > math.MaxInt64/10_000 || abs(gap) > math.MaxInt64/10_000 {
			return "", 0, errOverflow
		}
		units, outScale = x*10_000+gap*frac, scale+4
	}
	// Drop the zeros the interpolation added, keeping the values' own
	// decimal places: a median of 1 and 2 is 1.5, of 2 and 2 is 2.
	for outScale > scale && units%10 == 0 {
		units /= 10
		outScale--
	}
	return formatUnits(units, outScale), float64(units) / float64(pow10[outScale]), nil
}

// abs returns the absolute value of n.
func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// group is the rows that share one value in each group_by column.
type group struct {
	key   []string   // the group's values, as first seen
	aggs  []aggState // one per aggregate
	first int        // the first row number in the group, to keep order stable
}

// outRow is one row of the result: its cells as text, the numbers to sort
// aggregates by, and the sheet row it came from (0 for a group).
type outRow struct {
	cells []string
	nums  []float64
	row   int
}

// sortRows sorts rows by keys, each the index of a result column and
// whether to sort it descending. A column of numbers sorts by value, a
// column of dates by date, and any other by text, ignoring case. Empty
// cells go last. Rows that tie keep their order.
func sortRows(rows []outRow, keys []sortBy) {
	slices.SortStableFunc(rows, func(a, b outRow) int {
		for _, k := range keys {
			x, y := a.cells[k.index], b.cells[k.index]
			// An empty cell sorts last either way: "largest first" means
			// the largest values, not the blanks.
			if (x == "") != (y == "") {
				if x == "" {
					return 1
				}
				return -1
			}
			c := 0
			if k.numeric {
				c = cmpFloat(a.nums[k.index], b.nums[k.index])
			} else {
				c = compareCells(x, y, k.col)
			}
			if k.desc {
				c = -c
			}
			if c != 0 {
				return c
			}
		}
		return 0
	})
}

// sortBy is one compiled sort key.
type sortBy struct {
	index   int
	desc    bool
	numeric bool    // an aggregate: sort by outRow.nums
	col     *column // the sheet column behind it, for a plain column
}

// compareCells orders two non-empty cells of the same column: numbers by
// value, dates by date, and text ignoring case.
func compareCells(a, b string, col *column) int {
	if x, ok := parseNumber(a); ok {
		if y, ok := parseNumber(b); ok {
			return cmpDecimal(x, y)
		}
	}
	dayFirst := col != nil && col.dayFirst
	if x, ok := parseDate(a, dayFirst); ok {
		if y, ok := parseDate(b, dayFirst); ok {
			return x.Compare(y)
		}
	}
	return strings.Compare(strings.ToLower(a), strings.ToLower(b))
}

// checkEvery is how many rows the query reads between looks at ctx, so a
// cancelled turn stops it without a check on every row.
const checkEvery = 10_000

// filterRows returns the rows that satisfy every condition. It returns
// ctx's error when the turn ends first.
func filterRows(ctx context.Context, rows []dataRow, conds []match) ([]dataRow, error) {
	var out []dataRow
	for i, r := range rows {
		if i%checkEvery == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		ok := true
		for _, m := range conds {
			if !m.holds(r.cells) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// errTooManyGroups is the line for a group_by that makes too many groups.
var errTooManyGroups = fmt.Errorf("analyze_table: more than %d groups; filter with where first, or group by fewer columns", maxGroups)

// groupRows puts rows into groups by the by columns, case and spaces
// aside, and adds up aggs in each. With no by columns, every row is one
// group, even when no row matched, so a count says 0. Groups come back in
// the order their first row appears. It fails past maxGroups groups, and
// with ctx's error when the turn ends.
func groupRows(ctx context.Context, rows []dataRow, by []column, aggs []agg) ([]*group, error) {
	index := map[string]*group{}
	var groups []*group
	if len(by) == 0 {
		g := newGroup(nil, 0, aggs)
		groups = append(groups, g)
		index[""] = g
	}
	for i, r := range rows {
		if i%checkEvery == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		key := make([]string, len(by))
		for j, c := range by {
			key[j] = c.cell(r.cells)
		}
		// \x00 can't appear in a cell's text, so it keeps "a b"+"c" apart
		// from "a"+"b c".
		k := strings.ToLower(strings.Join(key, "\x00"))
		g, ok := index[k]
		if !ok {
			if len(groups) >= maxGroups {
				return nil, errTooManyGroups
			}
			g = newGroup(key, r.n, aggs)
			index[k] = g
			groups = append(groups, g)
		}
		for j, a := range aggs {
			g.aggs[j].add(a, r.cells)
		}
	}
	return groups, nil
}

// newGroup starts a group with key, whose first row is first. Each sum
// counts in its column's scale, the most decimal places among its numbers,
// so every value lines up without losing a digit.
func newGroup(key []string, first int, aggs []agg) *group {
	g := &group{key: key, aggs: make([]aggState, len(aggs)), first: first}
	for j, a := range aggs {
		if a.col != nil {
			g.aggs[j].scale = a.col.scale
		}
	}
	return g
}

// markdownTable writes header and rows as a Markdown table, each cell cut
// to maxCellChars and with its pipes escaped, so a cell can't break the
// table.
func markdownTable(header []string, rows [][]string) string {
	var b strings.Builder
	line := func(cells []string) {
		b.WriteString("|")
		for _, c := range cells {
			c = strings.ReplaceAll(cut(oneLine(c), maxCellChars), "|", "\\|")
			b.WriteString(" " + c + " |")
		}
		b.WriteString("\n")
	}
	line(header)
	sep := make([]string, len(header))
	for i := range sep {
		sep[i] = "---"
	}
	line(sep)
	for _, r := range rows {
		line(r)
	}
	return b.String()
}

// oneLine replaces line breaks in s with spaces.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
