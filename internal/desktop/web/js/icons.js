// Stroke icons for the page, drawn as inline SVG. Each icon is a list of
// SVG path strings on a 24 by 24 grid. icon() builds the element with the
// DOM, never from an HTML string, so no markup is parsed.

const SVG = "http://www.w3.org/2000/svg";

const PATHS = {
  plus: ["M12 5 V19", "M5 12 H19"],
  search: ["M10.5 17 A6.5 6.5 0 1 0 10.5 4 A6.5 6.5 0 1 0 10.5 17 Z", "M15.5 15.5 L20 20"],
  copy: ["M9 9 H19 V19 H9 Z", "M5 15 V5 H15"],
  retry: ["M4 12 A8 8 0 1 0 6.5 6.2", "M4 4 V9 H9"],
  stop: ["M7 7 H17 V17 H7 Z"],
  send: ["M12 19 V5", "M6 11 L12 5 L18 11"],
  file: ["M6 3 H14 L19 8 V21 H6 Z", "M14 3 V8 H19"],
  tool: ["M14.5 5.5 A4 4 0 0 0 9.8 10.8 L4 16.6 L7.4 20 L13.2 14.2 A4 4 0 0 0 18.5 9.5 L16 12 L12 8 Z"],
  ask: ["M12 3 L20 6.5 V12 C20 16.5 16.5 19.8 12 21 C7.5 19.8 4 16.5 4 12 V6.5 Z", "M12 8 V12.5", "M12 15.5 V16"],
  globe: ["M12 21 A9 9 0 1 0 12 3 A9 9 0 1 0 12 21 Z", "M3 12 H21", "M12 3 C9 6 9 18 12 21 C15 18 15 6 12 3 Z"],
  chevron: ["M9 6 L15 12 L9 18"],
  close: ["M6 6 L18 18", "M18 6 L6 18"],
  panel: ["M4 5 H20 V19 H4 Z", "M15 5 V19"],
  check: ["M5 12.5 L10 17 L19 7"],
  alert: ["M12 3 L22 20 H2 Z", "M12 10 V14", "M12 17 V17.5"],
  lock: ["M6 11 H18 V20 H6 Z", "M8.5 11 V8 A3.5 3.5 0 0 1 15.5 8 V11"],
  clip: ["M15.5 7 L8.5 14 A2.1 2.1 0 0 0 11.5 17 L18.5 10 A3.5 3.5 0 0 0 13.5 5 L6.5 12 A4.9 4.9 0 0 0 13.5 19 L19 13.5"],
  book: ["M5 4 H11 A2 2 0 0 1 13 6 V20 A2 2 0 0 0 11 18 H5 Z", "M19 4 H13 V20 A2 2 0 0 1 15 18 H19 Z"],
  sliders: ["M4 7 H14", "M18 7 H20", "M16 5 V9", "M4 17 H8", "M12 17 H20", "M10 15 V19"],
  chats: ["M4 6 H20", "M4 12 H20", "M4 18 H14"],
  sidebar: ["M4 5 H20 V19 H4 Z", "M9 5 V19"],
  folder: ["M3 7 V18 H21 V8 H11 L9 6 H3 Z"],
  share: ["M12 15 V4", "M8 8 L12 4 L16 8", "M5 13 V20 H19 V13"],
  note: ["M6 3 H18 V21 H6 Z", "M9 8 H15", "M9 12 H15", "M9 16 H13"],
  back: ["M15 6 L9 12 L15 18"],
  edit: ["M5 19 L6 14 L15.5 4.5 L19.5 8.5 L10 18 Z", "M13.5 6.5 L17.5 10.5"],
};

// icon returns an <svg> element for the named icon. It is hidden from
// screen readers: the button or text next to it carries the meaning.
export function icon(name, size = 16) {
  const svg = document.createElementNS(SVG, "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("width", String(size));
  svg.setAttribute("height", String(size));
  svg.setAttribute("aria-hidden", "true");
  svg.setAttribute("focusable", "false");
  svg.classList.add("icon");
  for (const d of PATHS[name] || []) {
    const path = document.createElementNS(SVG, "path");
    path.setAttribute("d", d);
    svg.append(path);
  }
  return svg;
}
