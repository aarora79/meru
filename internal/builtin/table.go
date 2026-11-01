// This file holds analyze_table, the read-only tool that answers questions
// about an Excel sheet or a delimited text file: it describes a sheet, or
// filters, groups, adds up and sorts its rows from fixed fields the model
// fills in. The model never writes code, and nothing reaches an
// interpreter (ARCHITECTURE.md, "Deliberate non-goals"). tablequery.go
// holds the arithmetic; docs/design/word-excel-csv.md explains the design.

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/index"
	"github.com/aarora79/meru/internal/rpc"
)

// AnalyzeTable is the table tool's name, as the model sees it.
const AnalyzeTable = "analyze_table"

// Limits on one analyze_table call. The caps on the call's own fields keep
// a model that a hostile page steered from making every row compare
// against thousands of values; the caps on the work keep the result far
// under dispatch's 16,000-character cut.
const (
	maxWhere          = 10
	maxValues         = 100
	maxGroupBy        = 3
	maxAggregates     = 10
	maxColumns        = 20
	maxSort           = 3
	defaultTableLimit = 20
	maxTableLimit     = 200
	maxGroups         = 10_000
	maxCellChars      = 80
	describeRows      = 5 // data rows the description shows
	describeSamples   = 3 // sample values per column in the description
)

// fns are the aggregates analyze_table computes.
var fns = []string{"count", "sum", "avg", "min", "max", "median", "percentile"}

// tableArgs is the JSON the model sends. decode refuses any key the struct
// doesn't name, so the model can pass nothing that reads as code.
type tableArgs struct {
	Path      string      `json:"path"`
	Sheet     string      `json:"sheet"`      // .xlsx only; empty means the first sheet with data
	HeaderRow int         `json:"header_row"` // 0 means find it
	Where     []condition `json:"where"`      // all must hold
	GroupBy   stringList  `json:"group_by"`
	Aggregate []aggArg    `json:"aggregate"`
	Columns   stringList  `json:"columns"` // for a list of rows, without aggregate
	Sort      []sortKey   `json:"sort"`
	Limit     int         `json:"limit"` // rows in the result; 0 means defaultTableLimit
}

// condition is one filter. Column is a header name or a column letter.
type condition struct {
	Column string     `json:"column"`
	Op     string     `json:"op"`
	Value  string     `json:"value"`
	Values stringList `json:"values"` // for "in"
}

// aggArg is one figure per group. Column is empty for a count of rows. P
// is the percentile for fn "percentile", 0 to 100; a pointer, so a call
// that leaves it out can be told from one that passes 0.
type aggArg struct {
	Fn     string   `json:"fn"`
	Column string   `json:"column"`
	P      *float64 `json:"p"`
}

// stringList is a list of strings that also takes one string, or a string
// that holds a JSON list. Models send `"group_by": "City"` and
// `"group_by": "[\"City\"]"` as often as the list the schema asks for, and
// refusing them costs a round for no gain.
type stringList []string

// UnmarshalJSON reads a list, a string, or a string holding a list. A
// method with this name is what encoding/json calls to decode the type.
func (l *stringList) UnmarshalJSON(b []byte) error {
	var list []string
	if err := json.Unmarshal(b, &list); err == nil {
		*l = list
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return errors.New("want a list of strings")
	}
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[") && json.Unmarshal([]byte(s), &list) == nil {
		*l = list
		return nil
	}
	// A list written without quotes, such as [daily] or [City, Year].
	if inner, ok := strings.CutPrefix(s, "["); ok {
		inner = strings.TrimSuffix(inner, "]")
		for _, part := range strings.Split(inner, ",") {
			if part = strings.Trim(strings.TrimSpace(part), `"'`); part != "" {
				list = append(list, part)
			}
		}
		*l = list
		return nil
	}
	if s != "" {
		*l = stringList{s}
	}
	return nil
}

// sortKey orders the result by one of its columns, written as its header
// shows it, such as "sum(Amount)".
type sortKey struct {
	By   string `json:"by"`
	Desc bool   `json:"desc"`
}

