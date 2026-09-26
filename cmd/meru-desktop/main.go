//go:build desktop

// Command meru-desktop is Meru's desktop app: a native window, drawn by the
// system's WebView through Wails v3, that talks to merud over the same Unix
// socket as `meru` and `meru chat`. See ARCHITECTURE.md, "Desktop app".
//
// Usage:
//
//	meru-desktop [-socket path]
//
// Wails needs cgo and the platform's WebView headers, so this command
// builds only with the desktop build tag: `make desktop`. The line at the
// top of the file, //go:build desktop, is a build constraint: without
// -tags desktop the go command skips the file, and so the whole command,
// which keeps `go build ./...` and CI's cross-compiles free of cgo.
//
// Everything that doesn't need a window lives in internal/desktop, where
// tests run on every machine. This file only opens the window, serves the
// page from internal/desktop, and binds the Bridge so the page can call it.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/aarora79/meru/internal/desktop"
)

// main runs the app and turns an error into exit status 1.
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "meru-desktop: %v\n", err)
		os.Exit(1)
	}
}

// run parses the flags, builds the Bridge, opens the window and blocks
// until the user quits. It fails on a bad flag, when the home folder can't
// be found, or when Wails can't start.
func run(args []string) error {
	flags := flag.NewFlagSet("meru-desktop", flag.ContinueOnError)
	socket := flags.String("socket", "", "merud's socket (default ~/.meru/merud.sock)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	opts, err := desktop.DefaultOptions(*socket)
	if err != nil {
		return err
	}

	// app is declared before it is set, so the functions below can refer
	// to it. The Bridge calls them only when the page asks, long after
	// application.New has returned.
	var app *application.App
	opts.Emit = func(name string, data any) { app.Event.Emit(name, data) }
	// The system's own dialogs choose a file to attach and a folder to
	// index. PromptForSingleSelection returns "" when the user cancels.
	opts.PickFile = func() (string, error) {
		return app.Dialog.OpenFile().CanChooseFiles(true).CanChooseDirectories(false).
			SetTitle("Attach a file").PromptForSingleSelection()
	}
	opts.PickFolder = func() (string, error) {
		return app.Dialog.OpenFile().CanChooseFiles(false).CanChooseDirectories(true).
			SetTitle("Choose a folder for Meru to index").SetButtonText("Choose").PromptForSingleSelection()
	}
	opts.Quit = func() { app.Quit() }
	bridge := desktop.New(opts)

	app = application.New(application.Options{
		Name:        "Meru",
		Description: desktop.Tagline,
		// NewService binds every exported method of the Bridge, so the
		// page can call it by name, and calls its ServiceShutdown when
		// the app quits.
		Services: []application.Service{application.NewService(bridge)},
		Assets: application.AssetOptions{
			// desktop.Assets serves the embedded page with its
			// Content-Security-Policy. Logging off keeps each file
			// request out of the terminal.
			Handler:        desktop.Assets(),
			DisableLogging: true,
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		// The title bar says what Meru is. The chat's own title shows in
		// the page's header, so the title bar never changes.
		Title:     desktop.WindowTitle,
		URL:       "/",
		Width:     1440,
		Height:    900,
		MinWidth:  1000,
		MinHeight: 640,
		// The page's own background, so the window shows no white flash
		// before the page draws.
		BackgroundColour: application.NewRGB(0xF6, 0xF4, 0xEF),
	})
	return app.Run()
}
