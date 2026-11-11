# Meru test vault

FICTIONAL TEST DATA - every name, place, company and number in this folder is
made up.

This folder is a made-up Obsidian vault for testing Meru: the indexer, hybrid
search, the file tools, tables and how answers handle private notes. It
belongs to a fictional person, **Jordan Reyes**, who runs a one-person
consulting firm, **Cedar Ridge Consulting LLC**, in Portland, Oregon. Point
`[index] folders` at it to try Meru without your own files:

```toml
[index]
folders = ["~/meru/testdata/vault"]
```

The vault holds no government ID, bank account or card number, not even a
fake one, so no scanner mistakes it for leaked personal data:

- Street addresses use "Example" street names.
- Phone numbers use 555-01xx, which phone companies reserve for fiction.
- Emails use example.com and example.org.
- `Personal/credentials-old-laptop.txt` holds placeholder passwords such as
  `example-password-1`.

What the indexer should do with each kind of file:

| Files | Expected |
| --- | --- |
| `.md`, `.txt`, `.pdf`, `.docx`, `.csv` | indexed |
| `Personal/credentials-old-laptop.txt` | skipped as a secret (`credentials*`) |
| `.obsidian/` | skipped as hidden |

Start at [[Home]].
