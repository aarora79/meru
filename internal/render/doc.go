// Package render loads a web page in a headless Chrome and returns the HTML
// the page shows once its scripts have run. web_fetch calls it when plain
// HTTP brings back an empty shell, such as a Workday job posting, whose
// text a script fills in. See ARCHITECTURE.md, "Pages that need
// JavaScript".
//
// A Renderer installs the pinned chrome-headless-shell into
// ~/.meru/runtime the first time a page needs it (internal/connectors
// downloads and checks it). For each page it starts a proxy on loopback
// and a fresh Chrome with a fresh profile, drives Chrome over the DevTools
// Protocol through two pipes rather than a network port, reads the page,
// and stops both. No browser runs between pages.
//
// Every request the page makes goes through the proxy, which dials with
// web_fetch's own check, so the page can't reach this machine or the local
// network. The proxy refuses all traffic until the page starts to load,
// so Chrome's own start-up requests go nowhere.
//
// It chooses not to click, type, scroll, take screenshots, keep cookies or
// load more than one page at a time, and it doesn't run on Windows, where
// Go can't pass Chrome its pipes.
package render
