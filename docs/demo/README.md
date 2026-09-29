# The README demo

The screenshot and terminal recording in the README come from a Meru that reads
only the invented notes in `notes/`, so they show no one's real files. To make
them again, run a second `merud` with its own home and socket, beside the one you
use:

```sh
mkdir -p /tmp/meru-demo
sed "s|NOTES|$PWD/docs/demo/notes|" docs/demo/config.toml > /tmp/meru-demo/config.toml
merud -config /tmp/meru-demo/config.toml &
meru -socket /tmp/meru-demo/merud.sock "when do I sow the tomatoes?"
```

It uses the models your Ollama already holds, so it adds no model memory.
