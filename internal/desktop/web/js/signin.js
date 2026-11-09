// The sign-in to an MCP server with auth = "oauth", which the Settings
// card's Sign in button and the chat's sign-in box share. The Bridge asks
// merud to start it and opens the server's sign-in page in the browser;
// merud waits for the browser and connects the server once the user
// approves. This file then asks for the connections every two seconds
// until the server connects, or the sign-in ends without it.

import { bridge, errorText } from "./api.js";

// POLL_MS is how often the page asks whether the server connected.
const POLL_MS = 2000;
// WAIT_MS is how long the page watches: merud gives up on a sign-in after
// ten minutes too.
const WAIT_MS = 10 * 60 * 1000;

// signIn starts the sign-in for server name. report(state, words) hears
// how it goes: "waiting" while the browser is open, then "done" or
// "failed", each with one sentence for the page.
export function signIn(name, report) {
  report("waiting", "Waiting for you to sign in to " + name + " in your browser…");
  bridge.signIn(name).then(
    () => watch(name, Date.now() + WAIT_MS, report),
    (err) => report("failed", errorText(err)),
  );
}

// watch asks for the connections after POLL_MS and reports "done" once
// server name connects. A server that again waits for a sign-in, with no
// sign-in page open, means the sign-in ended without it, as when the user
// said no in the browser. A merud that doesn't answer gets asked again
// until the time runs out.
function watch(name, until, report) {
  setTimeout(() => {
    const again = () => {
      if (Date.now() < until) watch(name, until, report);
      else report("failed", "The sign-in to " + name + " took too long. Try again.");
    };
    bridge.connections().then((cv) => {
      const c = cv.connections.find((x) => x.kind === "mcp" && x.name === name);
      if (c && c.state === "connected") {
        report("done", "Signed in to " + name + ". Ask again to use its tools.");
      } else if (!c || (c.sign_in && !c.link)) {
        report("failed", "The sign-in to " + name + " didn't finish. Try again.");
      } else {
        again();
      }
    }, again);
  }, POLL_MS);
}