// analyzeTable runs one analyze_table call and returns its text and, when
// rows matched, one citation for the sheet and the rows it drew on. With
// only a path, and perhaps a sheet or header_row, it describes the sheet.
// It fails, with text the model reads, on bad fields, a path it may not
// read, a file that isn't a table, a limit, and a cancelled turn.
func (t *Tools) analyzeTable(ctx context.Context, raw json.RawMessage) (string, []rpc.Citation, error) {
	var a tableArgs
	if err := decode(AnalyzeTable, raw, &a); err != nil {
		return "", nil, err
	}
	if err := checkTableArgs(&a); err != nil {
		return "", nil, err
	}
	c, err := t.resolve(AnalyzeTable, a.Path)
	if err != nil {
		return "", nil, err
	}
	if c.Info.IsDir() {
		return "", nil, fmt.Errorf("analyze_table: %s is a folder; pass one .xlsx, .csv, .tsv or .tab file", t.show(c.Path))
	}
	tables, reason, err := t.files.ReadTable(c.Path)
	switch {
	case errors.Is(err, index.ErrNotTable):
		return "", nil, fmt.Errorf("analyze_table: %s isn't a table. It reads Excel (.xlsx) and delimited text (.csv, .tsv, .tab) files; read this one with read_file", t.show(c.Path))
	case err != nil:
		return "", nil, fmt.Errorf("analyze_table: %s: %v", t.show(c.Path), err)
	case reason != "":
		return "", nil, fmt.Errorf("analyze_table: %s is off limits: %s", t.show(c.Path), why(reason))
	}
	tab, err := pickSheet(tables, a.Sheet)
	if err != nil {
		return "", nil, err
	}
	h, how := a.HeaderRow, "the row you named"
	if h > len(tab.Rows) {
		return "", nil, fmt.Errorf("analyze_table: header_row %d is past the last row of %s, row %d", h, tab.Name, len(tab.Rows))
	}
	if h == 0 {
		h, how = index.HeaderRow(tab), "the first row that names the columns"
	}
	cols := sheetColumns(tab, h)
	rows := dataRows(tab, h)
	typeColumns(cols, rows)

	sheetLine := ""
	if len(tables) > 1 {
		sheetLine = fmt.Sprintf(" Pass sheet to pick another: %s.", sheetList(tables))
	}
	if !a.hasQuery() {
		return describe(t.show(c.Path), tables, tab, h, how, cols, rows) + sheetLine, nil, nil
	}
	q, err := compileQuery(a, cols)
	if err != nil {
		return "", nil, err
	}
	text, first, last, matched, err := q.run(ctx, tab.Name, rows)
	if err != nil {
		// The query's own errors already start "analyze_table:"; a sum's
		// overflow doesn't.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
			strings.HasPrefix(err.Error(), AnalyzeTable+":") {
			return "", nil, err
		}
		return "", nil, fmt.Errorf("analyze_table: %v", err)
	}
	// The dispatch span counts rows, never cell text.
	trace.SpanFromContext(ctx).SetAttributes(
		attribute.Int("meru.table.rows", len(rows)),
		attribute.Int("meru.table.matched", matched))
	if matched == 0 {
		return text, nil, nil
	}
	n := dispatch.CiteNumbers(ctx, 1)
	src := rpc.Citation{N: n, Path: t.show(c.Path), Heading: tab.Name, StartLine: first, EndLine: last, Kind: rpc.SourceTable}
	return text + fmt.Sprintf("Cite this table as [%d].", n), []rpc.Citation{src}, nil
}

// hasQuery reports whether the call asks for more than a description.
func (a tableArgs) hasQuery() bool {
	return len(a.Where) > 0 || len(a.GroupBy) > 0 || len(a.Aggregate) > 0 || len(a.Columns) > 0 || len(a.Sort) > 0
}

