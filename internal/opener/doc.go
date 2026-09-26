// Package opener hands a URL to the program the system uses to open files
// and links, which starts the browser or the file's own app. Both clients
// that open links use it: `meru chat` when a click lands on a link, and the
// desktop app when the user clicks a link in an answer or a source chip.
//
// It opens only http, https and file URLs, refuses one that starts with
// "-", and starts the opener with no shell and the URL as one argument. So
// a link in model output can't run a command or pass the opener an option.
// See ARCHITECTURE.md, "Terminal UI" and "Desktop app".
//
// The package holds no state and opens nothing on its own: a person's
// click is the only thing that calls Open.
package opener
