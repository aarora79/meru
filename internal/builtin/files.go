// This file holds the three read-only file tools: read_file, list_folder
// and grep. They let the model open a whole file, see what a folder holds
// and find every line that matches, where search alone gives it ten
// excerpts. They reach the [index] folders and the [skills] output_dir,
// and apply the indexer's skip rules through the index package, so in the
// [index] folders they read exactly what search can read. See
// ARCHITECTURE.md, "Approving a tool call".

package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/index"
)

// The file tools' names, as the model sees them.
const (
	ReadFile   = "read_file"
	ListFolder = "list_folder"
	Grep       = "grep"
)

// attachmentsFolder is the folder under [skills] output_dir where the
// google server saves the mail attachments the model asks for. The
// catalog's start command points WORKSPACE_ATTACHMENT_DIR there. A path
// that names no file in any folder the tools read gets one more try in
// this folder, so the model can pass the saved filename that the
// attachment tool reports, as it stands.
const attachmentsFolder = "attachments"

// IsFileTool reports whether name is one of the four file tools: the three
// here and search_files. The agent offers these alone on the "search"
// route.
func IsFileTool(name string) bool {
	return name == ReadFile || name == ListFolder || name == Grep || name == SearchFiles
}

// Limits on what one call returns. dispatch cuts any result at 16,000
// characters; these keep a result under that, so the closing lines that
// say what was left out survive.
const (
	// maxReadChars is how much text read_file returns per call.
	maxReadChars = 12000
	// maxEntries is how many files and folders list_folder shows.
	maxEntries = 300
	// maxListChars caps list_folder's lines, whatever their count.
	maxListChars = 14000
	// maxDepth is how deep list_folder goes.
	maxDepth = 3
	// maxLineChars cuts each line grep returns.
	maxLineChars = 200
	// defaultMatches and maxMatches bound grep's max_results.
	defaultMatches = 50
	maxMatches     = 200
	// grepTime and grepFiles stop a grep that runs too long. A turn waits
	// for the result, and 5 seconds of searching is already a long wait.
	grepTime  = 5 * time.Second
	grepFiles = 20000
)

// reasonText says, for the model, why the indexer skips a path. The keys
// are the index.Reason constants.
var reasonText = map[string]string{
	index.ReasonHidden:      "hidden file or folder",
	index.ReasonBuildFolder: "build or dependency folder",
	index.ReasonSecret:      "secret file",
	index.ReasonIgnored:     "ignored by .gitignore, .meruignore or [index] ignore",
	index.ReasonSymlink:     "symbolic link",
	index.ReasonMedia:       "media file",
	index.ReasonBinary:      "binary file",
	index.ReasonUnsupported: "file type Meru doesn't read",
	index.ReasonTooLarge:    "larger than [index] max_file_mb",
}

// why returns reasonText for reason, or reason itself for one the map
// lacks.
func why(reason string) string {
	if s, ok := reasonText[reason]; ok {
		return s
	}
	return reason
}

// readFileArgs, listFolderArgs and grepArgs are the JSON objects the model
// sends to each tool.
type readFileArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
}

type listFolderArgs struct {
	Path  string `json:"path"`
	Depth int    `json:"depth"`
}

type grepArgs struct {
	Pattern       string `json:"pattern"`
	Path          string `json:"path"`
	Regex         bool   `json:"regex"`
	CaseSensitive bool   `json:"case_sensitive"`
	MaxResults    int    `json:"max_results"`
}

// decode reads raw into v and refuses keys v doesn't name, so a misspelt
// argument shows up instead of being dropped. tool names the tool in the
// error.
func decode(tool string, raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: the arguments aren't a valid JSON object for this tool: %v", tool, err)
	}
	return nil
}