// checkTableArgs checks the fields that don't need the sheet: the path,
// the caps on each list, the operators and functions, and the limit, which
// it sets to its default when the call leaves it out. It fails with a line
// that says what to fix.
func checkTableArgs(a *tableArgs) error {
	if strings.TrimSpace(a.Path) == "" {
		return errors.New("analyze_table: pass path, the .xlsx, .csv, .tsv or .tab file to read")
	}
	caps := []struct {
		field string
		n     int
		max   int
	}{
		{"where", len(a.Where), maxWhere}, {"group_by", len(a.GroupBy), maxGroupBy},
		{"aggregate", len(a.Aggregate), maxAggregates}, {"columns", len(a.Columns), maxColumns},
		{"sort", len(a.Sort), maxSort},
	}
	for _, c := range caps {
		if c.n > c.max {
			return fmt.Errorf("analyze_table: %s has %d entries; pass at most %d", c.field, c.n, c.max)
		}
	}
	for _, w := range a.Where {
		if !slices.Contains(ops, w.Op) {
			return fmt.Errorf("analyze_table: op %q isn't one of %s", w.Op, strings.Join(ops, ", "))
		}
		if len(w.Values) > maxValues {
			return fmt.Errorf("analyze_table: values has %d entries; pass at most %d", len(w.Values), maxValues)
		}
	}
	for _, g := range a.Aggregate {
		if !slices.Contains(fns, g.Fn) {
			return fmt.Errorf("analyze_table: fn %q isn't one of %s", g.Fn, strings.Join(fns, ", "))
		}
		if g.Fn == "percentile" && (g.P == nil || *g.P < 0 || *g.P > 100) {
			return errors.New("analyze_table: percentile needs p, from 0 to 100, such as 25 for the first quartile")
		}
	}
	if a.HeaderRow < 0 {
		return fmt.Errorf("analyze_table: header_row %d is negative", a.HeaderRow)
	}
	if a.Limit == 0 {
		a.Limit = defaultTableLimit
	}
	if a.Limit < 1 || a.Limit > maxTableLimit {
		return fmt.Errorf("analyze_table: limit %d is out of range; pass 1 to %d", a.Limit, maxTableLimit)
	}
	return nil
}

// pickSheet returns the sheet named name, ignoring case, or, with no name,
// the first shown sheet that holds data, then the first that holds any. It
// fails, listing the sheets, when no sheet has that name.
func pickSheet(tables []index.Table, name string) (index.Table, error) {
	name = strings.TrimSpace(name)
	if name != "" {
		for _, tab := range tables {
			if strings.EqualFold(tab.Name, name) {
				return tab, nil
			}
		}
		return index.Table{}, fmt.Errorf("analyze_table: no sheet is named %q. The sheets are: %s", name, sheetList(tables))
	}
	for _, tab := range tables {
		if !tab.Hidden && len(tab.Rows) > 0 {
			return tab, nil
		}
	}
	for _, tab := range tables {
		if len(tab.Rows) > 0 {
			return tab, nil
		}
	}
	return tables[0], nil
}

// sheetList names the sheets, each with its row count, for a message.
func sheetList(tables []index.Table) string {
	parts := make([]string, len(tables))
	for i, tab := range tables {
		parts[i] = fmt.Sprintf("%s (%s)", tab.Name, plural(len(tab.Rows), "row"))
		if tab.Hidden {
			parts[i] = fmt.Sprintf("%s (%s, hidden)", tab.Name, plural(len(tab.Rows), "row"))
		}
	}
	return strings.Join(parts, ", ")
}

// sheetColumns returns the sheet's columns, named from header row h.
func sheetColumns(tab index.Table, h int) []column {
	names := index.ColumnNames(tab, h)
	cols := make([]column, len(names))
	for j, name := range names {
		cols[j] = column{name: name, letter: index.ColumnLetter(j), index: j}
	}
	return cols
}

// dataRows returns the rows below header row h that hold something.
func dataRows(tab index.Table, h int) []dataRow {
	var rows []dataRow
	for i := h; i < len(tab.Rows); i++ {
		for _, cell := range tab.Rows[i] {
			if strings.TrimSpace(cell) != "" {
				rows = append(rows, dataRow{n: i + 1, cells: tab.Rows[i]})
				break
			}
		}
	}
	return rows
}

// findColumn finds the column named name: by its header, ignoring case,
// or by its letter, as "C" or "Column C". It fails with the list of
// columns.
func findColumn(cols []column, name string) (column, error) {
	name = strings.TrimSpace(name)
	for _, c := range cols {
		if strings.EqualFold(c.name, name) {
			return c, nil
		}
	}
	letter := strings.TrimSpace(strings.TrimPrefix(strings.ToUpper(name), "COLUMN "))
	for _, c := range cols {
		if c.letter == letter {
			return c, nil
		}
	}
	names := make([]string, 0, len(cols))
	for _, c := range cols {
		names = append(names, c.name)
	}
	return column{}, fmt.Errorf("analyze_table: no column is named %q. The columns are: %s", name, strings.Join(names, ", "))
}

