//go:build desktop

// Command meru-installer is the guided Mac installer, "Install Meru.app" on
// the disk image. It opens a window, drawn by the system's WebView through
// Wails v3 with the same fonts and colours as Meru.app, and walks the user
// through ten steps: the Mac check, Meru itself, Ollama and the models,
// folders, web search, Obsidian, skills and commands, Google, About you,
// and starting merud. See ARCHITECTURE.md, "Installer".
//
// Usage: open "Install Meru.app". It takes no flags.
//
// Like meru-desktop, it builds only with the desktop build tag, because
// Wails needs cgo: `make installer-app`. The //go:build line at the top
// keeps it out of `go build ./...` and CI's cross-compiles.
//
// This file only opens the window, serves the page and binds the Bridge.
// The steps, and every program the installer runs, live in
// internal/installer, where tests run without Wails.
package main

import (
	"fmt"
	"os"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/aarora79/meru/internal/desktop"
	"github.com/aarora79/meru/internal/installer"
)

// main runs the installer and turns an error into exit status 1.
func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "meru-installer: %v\n", err)
		os.Exit(1)
	}
}

// run builds the Bridge, opens the window and blocks until the user quits.
// It fails when the home folder can't be found or Wails can't start.
func run() error {
	opts, err := installer.DefaultOptions()
	if err != nil {
		return err
	}

	// app is declared before it is set, so the functions below can refer
	// to it. The Bridge calls them only when the page asks, after
	// application.New has returned.
	var app *application.App
	opts.Emit = func(name string, data any) { app.Event.Emit(name, data) }
	opts.PickFolder = func() (string, error) {
		return app.Dialog.OpenFile().CanChooseFiles(false).CanChooseDirectories(true).
			SetTitle("Choose a folder for Meru to read").SetButtonText("Choose").PromptForSingleSelection()
	}
	opts.Quit = func() { app.Quit() }
	bridge := installer.New(opts)

	app = application.New(application.Options{
		Name:        "Install Meru",
		Description: desktop.Tagline,
		Services:    []application.Service{application.NewService(bridge)},
		Assets: application.AssetOptions{
			// The installer's own page, with Meru.app's page behind it for
			// app.css, the fonts and the logo.
			Handler:        installer.Assets(desktop.Assets()),
			DisableLogging: true,
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     "Install Meru",
		URL:       "/",
		Width:     1100,
		Height:    780,
		MinWidth:  900,
		MinHeight: 640,
		// The page's own background, so the window shows no white flash.
		BackgroundColour: application.NewRGB(0xF6, 0xF4, 0xEF),
	})
	return app.Run()
}
