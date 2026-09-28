// The rail's right-click menus and the dialogs they open: delete a chat,
// move it to a folder, tag it, and make, rename or delete a folder. Every
// change goes to merud through the Bridge; the page only redraws. The
// dialog is one <dialog> element in the page, never window.confirm, so it
// looks like the app's other cards and a stray Enter lands on Cancel.

import { bridge, errorText } from "./api.js";
import { el, button } from "./turns.js";

// ctx is what app.js hands setupOrganize: state (the page's state),
// notice(text), reload() (a promise that loads the chat list and the
// folders again), chatDeleted(id) and focus() (the question box).
let ctx = null;

// setupOrganize wires the menu and the dialog to the page.
export function setupOrganize(context) {
  ctx = context;
  const menu = $("context-menu");
  document.addEventListener("click", (e) => {
    if (!menu.hidden && !menu.contains(e.target)) closeMenu();
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && !menu.hidden) closeMenu();
  });
  menu.addEventListener("keydown", menuKeys);
}

// $ finds the one element with this id.
const $ = (id) => document.getElementById(id);

// ---- The menu ----

// opener is the element the open menu came from, so focus goes back to it.
let opener = null;

// openMenu shows items, each { label, onClick }, at the pointer, or under
// the element that has focus when the keyboard opened the menu.
function openMenu(e, items) {
  e.preventDefault();
  closeMenu();
  const menu = $("context-menu");
  opener = e.currentTarget || document.activeElement;
  for (const item of items) {
    const b = button(item.label, { className: "menu-item" });
    b.setAttribute("role", "menuitem");
    b.tabIndex = -1;
    b.addEventListener("click", () => {
      closeMenu();
      item.onClick();
    });
    menu.append(b);
  }
  menu.hidden = false;
  // A menu opened from the keyboard has no pointer position; it goes
  // under the row instead.
  let x = e.clientX;
  let y = e.clientY;
  if (!x && !y && opener && opener.getBoundingClientRect) {
    const r = opener.getBoundingClientRect();
    x = r.left + 12;
    y = r.bottom;
  }
  // Keep the menu inside the window.
  const w = menu.offsetWidth;
  const h = menu.offsetHeight;
  menu.style.left = Math.min(x, window.innerWidth - w - 8) + "px";
  menu.style.top = Math.min(y, window.innerHeight - h - 8) + "px";
  menu.firstChild.focus();
}

// closeMenu hides the menu and gives focus back to where it came from.
function closeMenu() {
  const menu = $("context-menu");
  if (menu.hidden) return;
  menu.hidden = true;
  menu.replaceChildren();
  if (opener && opener.focus) opener.focus();
  opener = null;
}

// menuKeys moves through the menu with the arrow keys, as a menu does.
function menuKeys(e) {
  const items = [...$("context-menu").querySelectorAll(".menu-item")];
  const i = items.indexOf(document.activeElement);
  const step = e.key === "ArrowDown" ? 1 : e.key === "ArrowUp" ? -1 : 0;
  if (!step) return;
  e.preventDefault();
  items[(i + step + items.length) % items.length].focus();
}

// menuKey reports whether a key press should open a row's menu: the
// context-menu key, or Shift+F10, as on any desktop.
export function menuKey(e) {
  return e.key === "ContextMenu" || (e.shiftKey && e.key === "F10");
}

// chatMenu opens the menu for chat s: Move to folder, Tags and Delete.
export function chatMenu(e, s) {
  const items = [
    { label: s.folder ? "Move to another folder…" : "Move to folder…", onClick: () => moveDialog(s) },
  ];
  if (s.folder) items.push({ label: "Remove from " + s.folder, onClick: () => move(s, "") });
  items.push({ label: "Tags…", onClick: () => tagsDialog(s) });
  items.push({ label: "Delete…", onClick: () => deleteDialog(s) });
  openMenu(e, items);
}

// folderMenu opens the menu for the folder name: Rename and Delete.
export function folderMenu(e, name) {
  openMenu(e, [
    { label: "Rename…", onClick: () => renameDialog(name) },
    { label: "Delete folder…", onClick: () => removeFolderDialog(name) },
  ]);
}

// ---- The dialog ----

// dialog fills the <dialog> with a title, a body and actions, and shows
// it. Each action is { label, className, onClick }; the first one named
// Cancel takes focus, so Enter cancels. A form in the body submits with
// the action marked primary. The dialog closes on Esc as any <dialog>
// does.
function dialog(title, body, actions) {
  const d = $("dialog");
  d.replaceChildren();
  const head = el("h2", "dialog-title", title);
  head.id = "dialog-title";
  d.append(head, ...body);
  const bar = el("div", "dialog-actions");
  let cancel = null;
  for (const a of actions) {
    const b = button(a.label, { className: a.className || "button secondary", onClick: a.onClick });
    if (a.label === "Cancel") cancel = b;
    bar.append(b);
  }
  d.append(bar);
  if (!d.open) d.showModal();
  const input = d.querySelector("input");
  (input || cancel || bar.firstChild).focus();
}

// closeDialog closes the dialog and gives the question box its focus back.
function closeDialog() {
  const d = $("dialog");
  if (d.open) d.close();
  ctx.focus();
}

// cancel is the Cancel action every dialog has.
const cancel = { label: "Cancel", onClick: () => closeDialog() };

// field returns a labelled text input for a dialog, and runs submit when
// the user presses Enter in it.
function field(label, value, submit) {
  const id = "dialog-field";
  const wrap = el("div", "dialog-field");
  const l = el("label", "", label);
  l.htmlFor = id;
  const input = el("input");
  input.id = id;
  input.type = "text";
  input.value = value || "";
  input.autocomplete = "off";
  input.spellcheck = false;
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.isComposing) {
      e.preventDefault();
      submit(input.value);
    }
  });
  wrap.append(l, input);
  return { wrap, input };
}

