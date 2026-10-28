// The name of the computer merud runs on, as the page words it: "this Mac"
// on macOS and "this computer" elsewhere. The Bridge sends it in merud's
// status (Status.Machine), and internal/platform picks it, so every screen
// says the same thing on every system. Until the first status arrives the
// page says "this computer", which is true everywhere.

let name = "this computer";

// setMachine keeps the name from a status; an empty one changes nothing.
export function setMachine(n) {
  if (n) name = n;
}

// here returns the name for the middle of a sentence: "this Mac".
export function here() {
  return name;
}
