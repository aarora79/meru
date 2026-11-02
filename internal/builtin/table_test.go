// This file tests analyze_table through Tools.Call, on files in a
// temporary folder: describing a sheet, the queries, the citation, and
// each check on the call.

package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/index"
	"github.com/aarora79/meru/internal/rpc"
)

// tripCSV is a small expense list under a title row, so the header guess
// has to skip the title.
const tripCSV = "Trip to the Netherlands\n" +
	"Date,City,Category,Amount\n" +
	"2026-05-01,Amsterdam,Museum,EUR 27.00\n" +
	"2026-05-02,Amsterdam,Food tour,EUR 64.50\n" +
	"2026-05-03,Utrecht,Food tour,EUR 70.00\n" +
	"2026-05-03,Amsterdam,Food tour,\"EUR 1,042.25\"\n" +
	"2026-05-04,Amsterdam,Food tour,EUR 100.25\n" +
	"2026-05-05,Rotterdam,Tram,EUR 12.00\n"

// tableTools returns built-in tools over a folder holding trip.csv, a
// workbook, a Markdown file and an old .xls, and the folder.
func tableTools(t *testing.T) (*Tools, string) {
	t.Helper()
	root := t.TempDir()
	files := map[string][]byte{
		"trip.csv": []byte(tripCSV),
		"budget.xlsx": workbook(t, []string{"Summary", "Invoices"}, [][][]string{
			{{"Total", "277"}},
			{{"No", "City", "Amount"}, {"14", "Amsterdam", "64.50"}, {"15", "Rome", "10"}},
		}),
		"notes.md": []byte("# Notes\n"),
		"old.xls":  {0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1},
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ix, err := index.New(config.Index{Folders: []string{root}, MaxFileMB: 1}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return New(filepath.Join(root, "config.toml"), config.Builtin{Tools: config.BuiltinTools()}, config.Web{}, nil, "", ix, nil, nil), root
}

// callTable calls analyze_table with args, where %s stands for the folder.
func callTable(t *testing.T, tools *Tools, root, args string) dispatch.Result {
	t.Helper()
	return callTool(t, tools, context.Background(), AnalyzeTable, strings.ReplaceAll(args, "%s", root))
}

func TestAnalyzeTableOffered(t *testing.T) {
	tools, _ := tableTools(t)
	var spec bool
	for _, s := range tools.Tools() {
		if s.Name == AnalyzeTable {
			spec = true
			if n := len(s.Description); n > 600 {
				t.Errorf("description is %d characters; keep it under 600, since every file question carries it", n)
			}
		}
	}
	if !spec || !IsFileTool(AnalyzeTable) || tools.Confirm(AnalyzeTable) != dispatch.ConfirmNever {
		t.Errorf("offered %v, file tool %v, confirm %v; want offered, a file tool that never asks", spec, IsFileTool(AnalyzeTable), tools.Confirm(AnalyzeTable))
	}
	none := New(filepath.Join(t.TempDir(), "config.toml"), config.Builtin{Tools: config.BuiltinTools()}, config.Web{}, nil, "", nil, nil, nil)
	if slices.ContainsFunc(none.Tools(), func(s engine.ToolSpec) bool { return s.Name == AnalyzeTable }) {
		t.Error("analyze_table offered with no [index] folders")
	}
}

func TestAnalyzeTableDescribe(t *testing.T) {
	tools, root := tableTools(t)
	res := callTable(t, tools, root, `{"path":"%s/trip.csv"}`)
	for _, want := range []string{
		"a delimited text file of 8 rows",
		"Header row 2, the first row that names the columns",
		"Data: rows 3 to 8, 6 rows",
		"- Date (A): date, 6 values, from 2026-05-01 to 2026-05-05",
		"- Amount (D): number, 6 values, from EUR 12.00 to EUR 1,042.25",
		"- City (B): text, 6 values, such as \"Amsterdam\", \"Utrecht\", \"Rotterdam\"",
		"| 3 | 2026-05-01 | Amsterdam | Museum | EUR 27.00 |",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("description lacks %q:\n%s", want, res.Text)
		}
	}
	if len(res.Sources) != 0 {
		t.Errorf("a description cites %v; it should cite nothing", res.Sources)
	}

	res = callTable(t, tools, root, `{"path":"%s/budget.xlsx"}`)
	if !strings.Contains(res.Text, "a workbook with 2 sheets: Summary (1 row), Invoices (3 rows)") || !strings.Contains(res.Text, "Sheet Summary.") {
		t.Errorf("workbook description:\n%s", res.Text)
	}
	res = callTable(t, tools, root, `{"path":"%s/budget.xlsx","sheet":"invoices","header_row":1}`)
	if !strings.Contains(res.Text, "Sheet Invoices. Header row 1, the row you named") {
		t.Errorf("named sheet and header:\n%s", res.Text)
	}
}

func TestAnalyzeTableQueries(t *testing.T) {
	tools, root := tableTools(t)
	tests := []struct {
		name string
		args string
		want []string
		rows [2]int // the rows the citation names; zero for none
	}{
		{"sum of matching rows",
			`{"path":"%s/trip.csv","where":[{"column":"City","op":"=","value":"amsterdam"},{"column":"Category","op":"=","value":"Food tour"}],"aggregate":[{"fn":"sum","column":"Amount"},{"fn":"count"}]}`,
			[]string{"Matched 3 of 6 rows in trip (rows 4 to 7).", "| sum(Amount) | count |", "| 1207.00 | 3 |", "Cite this table as [1]."}, [2]int{4, 7}},
		{"group and sort",
			`{"path":"%s/trip.csv","group_by":["City"],"aggregate":[{"fn":"sum","column":"Amount"}],"sort":[{"by":"sum(amount)","desc":true}],"limit":2}`,
			[]string{"3 groups.", "| Amsterdam | 1234.00 |", "| Utrecht | 70.00 |", "Showing 2 of 3"}, [2]int{3, 8}},
		{"group with no aggregate counts",
			`{"path":"%s/trip.csv","group_by":["Category"]}`,
			[]string{"| Category | count |", "| Museum | 1 |", "| Food tour | 4 |", "| Tram | 1 |"}, [2]int{3, 8}},
		{"list rows by date",
			`{"path":"%s/trip.csv","where":[{"column":"Date","op":">=","value":"2026-05-03"}],"columns":["City","D"],"sort":[{"by":"Amount"}]}`,
			[]string{"| Row | City | Amount |", "| 8 | Rotterdam | EUR 12.00 |\n| 5 | Utrecht | EUR 70.00 |"}, [2]int{5, 8}},
		{"in and a column letter",
			`{"path":"%s/trip.csv","where":[{"column":"Column B","op":"in","values":["Utrecht","Rotterdam"]}],"aggregate":[{"fn":"max","column":"Date"},{"fn":"avg","column":"Amount"}]}`,
			[]string{"| max(Date) | avg(Amount) |", "| 2026-05-05 | 41.0000 |"}, [2]int{5, 8}},
		{"median and quartiles",
			`{"path":"%s/trip.csv","aggregate":[{"fn":"median","column":"Amount"},{"fn":"percentile","column":"Amount","p":25},{"fn":"percentile","column":"Amount","p":75}]}`,
			[]string{"| median(Amount) | p25(Amount) | p75(Amount) |", "| 67.25 | 36.375 | 92.6875 |"}, [2]int{3, 8}},
		{"lists sent as strings",
			`{"path":"%s/trip.csv","group_by":"[City]","columns":"City","aggregate":[{"fn":"count"}]}`,
			[]string{"| City | count |", "| Amsterdam | 4 |"}, [2]int{3, 8}},
		{"objects sent as a string, values bare",
			`{"path":"%s/trip.csv","group_by":["City"],"aggregate":"[{\"column\": Amount, \"fn\": sum}, {\"fn\": count}]","sort":"[{\"by\": \"count\", \"desc\": true}]"}`,
			[]string{"| City | sum(Amount) | count |", "| Amsterdam | 1234.00 | 4 |"}, [2]int{3, 8}},
		{"a sheet of a workbook",
			`{"path":"%s/budget.xlsx","sheet":"Invoices","aggregate":[{"fn":"sum","column":"Amount"}]}`,
			[]string{"Matched 2 of 2 rows in Invoices", "| 74.50 |"}, [2]int{2, 3}},
		{"nothing matches",
			`{"path":"%s/trip.csv","where":[{"column":"City","op":"=","value":"Paris"}],"aggregate":[{"fn":"count"}]}`,
			[]string{"No row of trip's 6 rows matched."}, [2]int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := callTable(t, tools, root, tt.args)
			if res.IsError {
				t.Fatalf("error: %s", res.Text)
			}
			for _, want := range tt.want {
				if !strings.Contains(res.Text, want) {
					t.Errorf("result lacks %q:\n%s", want, res.Text)
				}
			}
			if tt.rows == [2]int{} {
				if len(res.Sources) != 0 {
					t.Errorf("sources = %v, want none", res.Sources)
				}
				return
			}
			if len(res.Sources) != 1 {
				t.Fatalf("sources = %v, want one", res.Sources)
			}
			s := res.Sources[0]
			if s.Kind != rpc.SourceTable || s.StartLine != tt.rows[0] || s.EndLine != tt.rows[1] || s.N != 1 {
				t.Errorf("source = %+v, want rows %d to %d of kind %q", s, tt.rows[0], tt.rows[1], rpc.SourceTable)
			}
		})
	}
}