// readFile returns one page of a file's text, up to maxReadChars
// characters from offset, headed by the file's path, size and modified
// date. It fails, with text the model reads, when the path can't be
// resolved or is skipped, when it names a folder, when the offset is out
// of range, and when the file can't be read.
func (t *Tools) readFile(raw json.RawMessage) (string, error) {
	var a readFileArgs
	if err := decode(ReadFile, raw, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", errors.New("read_file: pass path, the file to read")
	}
	if a.Offset < 0 {
		return "", fmt.Errorf("read_file: offset %d is negative", a.Offset)
	}
	c, err := t.resolve(ReadFile, a.Path)
	if err != nil {
		return "", err
	}
	if c.Info.IsDir() {
		return "", fmt.Errorf("read_file: %s is a folder; list it with list_folder", t.show(c.Path))
	}
	text, reason, err := t.files.ReadText(c.Path)
	if err != nil {
		return "", fmt.Errorf("read_file: %s: %v", t.show(c.Path), err)
	}
	if reason != "" {
		return "", fmt.Errorf("read_file: %s is off limits: %s", t.show(c.Path), why(reason))
	}

	full := joinPages(text)
	// Offsets count characters, not bytes, so a page never ends inside a
	// multi-byte character. []rune(s) turns the text into its characters.
	runes := []rune(full)
	if a.Offset > len(runes) {
		return "", fmt.Errorf("read_file: offset %d is past the end; the file has %d characters", a.Offset, len(runes))
	}
	end := min(a.Offset+maxReadChars, len(runes))

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s, modified %s. Characters %d to %d of %d.\n\n",
		t.show(c.Path), size(c.Info.Size()), c.Info.ModTime().Format("2006-01-02 15:04"), a.Offset, end, len(runes))
	b.WriteString(string(runes[a.Offset:end]))
	if end < len(runes) {
		fmt.Fprintf(&b, "\n\n[%d more characters. To read on, call read_file with offset %d.]", len(runes)-end, end)
	}
	return b.String(), nil
}

// joinPages returns a file's text as one string. A PDF's pages each start
// with a "--- page N ---" line, so the model can cite the page.
func joinPages(text index.Text) string {
	if text.Kind != index.KindPDF {
		return strings.Join(text.Pages, "")
	}
	var b strings.Builder
	for i, p := range text.Pages {
		fmt.Fprintf(&b, "--- page %d ---\n%s\n", i+1, strings.TrimSpace(p))
	}
	return b.String()
}

