// The page's only way to reach Go. Each function calls one method of the
// Bridge (internal/desktop) by name through Wails' runtime, which the app
// serves at /wails/runtime.js. The page makes no other request.

import { Call, Events, Clipboard } from "/wails/runtime.js";

// Wails names a bound method by its Go package path, type and method.
const BRIDGE = "github.com/aarora79/meru/internal/desktop.Bridge.";

// call runs one Bridge method and returns a promise of its result. A Go
// error rejects the promise with the error's text as its message.
function call(method, ...args) {
  return Call.ByName(BRIDGE + method, ...args);
}

export const bridge = {
  // The chat (bridge.go, history.go, status.go).
  send: (session, question, scope) => call("Send", session, question, scope),
  retry: (session, question, scope, images) => call("Retry", session, question, scope, images),
  stop: () => call("Stop"),
  unqueue: (index) => call("Unqueue", index),
  approve: (id, choice) => call("Approve", id, choice),
  sessions: () => call("Sessions"),
  sessionTurns: (id) => call("SessionTurns", id),
  status: () => call("Status"),
  openURL: (url) => call("OpenURL", url),
  openSource: (path) => call("OpenSource", path),
  // Files (files.go).
  saveChat: (session) => call("SaveChat", session),
  saveNote: (session, text) => call("SaveNote", session, text),
  reveal: (path) => call("Reveal", path),
  chooseFolder: () => call("ChooseFolder"),
  attachFile: () => call("AttachFile"),
  detach: (index) => call("Detach", index),
  detachAll: () => call("DetachAll"),
  // Settings and Setup (settings.go).
  connections: () => call("Connections"),
  setPolicy: (kind, server, tool, policy) => call("SetPolicy", kind, server, tool, policy),
  addConnection: (name, secret, key) => call("AddConnection", name, secret, key),
  addCustomServer: (server) => call("AddCustomServer", server),
  removeConnection: (name) => call("RemoveConnection", name),
  setSecret: (name, value) => call("SetSecret", name, value),
  folders: () => call("Folders"),
  addFolder: (path) => call("AddFolder", path),
  removeFolder: (path) => call("RemoveFolder", path),
  memories: () => call("Memories"),
  addMemory: (kind, text) => call("AddMemory", kind, text),
  forgetMemory: (id) => call("ForgetMemory", id),
  skills: () => call("Skills"),
  setSkill: (name, on) => call("SetSkill", name, on),
  models: () => call("Models"),
  useModel: (name) => call("UseModel", name),
  useModelSet: (name, rebuild) => call("UseModelSet", name, rebuild),
  saveModels: () => call("SaveModels"),
  activity: () => call("Activity"),
  usage: () => call("Usage"),
  // The About section of Settings (about.go).
  about: () => call("About"),
  // Slash commands (commands.go).
  commands: () => call("Commands"),
  quit: () => call("Quit"),
};

// onUpdate calls fn with each Update the Bridge sends: turn starts, merud's
// events, approval cards, turn ends and queue changes.
export function onUpdate(fn) {
  Events.On("meru:update", (event) => fn(event.data));
}

// copyText puts text on the system clipboard through Wails, which works
// where the browser's own clipboard API is off.
export function copyText(text) {
  return Clipboard.SetText(text);
}

// errorText turns a rejected call into one line for the page.
export function errorText(err) {
  if (!err) return "Something went wrong.";
  return String(err.message || err);
}