// describe writes what the model needs before it asks: the file's sheets,
// the chosen sheet's header row and why, its data rows, each column's
// type and values, and the first data rows. how says how the header row
// was chosen.
func describe(path string, tables []index.Table, tab index.Table, h int, how string, cols []column, rows []dataRow) string {
	var b strings.Builder
	if strings.HasSuffix(strings.ToLower(path), ".xlsx") {
		fmt.Fprintf(&b, "%s: a workbook with %s: %s.\n", path, plural(len(tables), "sheet"), sheetList(tables))
	} else {
		fmt.Fprintf(&b, "%s: a delimited text file of %s.\n", path, plural(len(tab.Rows), "row"))
	}
	fmt.Fprintf(&b, "Sheet %s. ", tab.Name)
	if h > 0 {
		fmt.Fprintf(&b, "Header row %d, %s; pass header_row if another row names the columns.\n", h, how)
	} else {
		b.WriteString("No row names the columns, so they go by letter, such as \"Column A\"; pass header_row if a row names them.\n")
	}
	if len(rows) == 0 {
		b.WriteString("It holds no data rows.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "Data: rows %d to %d, %s that hold something.\n", rows[0].n, rows[len(rows)-1].n, plural(len(rows), "row"))
	b.WriteString("Columns:\n")
	for i, c := range cols {
		if i == maxColumns {
			rest := make([]string, 0, len(cols)-i)
			for _, more := range cols[i:] {
				rest = append(rest, more.name)
			}
			fmt.Fprintf(&b, "- and %d more: %s\n", len(rest), cut(strings.Join(rest, ", "), 400))
			break
		}
		b.WriteString(describeColumn(c, rows) + "\n")
	}
	header := []string{"Row"}
	shown := cols[:min(len(cols), maxColumns)]
	for _, c := range shown {
		header = append(header, c.name)
	}
	var first [][]string
	for _, r := range rows[:min(len(rows), describeRows)] {
		line := []string{fmt.Sprint(r.n)}
		for _, c := range shown {
			line = append(line, c.cell(r.cells))
		}
		first = append(first, line)
	}
	b.WriteString("First rows:\n")
	b.WriteString(markdownTable(header, first))
	b.WriteString("Call again with where, group_by, aggregate, columns or sort to filter rows, count them or add them up.")
	return b.String()
}

// describeColumn writes one column's line: its name and letter, its type,
// how many cells hold a value, and its range or some sample values.
func describeColumn(c column, rows []dataRow) string {
	n := 0
	var samples []string
	var lo, hi string
	var loD, hiD decimal
	var loT, hiT string
	for _, r := range rows {
		v := c.cell(r.cells)
		if v == "" {
			continue
		}
		n++
		if len(samples) < describeSamples && !slices.Contains(samples, v) {
			samples = append(samples, v)
		}
		switch c.typ {
		case colNumber:
			if d, ok := parseNumber(v); ok {
				if lo == "" || cmpDecimal(d, loD) < 0 {
					lo, loD = v, d
				}
				if hi == "" || cmpDecimal(d, hiD) > 0 {
					hi, hiD = v, d
				}
			}
		case colDate:
			if t, ok := parseDate(v, c.dayFirst); ok {
				s := formatDate(t)
				if loT == "" || s < loT {
					loT = s
				}
				if hiT == "" || s > hiT {
					hiT = s
				}
			}
		}
	}
	line := fmt.Sprintf("- %s (%s): %s, %s", c.name, c.letter, c.typ, plural(n, "value"))
	switch {
	case n == 0:
		return fmt.Sprintf("- %s (%s): empty", c.name, c.letter)
	case c.typ == colNumber:
		line += fmt.Sprintf(", from %s to %s", lo, hi)
	case c.typ == colDate:
		line += fmt.Sprintf(", from %s to %s", loT, hiT)
		if c.dayFirst {
			line += " (slash dates read day first)"
		}
	}
	quoted := make([]string, len(samples))
	for i, s := range samples {
		quoted[i] = fmt.Sprintf("%q", cut(oneLine(s), 40))
	}
	return line + ", such as " + strings.Join(quoted, ", ")
}

// query is a compiled analyze_table call.
type query struct {
	where   []match
	by      []column // group_by
	aggs    []agg
	list    []column // columns, for a list of rows
	grouped bool     // group_by or aggregate was given
	sort    []sortKey
	limit   int
}

// compileQuery turns the call's fields into a query against cols. A
// group_by with no aggregate counts rows; a call with neither lists rows,
// the columns it names or else every column up to maxColumns. It fails on
// an unknown column and on a field an operator or function needs.
func compileQuery(a tableArgs, cols []column) (query, error) {
	q := query{sort: a.Sort, limit: a.Limit, grouped: len(a.GroupBy) > 0 || len(a.Aggregate) > 0}
	for _, w := range a.Where {
		col, err := findColumn(cols, w.Column)
		if err != nil {
			return q, err
		}
		m := match{col: col, op: w.Op}
		switch w.Op {
		case "empty", "not_empty":
		case "in":
			if len(w.Values) == 0 {
				return q, fmt.Errorf("analyze_table: op \"in\" needs values, a list of the values to match")
			}
			m.values = w.Values
		default:
			if w.Value == "" && w.Op != "=" && w.Op != "!=" {
				return q, fmt.Errorf("analyze_table: op %q needs value", w.Op)
			}
			m.values = []string{w.Value}
		}
		q.where = append(q.where, m)
	}
	for _, name := range a.GroupBy {
		col, err := findColumn(cols, name)
		if err != nil {
			return q, err
		}
		q.by = append(q.by, col)
	}
	for _, g := range a.Aggregate {
		ag := agg{fn: g.Fn}
		if g.P != nil {
			// Hundredths of a percent, so p 2.5 is 250 and the arithmetic
			// stays in integers.
			ag.p = int64(math.Round(*g.P * 100))
		}
		if strings.TrimSpace(g.Column) != "" {
			col, err := findColumn(cols, g.Column)
			if err != nil {
				return q, err
			}
			ag.col = &col
		} else if g.Fn != "count" {
			return q, fmt.Errorf("analyze_table: %s needs column, the column to add up", g.Fn)
		}
		q.aggs = append(q.aggs, ag)
	}
	if q.grouped && len(q.aggs) == 0 {
		q.aggs = []agg{{fn: "count"}}
	}
	if !q.grouped {
		if len(a.Columns) == 0 {
			q.list = cols[:min(len(cols), maxColumns)]
		}
		for _, name := range a.Columns {
			col, err := findColumn(cols, name)
			if err != nil {
				return q, err
			}
			q.list = append(q.list, col)
		}
	}
	return q, nil
}

// run filters rows, then groups and adds them up or lists them, sorts and
// cuts the result, and writes it under a line that says what matched in
// the sheet called name. It returns the text, the first and last matching
// row numbers, and how many rows matched. It fails on an unknown sort
// column, on too many groups, on a sum past an int64, and with ctx's error
// when the turn ends.
func (q query) run(ctx context.Context, name string, rows []dataRow) (text string, first, last, matched int, err error) {
	hits, err := filterRows(ctx, rows, q.where)
	if err != nil {
		return "", 0, 0, 0, err
	}
	for _, r := range hits {
		if first == 0 || r.n < first {
			first = r.n
		}
		last = max(last, r.n)
	}
	var b strings.Builder
	if len(hits) == 0 {
		fmt.Fprintf(&b, "No row of %s's %s matched. Describe the sheet (call with path alone) to check the column names and values.\n", name, plural(len(rows), "row"))
		return b.String(), 0, 0, 0, nil
	}
	fmt.Fprintf(&b, "Matched %d of %s in %s (rows %d to %d).\n", len(hits), plural(len(rows), "row"), name, first, last)

	var header []string
	var out []outRow
	var plain []*column // the sheet column behind each result column, or nil for an aggregate
	notes := ""
	if q.grouped {
		groups, err := groupRows(ctx, hits, q.by, q.aggs)
		if err != nil {
			return "", 0, 0, 0, err
		}
		for i := range q.by {
			header = append(header, q.by[i].name)
			plain = append(plain, &q.by[i])
		}
		for _, a := range q.aggs {
			header = append(header, a.header())
			plain = append(plain, nil)
		}
		skipped := map[string]int{}
		for _, g := range groups {
			row := outRow{cells: append([]string{}, g.key...), nums: make([]float64, len(header)), row: g.first}
			for j, a := range q.aggs {
				s := &g.aggs[j]
				v, f, err := s.result(a)
				if err != nil {
					return "", 0, 0, 0, fmt.Errorf("%s: %v", a.header(), err)
				}
				row.cells = append(row.cells, v)
				row.nums[len(q.by)+j] = f
				if s.skipped > 0 {
					skipped[a.header()] += s.skipped
				}
			}
			out = append(out, row)
		}
		for _, a := range q.aggs {
			if n := skipped[a.header()]; n > 0 {
				notes += fmt.Sprintf("%s left out %s that aren't numbers.\n", a.header(), plural(n, "value"))
			}
		}
		if len(q.by) > 0 {
			fmt.Fprintf(&b, "%s.\n", plural(len(groups), "group"))
		}
	} else {
		header = append(header, "Row")
		plain = append(plain, nil)
		for i := range q.list {
			header = append(header, q.list[i].name)
			plain = append(plain, &q.list[i])
		}
		for _, r := range hits {
			row := outRow{cells: []string{fmt.Sprint(r.n)}, nums: make([]float64, len(header)), row: r.n}
			row.nums[0] = float64(r.n)
			for _, c := range q.list {
				row.cells = append(row.cells, c.cell(r.cells))
			}
			out = append(out, row)
		}
	}

	keys, err := sortKeys(q.sort, header, plain)
	if err != nil {
		return "", 0, 0, 0, err
	}
	sortRows(out, keys)
	total := len(out)
	if total > q.limit {
		out = out[:q.limit]
		notes += fmt.Sprintf("Showing %d of %d; the rest are left out. Pass a larger limit (at most %d) to see more, "+
			"and use aggregate for a figure over every row.\n", q.limit, total, maxTableLimit)
	}
	cells := make([][]string, len(out))
	for i, r := range out {
		cells[i] = r.cells
	}
	b.WriteString(markdownTable(header, cells))
	b.WriteString(notes)
	return b.String(), first, last, len(hits), nil
}

// sortKeys matches each sort key to a result column by its header,
// ignoring case. A column of aggregates sorts by value; "Row" sorts by row
// number. It fails, listing the headers, when a key names none of them.
func sortKeys(keys []sortKey, header []string, plain []*column) ([]sortBy, error) {
	var out []sortBy
	for _, k := range keys {
		i := -1
		for j, h := range header {
			if strings.EqualFold(strings.TrimSpace(k.By), h) {
				i = j
				break
			}
		}
		if i < 0 {
			return nil, fmt.Errorf("analyze_table: sort by %q matches no column of the result. Sort by one of: %s", k.By, strings.Join(header, ", "))
		}
		out = append(out, sortBy{index: i, desc: k.Desc, numeric: plain[i] == nil, col: plain[i]})
	}
	return out, nil
}

// tableSpec returns analyze_table's spec. The description stays short,
// since it joins the prompt of every file question, and carries the one
// rule that matters most: never add up rows by hand.
func tableSpec() engine.ToolSpec {
	str := func(desc string) map[string]any { return prop("string", desc) }
	return engine.ToolSpec{
		Name: AnalyzeTable,
		Description: "Answers questions about an Excel (.xlsx), CSV or TSV file exactly: filters, groups, counts, sums, averages, " +
			"minimums, maximums, medians and percentiles (IQR is p75 minus p25), and sorts its rows. For any total, count or average over a table, call this tool; " +
			"never add up rows yourself. Call it with path alone first to see the sheets, columns and sample rows.",
		Parameters: mustJSON(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":       str("The .xlsx, .csv, .tsv or .tab file."),
				"sheet":      str("An Excel sheet's name. Default: the first sheet with data."),
				"header_row": prop("integer", "The row that names the columns, counting from 1. Default: found."),
				"where": map[string]any{
					"type":        "array",
					"description": "Conditions every row must meet.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"column": str("A column name or letter."),
							"op":     map[string]any{"type": "string", "enum": ops},
							"value":  str("The value to compare with. Dates as 2026-05-03."),
							"values": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "The values for op in."},
						},
						"required": []string{"column", "op"},
					},
				},
				"group_by": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Up to 3 columns to group rows by."},
				"aggregate": map[string]any{
					"type":        "array",
					"description": "Figures per group, or over all matching rows.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"fn":     map[string]any{"type": "string", "enum": fns},
							"column": str("The column to add up; leave it out to count rows."),
							"p":      prop("number", "For percentile: 0 to 100, such as 25 or 75."),
						},
						"required": []string{"fn"},
					},
				},
				"columns": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Columns to list, without aggregate."},
				"sort": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"by":   str("A result column, such as sum(Amount)."),
							"desc": prop("boolean", "Largest first."),
						},
						"required": []string{"by"},
					},
				},
				"limit": prop("integer", fmt.Sprintf("Rows in the result, 1 to %d. Default %d.", maxTableLimit, defaultTableLimit)),
			},
			"required": []string{"path"},
		}),
	}
}

// mustJSON marshals v, a schema built from plain maps, slices and strings.
// json.Marshal can't fail on those, so it drops the error.
func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
