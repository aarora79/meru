Install Meru
============

Meru is a personal AI assistant that runs on your own Mac. The models run
here, and your questions and files stay here.

To install it, open "Install Meru.app" from this disk image. It walks you
through nine steps and says what each one does before it does it.

The first time: right-click, then Open
--------------------------------------

Meru isn't signed by Apple: it is a personal project with no Apple
developer account. So the first time, macOS won't open the installer with
a double-click. Instead:

  1. Right-click (or Control-click) "Install Meru.app".
  2. Choose Open.
  3. macOS asks whether you're sure. Click Open.

If macOS offers only "Move to Trash" or "Done", click Done, then open
System Settings, Privacy & Security, scroll to the line about
"Install Meru.app", and click Open Anyway.

You do this once. The installer's second step offers to clear the same
warning for Meru.app and merud, so they open like any other app.

What the installer needs
------------------------

  - A Mac with Apple silicon and 16 GB of memory or more.
  - About 5 GB of free disk for the smallest models, more for larger ones.
  - For web search: Docker Desktop, OrbStack or colima. You can skip web
    search and add it later by running the installer again.

Your settings end up in one file, ~/.meru/config.toml. The last screen
shows it and opens it for you.

Meru is free and open source under the Apache License 2.0:
https://github.com/aarora79/meru
