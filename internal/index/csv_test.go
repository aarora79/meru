// This file tests the delimited text reader: the delimiter guess, quoted
// fields, encodings, and the row limit.

package index

import (
	"reflect"
	"strings"
	"testing"
)

func TestCSVTable(t *testing.T) {
	tests := []struct {
		name string
		ext  string
		data string
		want [][]string
	}{
		{"comma", ".csv", "City,Amount\nAmsterdam,64.50\n", [][]string{{"City", "Amount"}, {"Amsterdam", "64.50"}}},
		{"semicolon", ".csv", "City;Amount\nParis;1,5\n", [][]string{{"City", "Amount"}, {"Paris", "1,5"}}},
		{"tab", ".tsv", "City\tAmount\nRome\t3\n", [][]string{{"City", "Amount"}, {"Rome", "3"}}},
		{"pipe", ".csv", "City|Amount\nOslo|4\n", [][]string{{"City", "Amount"}, {"Oslo", "4"}}},
		{"quoted commas", ".csv", "Name,Note\n\"Smith, Jo\",\"said \"\"hi\"\"\"\n", [][]string{{"Name", "Note"}, {"Smith, Jo", `said "hi"`}}},
		{"a line break in quotes", ".csv", "Name,Note\nA,\"two\nlines\"\nB,one\n", [][]string{{"Name", "Note"}, {"A", "two\nlines"}, {"B", "one"}}},
		{"a tie goes to tab in a .tsv", ".tsv", "a,b\tc\nd,e\tf\n", [][]string{{"a,b", "c"}, {"d,e", "f"}}},
		{"a tie goes to comma in a .csv", ".csv", "a,b\tc\nd,e\tf\n", [][]string{{"a", "b\tc"}, {"d", "e\tf"}}},
		{"one column", ".csv", "alpha\nbeta\n", [][]string{{"alpha"}, {"beta"}}},
		{"ragged rows and trailing empties", ".csv", "a,b,c\n1\n2,3,,\n\n", [][]string{{"a", "b", "c"}, {"1"}, {"2", "3"}}},
		{"byte-order mark", ".csv", "\xEF\xBB\xBFCity,Amount\nBern,5\n", [][]string{{"City", "Amount"}, {"Bern", "5"}}},
		{"Windows-1252", ".csv", "Item,Price\nCaf\xe9,\x8012\n", [][]string{{"Item", "Price"}, {"Café", "€12"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := csvTable("trip", tt.ext, []byte(tt.data))
			if err != nil {
				t.Fatalf("csvTable: %v", err)
			}
			if got.Name != "trip" || !reflect.DeepEqual(got.Rows, tt.want) {
				t.Errorf("csvTable = %q %q, want trip %q", got.Name, got.Rows, tt.want)
			}
		})
	}
}

func TestCSVLimits(t *testing.T) {
	rows := strings.Repeat("1\n", maxRows+1)
	if _, err := csvTable("log", ".csv", []byte(rows)); err == nil || !strings.Contains(err.Error(), "rows") {
		t.Errorf("csvTable with %d rows = %v, want a limit error", maxRows+1, err)
	}
	wide := strings.Repeat("x,", maxCols) + "x\n"
	if _, err := csvTable("wide", ".csv", []byte(wide)); err == nil || !strings.Contains(err.Error(), "columns") {
		t.Errorf("csvTable with %d columns = %v, want a limit error", maxCols+1, err)
	}
}