func TestAnalyzeTableRefuses(t *testing.T) {
	tools, root := tableTools(t)
	many := func(n int, item string) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = item
		}
		return strings.Join(parts, ",")
	}
	tests := []struct {
		name string
		args string
		want string
	}{
		{"no path", `{}`, "pass path"},
		{"an unknown field", `{"path":"%s/trip.csv","sql":"select 1"}`, "aren't a valid JSON object"},
		{"too many conditions", `{"path":"%s/trip.csv","where":[` + many(maxWhere+1, `{"column":"City","op":"not_empty"}`) + `]}`, "where has 11 entries; pass at most 10"},
		{"too many values", `{"path":"%s/trip.csv","where":[{"column":"City","op":"in","values":[` + many(maxValues+1, `"x"`) + `]}]}`, "values has 101 entries"},
		{"too many group columns", `{"path":"%s/trip.csv","group_by":["A","B","C","D"]}`, "group_by has 4 entries"},
		{"a bad op", `{"path":"%s/trip.csv","where":[{"column":"City","op":"like","value":"x"}]}`, `op "like" isn't one of`},
		{"a bad function", `{"path":"%s/trip.csv","aggregate":[{"fn":"mode","column":"Amount"}]}`, `fn "mode" isn't one of`},
		{"a percentile with no p", `{"path":"%s/trip.csv","aggregate":[{"fn":"percentile","column":"Amount"}]}`, "percentile needs p"},
		{"a percentile past 100", `{"path":"%s/trip.csv","aggregate":[{"fn":"percentile","column":"Amount","p":101}]}`, "percentile needs p"},
		{"a sum with no column", `{"path":"%s/trip.csv","aggregate":[{"fn":"sum"}]}`, "sum needs column"},
		{"limit out of range", `{"path":"%s/trip.csv","columns":["City"],"limit":201}`, "limit 201 is out of range"},
		{"an unknown column", `{"path":"%s/trip.csv","columns":["Price"]}`, `no column is named "Price". The columns are: Date, City, Category, Amount`},
		{"an unknown sort", `{"path":"%s/trip.csv","columns":["City"],"sort":[{"by":"Amount"}]}`, `sort by "Amount" matches no column of the result`},
		{"an unknown sheet", `{"path":"%s/budget.xlsx","sheet":"Payroll"}`, `no sheet is named "Payroll". The sheets are: Summary (1 row), Invoices (3 rows)`},
		{"a header past the end", `{"path":"%s/trip.csv","header_row":40}`, "header_row 40 is past the last row"},
		{"not a table", `{"path":"%s/notes.md"}`, "isn't a table"},
		{"an old .xls", `{"path":"%s/old.xls"}`, "save it as .xlsx"},
		{"a folder", `{"path":"%s"}`, "is a folder"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := callTable(t, tools, root, tt.args)
			if !res.IsError || !strings.Contains(res.Text, tt.want) {
				t.Errorf("result = %v %q, want an error with %q", res.IsError, res.Text, tt.want)
			}
			if strings.Count(res.Text, "analyze_table:") > 1 {
				t.Errorf("result names the tool twice: %q", res.Text)
			}
		})
	}
}

func TestAnalyzeTableTooManyGroups(t *testing.T) {
	tools, root := tableTools(t)
	var b strings.Builder
	b.WriteString("ID,Amount\n")
	for i := range maxGroups + 1 {
		fmt.Fprintf(&b, "%d,1\n", i)
	}
	if err := os.WriteFile(filepath.Join(root, "ids.csv"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	res := callTable(t, tools, root, `{"path":"%s/ids.csv","group_by":["ID"]}`)
	if !res.IsError || !strings.Contains(res.Text, "more than 10000 groups") {
		t.Errorf("result = %q, want the groups limit", res.Text)
	}
}
