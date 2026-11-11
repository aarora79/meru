# Meru test vault

FICTIONAL TEST DATA - every name, company and number in this folder is made
up, and made to look made up.

This folder is a made-up Obsidian vault for testing Meru: the indexer, hybrid
search, the file tools, tables and how answers handle private notes. It
belongs to **Jordan Testperson**, who runs a one-person consulting firm,
**Testbed Consulting LLC**, in Portland, Oregon. Point `[index] folders` at it
to try Meru without your own files:

```toml
[index]
folders = ["~/meru/testdata/vault"]
```

The names and numbers announce that they are fake, so no scanner or reader
mistakes them for a real person's data:

- People have surnames such as Testperson, Samplename, Mockwell and Fakerly.
- Companies, banks, insurers, shops and the hotel have names such as Acme
  Logistics, First Bank of Nowhere, Pretend Life and Example Hotel.
- ID numbers are all nines: SSNs run from 999-99-9991, the EIN is
  99-9999999, and passport, licence, policy and account numbers follow the
  same pattern.
- Street addresses use "Example" street names.
- Phone numbers use 555-01xx, which phone companies reserve for fiction.
- Emails use example.com, example.org and example.net.
- `Personal/credentials-old-laptop.txt` holds placeholder passwords such as
  `example-password-1`.
- Cities, museums and parks keep their real names, as do the card networks
  on the receipts.

What the indexer should do with each kind of file:

| Files | Expected |
| --- | --- |
| `.md`, `.txt`, `.pdf`, `.docx`, `.csv` | indexed |
| `Personal/credentials-old-laptop.txt` | skipped as a secret (`credentials*`) |
| `.obsidian/` | skipped as hidden |

Start at [[Home]].
