// The slash commands in the composer: the same six `meru chat` has, from
// the Bridge's list (internal/desktop/commands.go), so the two can't drift.
// Typing "/" at the start of the box opens a menu of them; a line that
// starts with "/" runs here and never reaches the model. Every command
// runs at once, even while a turn runs; /new stops that turn first, as it
// does in `meru chat`.

import { bridge, errorText } from "./api.js";
import { el } from "./turns.js";

// list holds the commands once the Bridge sends them.
let list = [];
// menu state: the options shown and the one the arrow keys point at.
const menu = { shown: [], active: -1 };
// The page's own actions, which setupCommands receives.
let act = null;
let box = null;
let listbox = null;

// setupCommands wires the menu to the question box. actions holds what
// the commands do in the page: newChat(), openLibrary(section), blocks()
// (every numbered code block in the chat, oldest first), newestBlocks()
// (the newest finished answer's, or null when none has finished),
// textOf(block), copy(text) (a promise), notice(text), askQuit() and
// fit().
export function setupCommands(question, menuList, actions) {
  box = question;
  listbox = menuList;
  act = actions;
  bridge.commands().then((cmds) => {
    list = cmds || [];
  });
  box.addEventListener("input", update);
  box.addEventListener("blur", () => setTimeout(close, 100));
}

// names writes the command list as the "unknown command" line does in
// `meru chat`: "/new, /usage, /me, /mcp, /copy, /exit".
function names() {
  return list.map((c) => c.name).join(", ");
}

// update shows the menu while the box holds "/" and a partial name, and
// hides it otherwise.
function update() {
  const v = box.value;
  if (!/^\/\S*$/.test(v)) {
    close();
    return;
  }
  menu.shown = list.filter((c) => c.name.startsWith(v));
  menu.active = menu.shown.length ? 0 : -1;
  draw();
}

// draw renders the menu as a listbox of options, with the active one
// marked for screen readers through aria-activedescendant.
function draw() {
  listbox.replaceChildren();
  if (menu.shown.length === 0) {
    close();
    return;
  }
  menu.shown.forEach((c, i) => {
    const li = el("li", "command-option" + (i === menu.active ? " active" : ""));
    li.id = "command-" + c.name.slice(1);
    li.setAttribute("role", "option");
    li.setAttribute("aria-selected", String(i === menu.active));
    li.append(el("span", "command-name", c.name + (c.args ? " " + c.args : "")), el("span", "command-desc", c.description));
    // mousedown, not click: a click would take focus from the box first.
    li.addEventListener("mousedown", (e) => {
      e.preventDefault();
      pick(c);
    });
    listbox.append(li);
  });
  listbox.hidden = false;
  box.setAttribute("aria-expanded", "true");
  if (menu.active >= 0) box.setAttribute("aria-activedescendant", "command-" + menu.shown[menu.active].name.slice(1));
}

// close hides the menu.
function close() {
  if (!listbox) return;
  listbox.hidden = true;
  menu.shown = [];
  menu.active = -1;
  box.setAttribute("aria-expanded", "false");
  box.removeAttribute("aria-activedescendant");
}

// menuKey handles a key in the box while the menu is open: the arrows
// move, Enter and Tab pick, Escape closes. It returns true when it used
// the key.
export function menuKey(e) {
  if (listbox.hidden || menu.shown.length === 0) return false;
  switch (e.key) {
    case "ArrowDown":
      menu.active = (menu.active + 1) % menu.shown.length;
      break;
    case "ArrowUp":
      menu.active = (menu.active - 1 + menu.shown.length) % menu.shown.length;
      break;
    case "Enter":
    case "Tab":
      if (e.shiftKey || e.isComposing) return false;
      pick(menu.shown[menu.active]);
      e.preventDefault();
      return true;
    case "Escape":
      close();
      e.preventDefault();
      return true;
    default:
      return false;
  }
  e.preventDefault();
  draw();
  return true;
}

// pick puts command c in the box: one that takes an argument waits for
// it, any other runs at once.
function pick(c) {
  close();
  if (c.args) {
    box.value = c.name + " ";
    act.fit();
    box.focus();
    return;
  }
  box.value = c.name;
  if (runCommand(box.value)) {
    box.value = "";
    act.fit();
  }
}

// runCommand runs text as a slash command and returns true when the box
// should empty. It returns false for an unknown command, which stays in
// the box so the user can fix a typo, as in `meru chat`.
export function runCommand(text) {
  const [name, ...rest] = text.trim().split(/\s+/);
  const arg = rest.join(" ");
  close();
  switch (name) {
    case "/new":
      act.newChat();
      return true;
    case "/usage":
      act.openLibrary("usage");
      return true;
    case "/me":
      act.openLibrary("you");
      return true;
    case "/mcp":
      act.openLibrary("connections");
      return true;
    case "/copy":
      copyCommand(arg);
      return true;
    case "/exit":
      act.askQuit();
      return true;
  }
  act.notice("unknown command " + name + " · commands: " + names());
  return false;
}

// copyCommand runs /copy: with a number it copies that block, alone the
// newest answer's last block. The notices match `meru chat`'s.
function copyCommand(arg) {
  const blocks = act.blocks();
  if (arg === "") {
    const newest = act.newestBlocks();
    if (newest === null) {
      act.notice("no code block to copy yet");
      return;
    }
    if (newest.length === 0) {
      act.notice("the newest answer has no code block" + (blocks.length ? " · /copy N copies an older one" : ""));
      return;
    }
    copyBlock(blocks.indexOf(newest[newest.length - 1]) + 1, blocks);
    return;
  }
  const n = Number(arg);
  if (!Number.isInteger(n)) {
    act.notice("/copy takes a block number, such as /copy 2");
    return;
  }
  copyBlock(n, blocks);
}

// copyBlock copies block n, or says why it can't.
function copyBlock(n, blocks) {
  if (blocks.length === 0) {
    act.notice("no code block to copy yet");
    return;
  }
  if (n < 1 || n > blocks.length) {
    act.notice("no code block " + n + " · blocks go from 1 to " + blocks.length);
    return;
  }
  const text = act.textOf(blocks[n - 1]);
  const lines = text.split("\n").length;
  act.copy(text).then(
    () => act.notice("copied block " + n + " (" + lines + (lines === 1 ? " line)" : " lines)")),
    (err) => act.notice("couldn't copy block " + n + ": " + errorText(err)),
  );
}
