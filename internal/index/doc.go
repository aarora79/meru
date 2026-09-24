// Package index reads the folders listed in config.toml's [index] section,
// splits each file into chunks along its structure, embeds the chunks and
// hands them to the store. It is the "indexer: chunk + embed" arrow in
// ARCHITECTURE.md, "Storage", and the chunking rules in "Retrieval".
//
// The work splits into four parts, one or two files each:
//
//   - skip.go and ignore.go decide which files to read at all: hidden files,
//     build folders, binaries, media, secret files, and anything a .gitignore,
//     a .meruignore or the config's ignore list names.
//   - chunk.go, markdown.go, code.go, html.go and pdf.go cut a file's text
//     into chunks of about [index] chunk_tokens each.
//   - indexer.go walks the folders, skips files the store already holds
//     unchanged, embeds new chunks in batches and writes them.
//   - watch.go re-indexes a file soon after it changes while merud runs.
//
// What this package deliberately doesn't do: it doesn't open the database
// or write SQL (the store does), it doesn't search (retrieval does), and it
// never reads a file outside the configured folders, following no symlink.
package index
