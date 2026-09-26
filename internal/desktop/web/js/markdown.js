// Renders a finished answer's Markdown as DOM nodes. Model output is
// untrusted, so every answer goes through two gates:
//
//  1. marked turns Markdown into HTML, with any raw HTML in the answer
//     escaped into plain text.
//  2. DOMPurify keeps only the tags and attributes below, and only http,
//     https and file links. It returns a DocumentFragment, so no HTML
//     string ever reaches innerHTML.
//
// The page's Content-Security-Policy stands behind both: even a tag that
// slipped through could run no script and load nothing from the network.

import { Marked } from "../vendor/marked.esm.js";
import DOMPurify from "../vendor/purify.es.mjs";
import { icon } from "./icons.js";

// escapeHTML writes text so the browser shows it as text.
function escapeHTML(text) {
  return text
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;");
}

// One marked instance with GitHub-flavoured Markdown (tables, strike
// through, fenced code). Its html renderer escapes raw HTML blocks and
// inline tags, so "<img src=...>" in an answer shows as those characters.
const md = new Marked({
  gfm: true,
  breaks: false,
  renderer: {
    html(token) {
      return escapeHTML(token.text);
    },
  },
});

// What DOMPurify lets through. No images, forms, frames, styles or
// data-* attributes; links only to http, https or file.
const PURIFY = {
  ALLOWED_TAGS: [
    "p", "br", "hr", "strong", "em", "del", "code", "pre", "blockquote",
    "ul", "ol", "li", "a", "h1", "h2", "h3", "h4", "h5", "h6",
    "table", "thead", "tbody", "tr", "th", "td", "input",
  ],
  ALLOWED_ATTR: ["href", "title", "class", "start", "type", "checked", "disabled", "align"],
  ALLOW_DATA_ATTR: false,
  ALLOWED_URI_REGEXP: /^(?:https?|file):/i,
  RETURN_DOM_FRAGMENT: true,
};

// A task-list checkbox is the one input marked writes. DOMPurify can't
// limit an input's type, so this hook drops any input that isn't a
// disabled checkbox.
DOMPurify.addHook("uponSanitizeElement", (node, data) => {
  if (data.tagName === "input") {
    const ok = node.getAttribute("type") === "checkbox" && node.hasAttribute("disabled");
    if (!ok) node.remove();
  }
});

// The only class an answer keeps is marked's "language-xyz" on a code
// block. Any other class could borrow the page's own styles, and an answer
// dressed as an approval card could fool the reader.
DOMPurify.addHook("uponSanitizeAttribute", (node, data) => {
  if (data.attrName !== "class") return;
  const ok = node.nodeName === "CODE" && /^language-[\w+#.-]+$/.test(data.attrValue);
  if (!ok) data.keepAttr = false;
});

// renderMarkdown returns a DocumentFragment for text, sanitized, with a
// Copy button on each code block. onCopy(text, button) runs when one is
// pressed.
export function renderMarkdown(text, onCopy) {
  const html = md.parse(text, { async: false });
  const frag = DOMPurify.sanitize(html, PURIFY);
  for (const pre of frag.querySelectorAll("pre")) {
    wrapCode(pre, onCopy);
  }
  for (const a of frag.querySelectorAll("a[href]")) {
    // The page opens links through Go (see app.js); the title shows where
    // a click goes before the click.
    a.title = a.getAttribute("href");
  }
  return frag;
}

// wrapCode puts a code block in a box with a header that names its
// language, from marked's "language-go" class, and holds a Copy button.
function wrapCode(pre, onCopy) {
  const code = pre.querySelector("code");
  const lang = code ? [...code.classList].find((c) => c.startsWith("language-")) : null;
  const box = document.createElement("div");
  box.className = "code-block";
  const head = document.createElement("div");
  head.className = "code-head";
  const name = document.createElement("span");
  name.textContent = lang ? lang.slice("language-".length) : "code";
  const button = document.createElement("button");
  button.type = "button";
  button.className = "text-button";
  button.append(icon("copy", 14), document.createTextNode(" Copy"));
  button.setAttribute("aria-label", "Copy code");
  button.addEventListener("click", () => onCopy((code || pre).textContent, button));
  head.append(name, button);
  pre.replaceWith(box);
  box.append(head, pre);
}
