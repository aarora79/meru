# Slides

Two decks about Meru, each a self-contained HTML page and a PDF printed from it.

| Deck | For | Slides |
| --- | --- | --- |
| [meru-overview](meru-overview.html) ([PDF](meru-overview.pdf)) | anyone: why Meru exists, what it does, how it keeps data home, what it offers an organization as a private AI workspace, and what comes next | 14 |
| [meru-developers](meru-developers.html) ([PDF](meru-developers.pdf)) | developers: the design rules and non-negotiables, the daemon and its clients, the harness, tools, connectors, storage and observability, how the project works, and where to contribute | 16 |

Both use the logo's colours and fonts from the landing page, and open with a QR
code for the repository and close with it. The developer deck reuses figures from
`docs/architecture/img/`.

Open a deck in a browser and scroll; each slide fills the window. GitHub Pages
serves them at `aarora79.github.io/meru/slides/`.

**To print the PDF again** after an edit, with Chrome:

```sh
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless=new \
  --no-pdf-header-footer --print-to-pdf=docs/slides/meru-overview.pdf \
  "file://$PWD/docs/slides/meru-overview.html"
```

Each slide is 1280 by 720 pixels, and the page's print styles give each one a page
of that size. Version numbers and figures date from v0.4.18, October 2026; update
them with a release that changes what a slide says.
