// Package memory reads and writes Meru's memory files: one Markdown file per
// fact, under ~/.meru/memory/<kind>/<slug>.md, where the folder names the
// kind (me, preferences, projects, people, reference, other, or any folder
// you add).
//
// The files are the source of truth (see ARCHITECTURE.md, "Memory"). Each one
// opens with a short frontmatter recording when the memory was made and which
// session made it, and the fact follows as plain text. You can read, edit or
// delete them by hand; this package reads whatever it finds.
//
// Every path a caller hands in goes through one check (resolve) and every file
// operation goes through an os.Root opened on the memory directory, so no
// memory ID, however it is written, can read or delete a file outside it.
// Symbolic links inside the memory directory are refused too.
//
// What this package deliberately doesn't do: it doesn't index or search
// memories (index.Memories copies them into the store, and
// retrieve.SearchMemories searches them there), doesn't decide what is
// worth remembering (the model does, through the remember tool), and keeps
// no cache or index file of its own.
package memory
