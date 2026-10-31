# Word, Excel and CSV files

*High-level design. Status: the readers are built; `analyze_table` comes next. Issue: [#184](https://github.com/aarora79/meru/issues/184). Milestone: v0.7.*

Meru reads `.docx`, `.xlsx`, and delimited text files such as `.csv` and `.tsv`, so search finds a contract in Word and an invoice list in Excel, and the model can answer "how much did we spend on food tours in Amsterdam?" with an exact total. A built-in tool, `analyze_table`, does the arithmetic from fixed fields the model fills in. The model never writes code, and Meru never starts a program to answer a table question.

[ARCHITECTURE.md](../../ARCHITECTURE.md) holds the contract: what the indexer reads, the built-in tools and their rules. This page explains how the feature works from end to end.

## What a user sees

- A Word, Excel or CSV file in an `[index]` folder shows up in search like any other file. `meru index -status` counts it.
- "When does the Brightline agreement renew?" answers from `contract.docx` and cites the heading the date sits under.
- "Which wine did I set aside for our 15th anniversary?" answers from a row of `wine-cellar.csv` and cites the sheet and the rows.
- "How much did we spend on food tours in Amsterdam?" makes the model call `analyze_table`, which filters and adds up the rows of `trip-expenses.csv`; the answer is EUR 277.00, with the rows cited.
- `read_file` reads a Word file as text and a workbook sheet by sheet. `grep` reports a hit in a spreadsheet as "sheet Invoices, row 14".
- Settings, Connections lists `analyze_table` with Off, Ask and Allow. It only reads, so by default it runs without asking.

## How it works

![A .docx, .xlsx or .csv file goes to the readers in internal/index. Word and Excel files are ZIP archives, so a capped ZIP reader opens them first; a CSV file goes to the CSV reader. The readers feed the indexer, read_file and grep, and analyze_table. Every tool call goes through dispatch.](img/word-excel-csv-flow.svg)

### The readers

Three readers in `internal/index` sit beside the PDF reader. The indexer and the file tools share them, so search, `read_file`, `grep`, mail attachments and files attached in the desktop app all read the same text.

- **Word** (`docx.go`). A `.docx` file is a ZIP archive of XML parts. The reader opens `word/document.xml` and writes Markdown-like text: headings by their style, list items, paragraphs, and each table as one line per row with cells joined by ` | `. Headers, footers, footnotes, comments and deleted text stay out.
- **Excel** (`xlsx.go`). An `.xlsx` file is a ZIP archive too. The reader takes the sheet names from the workbook, the shared strings, and the number formats, so a cell formatted as a date reads as `2026-05-03`, not as `46145`. It reads each sheet one XML token at a time, and keeps the value Excel saved with each formula; it never evaluates a formula.
- **CSV, TSV and their variants** (`csv.go`). One reader takes `.csv`, `.tsv` and `.tab` files. It picks the delimiter from the first lines: a comma, a semicolon, a tab or a pipe, whichever splits every line into the same number of fields. It drops a byte-order mark, and reads a file that isn't UTF-8 as Windows-1252, the encoding Excel uses on Windows. A `.txt` file stays plain text.

The Excel and delimited-text readers return a `Table` per sheet: rows of text, where row 1 is the sheet's first row, the number a person sees in Excel.

### Search

The indexer cuts a Word file by heading, as it cuts Markdown, so a chunk carries its heading path, such as `Term > Renewal`. It cuts a sheet into chunks of rows, each starting with the sheet and its columns, and each row labelled with its number and its column names:

```text
Sheet trip-expenses. Columns: Date, City, Category, Amount.
Row 12: Date 2026-05-03; City Amsterdam; Category Food tour; Amount EUR 64.50
```

The column names let both keyword and vector search match "food tour in Amsterdam" to the row. A spreadsheet chunk cites its sheet and its rows, as "trip-expenses.csv, rows 12–41". Search takes the first 10,000 rows of a sheet; `analyze_table` reads all of them, up to 100,000.

### `analyze_table`

![The steps of one analyze_table call: check the fields and the path, read the file through the same readers and limits, pick the sheet and its header row, read each value as a number, a date or text; with no options, describe the table; with options, filter, group, add up, sort and cut, then answer with a small table and one citation.](img/analyze-table-steps.svg)

The model sends fixed fields, which the tool checks against a schema that refuses any other key:

| Field | What it does |
| --- | --- |
| `path` | an `.xlsx`, `.csv`, `.tsv` or `.tab` file, in an `[index]` folder or the output folder |
| `sheet` | an Excel sheet by name; the first sheet with data when left out |
| `header_row` | the row that names the columns; found when left out |
| `where` | conditions that must all hold: `=`, `!=`, `<`, `<=`, `>`, `>=`, `contains`, `starts_with`, `empty`, `not_empty`, `in` |
| `group_by` | up to three columns to group rows by |
| `aggregate` | `count`, `sum`, `avg`, `min` or `max` of a column, per group |
| `columns` | the columns to list, when the call lists rows instead of adding them up |
| `sort` | a column or an aggregate, ascending or descending |
| `limit` | rows in the result, 20 by default and 200 at most |

With only a path, the tool describes the sheet: its columns, each column's type and sample values, the header row it chose, and the first five rows. The model looks first, then asks. A call such as

```json
{"path": "trip-expenses.csv",
 "where": [{"column": "City", "op": "=", "value": "Amsterdam"},
           {"column": "Category", "op": "=", "value": "Food tour"}],
 "aggregate": [{"fn": "sum", "column": "Amount"}]}
```

returns a small table and one citation:

```text
Matched 4 of 63 rows (rows 12–41) in trip-expenses.
| sum(Amount) |
| 277.00 |
Cite this table as [3].
```

### Reading values

- **Numbers** may carry a currency symbol or code (`EUR 64.50`, `€1,234.50`), thousands commas, parentheses or a minus sign for a negative, and a trailing `%`. A dot is the decimal point; `1.234,56` stays text.
- **Dates** read as `2026-05-03` or `2026/05/03`. A date such as `03/04/2026` reads day first when the column has a first part above 12, and month first otherwise; the description says which.
- **Sums are exact.** The tool adds decimals as scaled integers, so 0.10 + 0.20 is 0.30 and a column of cents adds up to the cent.

## Limits and safety

A Word or Excel file can be built to hurt the program that opens it. Each reader stops at a limit, says why in one line, and keeps `merud` running.

| Limit | Value | Stops |
| --- | --- | --- |
| File size | `[index] max_file_mb`, 5 MB by default | a huge file |
| Entries in the ZIP archive | 10,000 | an archive of tiny entries |
| One unpacked XML part | 64 MiB | a ZIP bomb: a few KB that unpack into gigabytes |
| All parts of one file | 256 MiB | the same, spread over many parts |
| Rows in a sheet | 100,000, empty rows counted | a sheet of a million empty rows |
| Columns in a sheet | 1,024 | a row that runs to column XFD |
| Cells in one file | 2,000,000 | many medium sheets |
| `where` conditions, `in` values | 10 and 100 | a call that makes every row compare against thousands of values |
| Groups | 10,000 | a group by a unique column |

- Meru reads ZIP parts into memory and never writes them to disk, so an entry named `../../.zshrc` can't become a file.
- A password-protected Office file starts with a different signature; the reader says "the file is password-protected" instead of failing on a broken archive.
- Go's XML reader doesn't expand entities a document defines, so a "billion laughs" file can't grow in memory. Each reader also recovers from a parser panic, as the PDF reader does.
- Cell text reaches the model only as a tool result, through the same size cut and redaction as every tool result. A cell that reads like an instruction is data in a table.
- `analyze_table` goes through `dispatch` like every tool: an allowlist entry, a `tool_calls` row and transcript lines for each call.

## What it doesn't do

- Run code the model writes, in any language. A table question gets fixed fields; nothing reaches an interpreter. Meru has no sandbox.
- Read `.doc`, `.xls`, PowerPoint, Pages, Numbers or OpenDocument files. The old `.xls` and `.doc` formats keep binary records inside an OLE compound file, which Go's standard library can't read, and a reader for them would be Meru's largest parser of hostile input. Excel and Word have saved `.xlsx` and `.docx` by default since 2007. For an old file, `read_file` and `analyze_table` say to open it and save it as `.xlsx` or `.docx`.
- Analyze tables inside Word files. Search, `read_file` and `grep` read them as text.
- Evaluate formulas, write or edit a spreadsheet, draw charts, build pivot tables, join two files, or compute new columns.
- Read decimal commas, or text in pictures inside a document.

## Where the code goes

| Path | Holds |
| --- | --- |
| `internal/index/ooxml.go` | opening a Word or Excel file's ZIP parts, with the limits |
| `internal/index/docx.go`, `xlsx.go`, `csv.go` | the three readers; `csv.go` covers every delimited format |
| `internal/index/table.go` | `Table`, `ReadTable` and chunking by rows |
| `internal/builtin/table.go`, `tablequery.go` | `analyze_table`: the schema, the checks, the values, the query |
| `internal/retrieve`, `internal/rpc` | the `table` citation kind, printed as rows |

The work ships in two pull requests: the readers first, which give search, `read_file` and `grep` all three formats, then `analyze_table`.

## Related

- [ARCHITECTURE.md](../../ARCHITECTURE.md): "What gets indexed", "Chunking", "Approving a tool call" and "Deliberate non-goals".
- [docs/coding-notes/index.md](../coding-notes/index.md) and [builtin.md](../coding-notes/builtin.md), once the code lands.
