# How do I ask about a spreadsheet?

Put the file in a folder Meru indexes, then ask in your own words. Meru reads
Excel workbooks (`.xlsx`), and CSV and TSV files (`.csv`, `.tsv`, `.tab`).

- "Which wine did I set aside for our anniversary?" Search finds the row, and
  the answer cites the sheet and its rows.
- "How much did we spend on food tours in Amsterdam?" The model calls
  `analyze_table`, which filters the rows and adds them up in Meru's own code.
  The total is exact to the cent, and the answer cites the rows it used.
- "Which city cost the most? Show the top three." `analyze_table` groups the
  rows by city, adds each group up and sorts them.

The model never adds up rows itself and never writes code. It fills in fixed
fields: which rows to keep (`City = Amsterdam`), what to group by, and what to
work out (count, sum, average, smallest, largest). Meru does the rest.

## Tips

- **Name the file or the sheet** when you have several: "in budget.xlsx, sheet
  Invoices, …".
- **Dates** read as `2026-05-03`. A date such as `03/04/2026` reads day first
  when any date in its column has a first part above 12, and month first
  otherwise.
- **Numbers** may carry a currency (`EUR 64.50`, `$1,234.50`) or a percent sign.
  A comma as the decimal point, as in `1.234,56`, reads as text.
- **A title row above the header** is fine: Meru finds the row that names the
  columns. If it picks the wrong one, say which row holds the names.
- A Word file (`.docx`) is searched and read as text, tables included, but its
  tables don't go through `analyze_table`.

## What it can't do

- Read the old `.xls` and `.doc` formats. Open the file in Excel or Numbers and
  save it as `.xlsx` (or `.docx` in Word or Pages).
- Evaluate formulas. Meru reads the value Excel saved with each one.
- Change a spreadsheet, draw a chart, or join two files.
- Read a file over `[index] max_file_mb`, 5 MB by default. A workbook full of
  pictures can pass it; save a copy without them.
- Read more than 100,000 rows of a sheet. Search covers the first 10,000.

## If it doesn't work

- Check that the file is indexed: `meru index -status`, or ask "list my
  Documents folder". A file that failed to parse shows in `merud`'s log as
  "index: can't parse", with the reason.
- `analyze_table` is on by default. If your `config.toml` lists `[builtin]
  tools` by hand, add `"analyze_table"` to the list and restart `merud`.

[docs/design/word-excel-csv.md](../design/word-excel-csv.md) explains how it
works.
