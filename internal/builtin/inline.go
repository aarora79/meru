// This file puts pictures into an HTML page that write_file or edit_file
// saves: each <img src="..."> or CSS url(...) that names a picture inside
// the output folder becomes a data: URL holding the picture itself, so the
// page shows it wherever it goes, such as on a phone.
//
// A model can't do this itself: it writes text, and a photo's bytes aren't
// text it has. A real turn downloaded a family photo for a greeting card,
// then wrote a page with no picture in it and said the photo was
// "embedded (base64)". With this, the model writes the photo's path and
// Meru does the rest. See ARCHITECTURE.md, "Built-in skills".

package builtin

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/aarora79/meru/internal/rpc"
)

// maxPictureBytes caps one picture put into a page, and maxPageBytes the
// page with its pictures in. A phone photo is 2 to 6 MB; base64 grows it by
// a third. Past these, the page would be slow to open and to send.
const (
	maxPictureBytes = 16 << 20
	maxPageBytes    = 40 << 20
)

// pictureTypes maps the file endings a page may put in to their media type.
var pictureTypes = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png",
	".gif": "image/gif", ".webp": "image/webp", ".svg": "image/svg+xml",
}

// srcAttr matches a src="..." or src='...' attribute; group 2 or 3 holds
// the value. cssURL matches url(...), quoted or not; group 1, 2 or 3 holds
// it.
var (
	srcAttr = regexp.MustCompile(`(?i)\bsrc\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	cssURL  = regexp.MustCompile(`(?i)url\(\s*(?:"([^"]*)"|'([^']*)'|([^)"'\s]+))\s*\)`)
)

// isPage reports whether rel names an HTML file, the only kind whose
// pictures write_file and edit_file put in.
func isPage(rel string) bool {
	ext := strings.ToLower(filepath.Ext(rel))
	return ext == ".html" || ext == ".htm"
}

// inlinePictures returns page, an HTML page saved at rel inside root, with
// each local picture it names put in as a data: URL, and a line for the
// model saying what went in and what didn't. A picture counts as local when
// its path, relative to the page or written as ~/… or an absolute path or a
// file:// URL, lands inside the output folder; a web address, a data: URL
// or a path outside the folder stays as it is. The line is "" when the page
// names no local picture.
//
// It reads every picture through root, the os.Root on the output folder,
// so no path can read a file outside it, even through a symbolic link. It
// fails when the pictures would take the page past maxPageBytes.
func (t *Tools) inlinePictures(root *os.Root, rel, page string) (string, string, error) {
	var placed, missing []string
	total := len(page)
	// cache keeps each picture's data: URL, so a photo named twice is read
	// once and counted once.
	cache := map[string]string{}
	var tooBig error
	replace := func(ref string) (string, bool) {
		pic, why := t.localPicture(rel, ref)
		if why != "" {
			if why != "not local" {
				missing = append(missing, ref+" ("+why+")")
			}
			return "", false
		}
		if data, ok := cache[pic]; ok {
			return data, true
		}
		b, err := root.ReadFile(pic)
		switch {
		case err != nil:
			missing = append(missing, ref+" (no such file in "+t.outputDir+")")
			return "", false
		case len(b) > maxPictureBytes:
			missing = append(missing, ref+fmt.Sprintf(" (%d MB, over the %d MB limit)", len(b)>>20, maxPictureBytes>>20))
			return "", false
		}
		data := "data:" + pictureTypes[strings.ToLower(filepath.Ext(pic))] + ";base64," + base64.StdEncoding.EncodeToString(b)
		total += len(data)
		if total > maxPageBytes && tooBig == nil {
			tooBig = fmt.Errorf("the page with its pictures would be over %d MB; use fewer or smaller pictures", maxPageBytes>>20)
		}
		cache[pic] = data
		placed = append(placed, ref+" ("+size(int64(len(b)))+")")
		return data, true
	}
	// ReplaceAllStringFunc calls the function once per match and puts what
	// it returns in the match's place.
	page = srcAttr.ReplaceAllStringFunc(page, func(m string) string {
		g := srcAttr.FindStringSubmatch(m)
		ref, quote := g[1], `"`
		if ref == "" {
			ref, quote = g[2], `'`
		}
		if data, ok := replace(ref); ok {
			return "src=" + quote + data + quote
		}
		return m
	})
	page = cssURL.ReplaceAllStringFunc(page, func(m string) string {
		g := cssURL.FindStringSubmatch(m)
		ref := g[1] + g[2] + g[3]
		if data, ok := replace(ref); ok {
			return `url("` + data + `")`
		}
		return m
	})
	if tooBig != nil {
		return "", "", tooBig
	}
	var note []string
	if len(placed) > 0 {
		note = append(note, fmt.Sprintf("Put %s into the page, so it shows the pictures on its own, on a phone too: %s.",
			plural(len(placed), "picture"), strings.Join(placed, ", ")))
	}
	if len(missing) > 0 {
		note = append(note, "Left out, so the page shows nothing there: "+strings.Join(missing, ", ")+
			". Pass the picture's path inside "+t.outputDir+", such as a downloaded attachment's.")
	}
	return page, strings.Join(note, " "), nil
}

// localPicture returns the path, relative to the output folder, of the
// picture ref names in a page saved at rel, and "" for why. For a ref it
// leaves alone it returns why "not local"; for one it can't use, a short
// reason the model reads.
func (t *Tools) localPicture(rel, ref string) (pic, why string) {
	ref = strings.TrimSpace(ref)
	lower := strings.ToLower(ref)
	switch {
	case ref == "", strings.HasPrefix(lower, "data:"), strings.HasPrefix(lower, "http:"),
		strings.HasPrefix(lower, "https:"), strings.HasPrefix(ref, "//"), strings.HasPrefix(ref, "#"):
		return "", "not local"
	}
	if _, ok := pictureTypes[strings.ToLower(path.Ext(strings.SplitN(ref, "?", 2)[0]))]; !ok {
		return "", "not local"
	}
	if strings.HasPrefix(lower, "file:") {
		// rpc.LinkPath keeps a Windows drive in either form of link,
		// file:///C:/x or file://C:/x, and puts the home folder in place
		// of a ~, as in file:///~/meru-output/a.jpg. os.UserHomeDir fails
		// only when the system names no home folder; then a ~ link fails.
		home, _ := os.UserHomeDir()
		p, ok := rpc.LinkPath(ref, home)
		if !ok {
			return "", "not a file:// URL Meru can read"
		}
		ref = p
	} else if p, err := url.PathUnescape(ref); err == nil {
		ref = p // "my%20photo.jpg" names "my photo.jpg"
	}
	ref = strings.SplitN(ref, "?", 2)[0]
	var inside string
	if abs := t.insideOutput(ref); abs != ref || rooted(ref) || strings.HasPrefix(ref, "~") {
		// insideOutput turns a path that names the output folder into one
		// relative to it, and leaves any other absolute path alone.
		if rooted(abs) || strings.HasPrefix(abs, "~") {
			return "", "outside " + t.outputDir
		}
		inside = abs
	} else {
		// A path relative to the page, as a browser reads it.
		inside = path.Join(path.Dir(filepath.ToSlash(rel)), ref)
	}
	clean, err := cleanRelPath(inside)
	if err != nil {
		return "", "outside " + t.outputDir
	}
	return clean, ""
}

// rooted reports whether p starts at the top of a disk: an absolute path,
// or one that starts with / or \. On Windows /etc/x has no drive, so
// filepath.IsAbs says no, but a browser reads it from the top of the
// page's drive, outside the output folder, never relative to the page.
func rooted(p string) bool {
	return filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`)
}
