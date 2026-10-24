# Slides

Four decks about Meru, each a self-contained HTML page and a PDF printed from it.

| Deck | For | Slides |
| --- | --- | --- |
| [meru-overview](meru-overview.html) ([PDF](meru-overview.pdf)) | anyone: why Meru exists, what it does, how it keeps data home, what it offers an organization as a private AI workspace, and what comes next | 14 |
| [meru-developers](meru-developers.html) ([PDF](meru-developers.pdf)) | developers: the design rules and non-negotiables, the daemon and its clients, the harness, tools, connectors, storage and observability, how the project works, and where to contribute | 16 |
| [meru-individuals](meru-individuals.html) ([PDF](meru-individuals.pdf)) | one person on their own Mac: what it does with their files, mail, notes and pictures, what it remembers, what leaves the Mac, how they stay in charge, which Mac they need, getting started, how it compares with Meta's Muse and OpenClaw, what it does not do yet, and how a question is answered | 15 |
| [meru-organizations](meru-organizations.html) ([PDF](meru-organizations.pdf)) | a CTO or CIO weighing a pilot, and a platform team weighing a managed offering: the architecture inside `merud` and one question end to end, what leaves the machine, the controls and audit, measured quality, speed and memory, what rollout takes, how it compares with Meta's Muse and with OpenClaw, what Meru lacks for a fleet, and a four-week pilot | 18 |

All four use the logo's colours and fonts from the landing page, and open with a QR
code for the repository and close with it. The developer, organization and individual decks reuse figures from
`docs/architecture/img/`.

Open a deck in a browser and scroll, or use the back and forward buttons at the bottom right of each slide; each slide fills the window. The buttons are plain links, and printing leaves them out. GitHub Pages
serves them at `aarora79.github.io/meru/slides/`.

**To print the PDF again** after an edit, with Chrome:

```sh
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless=new \
  --no-pdf-header-footer --print-to-pdf=docs/slides/meru-overview.pdf \
  "file://$PWD/docs/slides/meru-overview.html"
```

Each slide is 1280 by 720 pixels, and the page's print styles give each one a page
of that size. The overview and developer decks date from v0.4.18 and the organization and individual decks from v0.4.19, October 2026; update
them with a release that changes what a slide says.
