// Package commands runs the local programs the user declares in
// config.toml's [[commands]] entries. Each entry becomes one tool the model
// sees, named "cmd.<name>", with a description and an argument schema built
// from the entry's parameters. See ARCHITECTURE.md, "Local commands".
//
// The model never writes a command. It picks a declared one and fills in
// its parameters, and Render puts each value into the argv that config
// declares. A value becomes part of one argv element and never more, and
// Run starts the program directly with no shell, so a value can't add a
// flag of its own, chain a second program or reach a shell. Each type
// checks its values: a path must resolve, symbolic links followed, inside
// the folder its parameter names.
//
// New checks every entry when merud starts and refuses a bad one, naming
// it: a duplicate name, an empty argv, a shell or script interpreter as the
// program, a placeholder with no parameter, a parameter no placeholder
// uses, a path parameter with no folder or one that doesn't exist, and a
// timeout over five minutes.
//
// Run gives the program a short environment (PATH, HOME, LANG and the
// names the entry allows), a timeout that kills its whole process group,
// and keeps at most 1 MiB of each output stream. A program that exits with
// a non-zero code is a result the model reads, not a failure.
//
// Set is the dispatch.Backend for the commands, so every run goes through
// dispatch like any other tool (AGENTS.md, non-negotiable 4). It is also a
// dispatch.Auditor: the tool_call line, the approval prompt and the
// tool_calls row show the argv that runs, not only the model's arguments.
//
// What it doesn't do: it has no general "run any program" tool and never
// will; half the Unix toolbox can run a shell through its flags, so an
// allowlist of program names can't make that safe. It holds no model or
// store logic, and it doesn't sandbox the program, which runs as the user.
package commands