// failed shows merud's refusal in the dialog, under its title, so the
// user can fix the name and try again.
function failed(err) {
  const d = $("dialog");
  let p = d.querySelector(".dialog-error");
  if (!p) {
    p = el("p", "dialog-error");
    p.setAttribute("role", "alert");
    d.querySelector(".dialog-title").after(p);
  }
  p.textContent = errorText(err);
}

// deleteDialog asks before chat s goes for good.
export function deleteDialog(s) {
  dialog("Delete this chat?", [
    el("p", "dialog-text", "“" + s.title + "”"),
    el("p", "dialog-note", "Meru deletes its transcript and everything search holds about it, so recall " +
      "won't bring it back. You can't undo this."),
  ], [
    cancel,
    {
      label: "Delete",
      className: "button danger",
      onClick: () => bridge.deleteSession(s.id).then(() => {
        closeDialog();
        ctx.chatDeleted(s.id);
        ctx.notice("Deleted the chat.");
        ctx.reload();
      }, failed),
    },
  ]);
}

// move puts chat s in folder, or in none for "".
function move(s, folder) {
  return bridge.moveSession(s.id, folder).then(() => {
    closeDialog();
    ctx.notice(folder ? "Moved the chat to " + folder + "." : "Took the chat out of " + s.folder + ".");
    ctx.reload();
  }, (err) => {
    if ($("dialog").open) failed(err);
    else ctx.notice(errorText(err));
  });
}

// moveDialog lists the folders to move chat s to, and takes the name of
// a new one.
function moveDialog(s) {
  const list = el("div", "dialog-choices");
  for (const f of ctx.state.folders) {
    if (f === s.folder) continue;
    list.append(button(f, { iconName: "folder", className: "button secondary", onClick: () => move(s, f) }));
  }
  if (!list.childElementCount) list.append(el("p", "dialog-note", "No other folder yet. Name one below."));
  const { wrap, input } = field("New folder", "", (v) => move(s, v));
  dialog("Move to folder", [list, wrap], [
    cancel,
    { label: "Create and move", className: "button primary", onClick: () => move(s, input.value) },
  ]);
}

// tagsDialog shows chat s's tags, each with a button that takes it off,
// and adds the ones typed. Tags are single words; merud makes them lower
// case.
function tagsDialog(s) {
  const tags = el("div", "dialog-tags");
  const draw = (list) => {
    tags.replaceChildren();
    if (!list.length) tags.append(el("p", "dialog-note", "No tags yet."));
    for (const t of list) {
      const chip = el("span", "tag-chip", "#" + t);
      chip.append(button("", {
        className: "icon-button small",
        iconName: "close",
        ariaLabel: "Remove the tag " + t,
        onClick: () => change([], [t]),
      }));
      tags.append(chip);
    }
  };
  // change sends the tags to add and remove, then redraws the chips from
  // the list merud answers.
  const change = (add, remove) => bridge.tagSession(s.id, add, remove).then(() => ctx.reload().then(() => {
    const now = ctx.state.sessions.find((x) => x.id === s.id);
    draw((now && now.tags) || []);
    input.value = "";
    input.focus();
  }), failed);
  const words = (v) => v.split(/[\s,]+/).filter(Boolean);
  const { wrap, input } = field("Add tags, one word each", "", (v) => words(v).length && change(words(v), []));
  draw(s.tags || []);
  dialog("Tags", [
    el("p", "dialog-note", "Tags show on the chat's row, and a search for one finds the chat, in the rail and when Meru recalls earlier chats."),
    tags,
    wrap,
  ], [
    { label: "Done", className: "button secondary", onClick: () => closeDialog() },
    { label: "Add", className: "button primary", onClick: () => words(input.value).length && change(words(input.value), []) },
  ]);
}

// newFolderDialog asks for a folder's name and makes it.
export function newFolderDialog() {
  const make = (v) => bridge.addChatFolder(v).then(() => {
    closeDialog();
    ctx.notice("Made the folder " + v.trim() + ". Right-click a chat to move it in.");
    ctx.reload();
  }, failed);
  const { wrap, input } = field("Name", "", make);
  dialog("New folder", [wrap], [cancel, { label: "Create", className: "button primary", onClick: () => make(input.value) }]);
}

// renameDialog asks for the folder name's new name.
function renameDialog(name) {
  const rename = (v) => bridge.renameChatFolder(name, v).then(() => {
    closeDialog();
    ctx.notice("Renamed " + name + " to " + v.trim() + ".");
    ctx.reload();
  }, failed);
  const { wrap, input } = field("New name", name, rename);
  dialog("Rename folder", [wrap], [cancel, { label: "Rename", className: "button primary", onClick: () => rename(input.value) }]);
  input.select();
}

// removeFolderDialog asks before the folder name goes. Its chats stay.
function removeFolderDialog(name) {
  const n = ctx.state.sessions.filter((s) => s.folder === name).length;
  const chats = n === 1 ? "Its 1 chat goes" : "Its " + n + " chats go";
  dialog("Delete the folder " + name + "?", [
    el("p", "dialog-note", (n ? chats + " back to the main list. " : "") + "Meru deletes no chat."),
  ], [
    cancel,
    {
      label: "Delete folder",
      className: "button danger",
      onClick: () => bridge.removeChatFolder(name).then(() => {
        closeDialog();
        ctx.notice("Deleted the folder " + name + ".");
        ctx.reload();
      }, failed),
    },
  ]);
}