// listFolder lists a folder's files and folders to depth levels, folders
// first, each file with its size and modified date. It leaves out what the
// indexer skips and ends with a line counting those by reason. With no
// path it lists the [index] folders. It fails when the path can't be
// resolved, is skipped or is a file, when depth is out of range, and when
// ctx ends.
func (t *Tools) listFolder(ctx context.Context, raw json.RawMessage) (string, error) {
	var a listFolderArgs
	if err := decode(ListFolder, raw, &a); err != nil {
		return "", err
	}
	if a.Depth == 0 {
		a.Depth = 1
	}
	if a.Depth < 1 || a.Depth > maxDepth {
		return "", fmt.Errorf("list_folder: depth %d is out of range; pass 1 to %d", a.Depth, maxDepth)
	}
	if strings.TrimSpace(a.Path) == "" {
		return t.listRoots(), nil
	}
	c, err := t.resolve(ListFolder, a.Path)
	if err != nil {
		return "", err
	}
	if !c.Info.IsDir() {
		return "", fmt.Errorf("list_folder: %s is a file; read it with read_file", t.show(c.Path))
	}

	var folders, files []string
	skipped := map[string]int{}
	more := false
	chars := 0
	err = t.files.Walk(ctx, c.Path, func(p string, info fs.FileInfo, reason string) error {
		if reason != "" {
			skipped[reason]++
			return nil
		}
		// Rel can't fail: Walk passes only paths under c.Path.
		rel, _ := filepath.Rel(c.Path, p)
		rel = filepath.ToSlash(rel)
		var line string
		if info.IsDir() {
			line = rel + "/"
		} else {
			line = fmt.Sprintf("%s  %s  %s", rel, size(info.Size()), info.ModTime().Format("2006-01-02"))
		}
		if len(folders)+len(files) >= maxEntries || chars+len(line) > maxListChars {
			more = true
			return fs.SkipAll
		}
		chars += len(line) + 1
		if info.IsDir() {
			folders = append(folders, line)
			// A folder at the last level is shown but not entered.
			if strings.Count(rel, "/")+1 >= a.Depth {
				return fs.SkipDir
			}
			return nil
		}
		files = append(files, line)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("list_folder: %s: %w", t.show(c.Path), err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s (depth %d): %s, %s.\n", t.show(c.Path), a.Depth, plural(len(folders), "folder"), plural(len(files), "file"))
	for _, f := range folders {
		b.WriteString(f + "\n")
	}
	for _, f := range files {
		b.WriteString(f + "\n")
	}
	if more {
		fmt.Fprintf(&b, "More entries exist than one list shows (%d at most). List a subfolder, or pass a smaller depth.\n", maxEntries)
	}
	if n := skippedLine(skipped); n != "" {
		b.WriteString(n + "\n")
	}
	return b.String(), nil
}

// listRoots lists the folders the file tools read, the [index] folders
// then the output folder, for list_folder with no path.
func (t *Tools) listRoots() string {
	roots := t.files.Roots()
	if len(roots) == 0 {
		return "No indexed folders exist. The user adds folders to [index] folders in ~/.meru/config.toml."
	}
	var b strings.Builder
	b.WriteString("The folders the file tools read. Pass one as path to see what it holds.\n")
	for _, r := range roots {
		b.WriteString(t.show(r) + "/\n")
	}
	return b.String()
}

// skippedLine counts the skipped entries by reason, largest count first,
// such as "Left out 7 that Meru doesn't read: hidden file or folder (4),
// secret file (3)." It returns "" when
// nothing was skipped.
func skippedLine(skipped map[string]int) string {
	total := 0
	reasons := make([]string, 0, len(skipped))
	for r, n := range skipped {
		total += n
		reasons = append(reasons, r)
	}
	if total == 0 {
		return ""
	}
	// Sort by count, then by name, so the line reads the same every time.
	sort.Slice(reasons, func(i, j int) bool {
		if skipped[reasons[i]] != skipped[reasons[j]] {
			return skipped[reasons[i]] > skipped[reasons[j]]
		}
		return reasons[i] < reasons[j]
	})
	parts := make([]string, len(reasons))
	for i, r := range reasons {
		parts[i] = fmt.Sprintf("%s (%d)", why(r), skipped[r])
	}
	return fmt.Sprintf("Left out %d that Meru doesn't read: %s.", total, strings.Join(parts, ", "))
}

// grep searches the text of every file the indexer reads under a folder or
// in one file, and returns the lines that match, each as "path:line: text"
// or, for a PDF, "path (page N): text". It stops at max_results matches,
// after grepTime, or after grepFiles files, and says which stopped it. It
// fails when the pattern is empty or an invalid regular expression, when
// the path can't be resolved or is skipped, and when ctx ends.
func (t *Tools) grep(ctx context.Context, raw json.RawMessage) (string, error) {
	var a grepArgs
	if err := decode(Grep, raw, &a); err != nil {
		return "", err
	}
	if a.Pattern == "" {
		return "", errors.New("grep: pass pattern, the text to find")
	}
	if a.MaxResults == 0 {
		a.MaxResults = defaultMatches
	}
	if a.MaxResults < 1 || a.MaxResults > maxMatches {
		return "", fmt.Errorf("grep: max_results %d is out of range; pass 1 to %d", a.MaxResults, maxMatches)
	}
	expr := a.Pattern
	if !a.Regex {
		expr = regexp.QuoteMeta(expr)
	}
	if !a.CaseSensitive {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return "", fmt.Errorf("grep: pattern %q isn't a valid regular expression: %v", a.Pattern, err)
	}

	// Build the list of places to search: one checked path, or every
	// [index] folder.
	var starts []index.Checked
	if strings.TrimSpace(a.Path) == "" {
		for _, r := range t.files.Roots() {
			info, err := os.Lstat(r)
			if err != nil {
				continue
			}
			starts = append(starts, index.Checked{Path: r, Root: r, Info: info})
		}
		if len(starts) == 0 {
			return "", errors.New("grep: no indexed folders exist")
		}
	} else {
		c, err := t.resolve(Grep, a.Path)
		if err != nil {
			return "", err
		}
		starts = append(starts, c)
	}

	s := &grepRun{t: t, re: re, max: a.MaxResults, timeLimit: grepTime, fileLimit: grepFiles}
	if err := s.search(ctx, starts); err != nil {
		return "", fmt.Errorf("grep: %w", err)
	}
	return s.report(a.Pattern, starts), nil
}

// grepRun holds one grep call's settings and progress. The limits sit
// here, not only in the constants, so a test can set small ones.
type grepRun struct {
	t         *Tools
	re        *regexp.Regexp
	max       int           // stop after this many matches
	timeLimit time.Duration // stop starting new files after this long
	fileLimit int           // stop after searching this many files

	start   time.Time       // when the search began
	lines   []string        // the matches so far, formatted
	files   map[string]bool // the files that matched, by path
	scanned int             // files searched
	failed  int             // files that couldn't be read
	stop    string          // which limit stopped the search; "" while it runs
}

// search runs the search over starts, each a file or a folder, in order,
// until a limit stops it. It fails when ctx ends or a walk fails.
func (s *grepRun) search(ctx context.Context, starts []index.Checked) error {
	s.start = time.Now()
	s.files = map[string]bool{}
	for _, c := range starts {
		if s.stop != "" {
			break
		}
		if !c.Info.IsDir() {
			if err := s.file(ctx, c.Path); err != nil {
				return err
			}
			continue
		}
		err := s.t.files.Walk(ctx, c.Path, func(p string, info fs.FileInfo, reason string) error {
			if reason != "" || info.IsDir() {
				return nil
			}
			if err := s.file(ctx, p); err != nil {
				return err
			}
			if s.stop != "" {
				return fs.SkipAll
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// file searches the file at p and adds its matching lines. It sets s.stop
// when a limit is reached, and returns ctx's error when ctx ends.
func (s *grepRun) file(ctx context.Context, p string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch {
	case time.Since(s.start) > s.timeLimit:
		s.stop = fmt.Sprintf("the time limit (%v)", s.timeLimit)
		return nil
	case s.scanned >= s.fileLimit:
		s.stop = fmt.Sprintf("the limit of %d files", s.fileLimit)
		return nil
	}
	s.scanned++
	text, reason, err := s.t.files.ReadText(p)
	if err != nil || reason != "" {
		s.failed++
		return nil
	}
	shown := s.t.show(p)
	for i, page := range text.Pages {
		for n, line := range strings.Split(page, "\n") {
			if !s.re.MatchString(line) {
				continue
			}
			where := fmt.Sprintf("%s:%d", shown, n+1)
			if text.Kind == index.KindPDF {
				where = fmt.Sprintf("%s (page %d)", shown, i+1)
			}
			s.lines = append(s.lines, where+": "+cut(strings.TrimSpace(line), maxLineChars))
			s.files[p] = true
			if len(s.lines) >= s.max {
				s.stop = fmt.Sprintf("max_results (%d)", s.max)
				return nil
			}
		}
	}
	return nil
}

// report formats the result: a first line with the counts and any limit
// that stopped the search, then one line per match. The counts come first
// so they survive if dispatch cuts a long result.
func (s *grepRun) report(pattern string, starts []index.Checked) string {
	where := make([]string, len(starts))
	for i, c := range starts {
		where[i] = s.t.show(c.Path)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "grep %q in %s: %s in %s; searched %s.",
		pattern, strings.Join(where, ", "), plural(len(s.lines), "matching line"), plural(len(s.files), "file"),
		plural(s.scanned, "file"))
	if s.failed > 0 {
		fmt.Fprintf(&b, " Couldn't read %s.", plural(s.failed, "file"))
	}
	if s.stop != "" {
		fmt.Fprintf(&b, " Stopped early at %s; narrow path or pattern to see the rest.", s.stop)
	}
	b.WriteString("\n")
	for _, l := range s.lines {
		b.WriteString(l + "\n")
	}
	return b.String()
}

// resolve turns the path the model gave into a path the indexer reads. The
// path may be absolute, start with "~/", be relative to a folder the tools
// read when exactly one of them holds it, or be the name of a file in the
// attachments folder. It fails, naming tool, when the path doesn't exist,
// sits outside those folders, matches in more than one, or is one the
// indexer skips.
func (t *Tools) resolve(tool, p string) (index.Checked, error) {
	p = strings.TrimSpace(p)
	abs, err := t.absPath(tool, p)
	if err != nil {
		return index.Checked{}, err
	}
	c, err := t.files.Check(abs)
	switch {
	case errors.Is(err, index.ErrOutsideFolders):
		return index.Checked{}, fmt.Errorf("%s: %s is outside the folders the file tools read. They read only inside: %s",
			tool, p, t.rootList())
	case errors.Is(err, fs.ErrNotExist):
		return index.Checked{}, fmt.Errorf("%s: %s doesn't exist. List its folder with list_folder to see what is there", tool, p)
	case err != nil:
		return index.Checked{}, fmt.Errorf("%s: %s: %v", tool, p, err)
	case c.Reason != "":
		return index.Checked{}, fmt.Errorf("%s: %s is off limits: %s. Meru reads only the files it indexes", tool, p, why(c.Reason))
	}
	return c, nil
}

// savedAttachment returns the file in the attachments folder that has the
// same name as abs, when abs doesn't exist, sits inside Meru's output
// folder, and that file does. Otherwise it returns abs unchanged.
//
// A mail tool reports only the saved file's name, so the model guesses the
// folder. In testing it asked for ~/meru-output/downloads/<name> when the
// server had saved ~/meru-output/attachments/<name>. Only the output folder
// gets this second look, and Check still applies every rule to the result.
func (t *Tools) savedAttachment(abs string) string {
	if t.outputDir == "" {
		return abs
	}
	if _, err := os.Lstat(abs); err == nil {
		return abs
	}
	rel, err := filepath.Rel(t.outputDir, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return abs
	}
	cand := filepath.Join(t.outputDir, attachmentsFolder, filepath.Base(abs))
	if _, err := os.Lstat(cand); err == nil {
		return cand
	}
	return abs
}

// absPath expands "~" and turns a relative path into an absolute one inside
// the one folder that holds it, trying the attachments folder last. It
// fails when no folder holds the relative path, or more than one does.
func (t *Tools) absPath(tool, p string) (string, error) {
	switch {
	case p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`):
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("%s: find the home folder: %w", tool, err)
		}
		return t.savedAttachment(filepath.Join(home, p[1:])), nil
	case filepath.IsAbs(p):
		return t.savedAttachment(p), nil
	}
	var found []string
	for _, r := range t.files.Roots() {
		// Join cleans the result, so "../x" can land outside r, and two
		// folders can reach the same place; Check refuses the first, and
		// the Contains test keeps the second from counting twice.
		cand := filepath.Join(r, p)
		if _, err := os.Lstat(cand); err == nil && !slices.Contains(found, cand) {
			found = append(found, cand)
		}
	}
	// The attachments folder comes last, and only when nothing else
	// matched, so a saved filename never makes a path in an [index] folder
	// ambiguous. Check still applies every rule to what this finds.
	if len(found) == 0 && t.outputDir != "" {
		cand := filepath.Join(t.outputDir, attachmentsFolder, p)
		if _, err := os.Lstat(cand); err == nil {
			found = append(found, cand)
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("%s: no folder the file tools read holds %q. Pass a full path, or one relative to one of: %s",
			tool, p, t.rootList())
	case 1:
		return found[0], nil
	}
	shown := make([]string, len(found))
	for i, f := range found {
		shown[i] = t.show(f)
	}
	return "", fmt.Errorf("%s: %q exists in more than one folder: %s. Pass the full path",
		tool, p, strings.Join(shown, ", "))
}

// rootList names the folders the file tools read, for an error message
// and the tool descriptions: the [index] folders, then the output folder.
func (t *Tools) rootList() string {
	return t.nameFolders(t.files.Roots())
}

// nameFolders joins roots, each written with "~" for the home folder, or
// says there are none.
func (t *Tools) nameFolders(roots []string) string {
	if len(roots) == 0 {
		return "none (no such folder exists)"
	}
	shown := make([]string, len(roots))
	for i, r := range roots {
		shown[i] = t.show(r)
	}
	return strings.Join(shown, ", ")
}

// show writes p with the home folder as "~", which is shorter for the model
// to read and to pass back.
func (t *Tools) show(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	rel, err := filepath.Rel(home, p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return p
	}
	return "~" + string(filepath.Separator) + rel
}

// size writes n bytes in the largest unit that keeps it at least 1.
func size(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d bytes", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
}

// plural writes n and a noun, adding "s" to the noun unless n is 1:
// "1 file", "3 files".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// cut shortens s to at most n characters, marking a cut with "…".
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// fileToolSpecs returns the specs of the three file tools. The
// descriptions name the folders the tools read, so the model knows where
// it may look. read_file's also says it takes a saved attachment's
// filename, since that is the one path the model gets from a tool result
// and not from the user.
func (t *Tools) fileToolSpecs() []engine.ToolSpec {
	where := "Paths may be absolute, start with \"~/\", or be relative to one of the folders below when only one holds them. " +
		"The folders are: " + t.rootList() + ". " +
		"Meru refuses paths outside them, and skips hidden, secret, ignored and binary files, as search does."
	attachments := ""
	if t.outputDir != "" {
		attachments = "To read a mail attachment that a tool saved, pass the saved filename it reported as path. "
	}
	return []engine.ToolSpec{
		{
			Name: ReadFile,
			Description: "Reads one of the user's files in full: Markdown, text, code, HTML (as text) and PDF (page by page). " +
				"Use it when the search excerpts aren't enough. It returns up to 12,000 characters per call; " +
				"when the file is longer, the result ends with the offset to pass next. " + attachments + where,
			Parameters: mustSchema(map[string]any{
				"path":   prop("string", "The file to read."),
				"offset": prop("integer", "Where to start, in characters from the start of the file. Default 0."),
			}, "path"),
		},
		{
			Name: ListFolder,
			Description: "Lists the files and folders in one of the user's folders: folders first, then files with size and modified date. " +
				"With no path it lists the folders it may read. " + where,
			Parameters: mustSchema(map[string]any{
				"path":  prop("string", "The folder to list. Leave it out to list the folders it may read."),
				"depth": prop("integer", "How many levels to list, 1 to 3. Default 1."),
			}),
		},
		{
			Name: Grep,
			Description: "Finds every line that holds a word or pattern in the user's files, and returns \"path:line: text\" lines " +
				"(\"path (page N): text\" for PDFs). Use it to find which files mention something. " +
				"By default it matches plain text, ignoring case, in every folder it may read. " + where,
			Parameters: mustSchema(map[string]any{
				"pattern":        prop("string", "The text to find, or a regular expression when regex is true."),
				"path":           prop("string", "A folder or file to search. Leave it out to search every folder it may read."),
				"regex":          prop("boolean", "Treat pattern as a regular expression (Go RE2 syntax). Default false."),
				"case_sensitive": prop("boolean", "Match upper and lower case exactly. Default false."),
				"max_results":    prop("integer", "Most lines to return, 1 to 200. Default 50."),
			}, "pattern"),
		},
	}
}

// prop builds one property of a JSON Schema.
func prop(typ, description string) map[string]any {
	return map[string]any{"type": typ, "description": description}
}

// mustSchema builds an object schema from properties, with the listed
// ones required. json.Marshal can't fail on these plain values, so it
// drops the error.
func mustSchema(properties map[string]any, required ...string) json.RawMessage {
	s := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	b, _ := json.Marshal(s)
	return b
}
