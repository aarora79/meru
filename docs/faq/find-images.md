# How do I find an image?

Meru indexes the PNG, JPEG, GIF and WebP files in the folders it reads, with its
default search model, `embeddinggemma-2:440m`. Nothing to set up: the next scan
picks them up. `meru index -status` shows how far it has got.

**By what it shows.** Ask in words, in the desktop app, `meru chat` or `meru`:

```text
find the screenshot of the Obsidian settings
do I have a photo of the garden beds?
```

Image results show under the answer with a small preview in the desktop app,
and as `[1] ~/Documents/img/shot.png (image)` in the terminal. Click one, or
open the path, to see it.

**Read what an image says.** Ask about what's in a picture, such as "what did my
Amtrak tickets cost?" An answer model that can look at pictures, such as
`qwen3.6:35b` or `gemma4:26b-a4b-it-qat`, opens each picture it found with the
`view_image` tool and reads it; the desktop app shows a "Looked at amtrak.png"
step for each. One question opens at most eight pictures. MiniCPM5-2B reads text
only: it names the pictures, and a line under the answer names a model set of
yours that can read them, with the command to switch, such as
`/model qwen-4bit` in `meru chat` or Use for answers in Settings, Models. If your
`config.toml` lists `[builtin] tools`, add `"view_image"` to the list and
restart `merud`.

**Like a picture you have.** Attach it in the desktop app, with the attach button
or by dragging it in, or in `meru chat` with `/attach ~/Desktop/shot.png`, and ask:

```text
find images like this
```

From a terminal:

```sh
meru -image ~/Desktop/shot.png "find images like this"
```

Meru lists the images in your folders most like it, best first. With an answer
model that can look at pictures, the model sees your image and the list, and can
say which fit. With one that can't, such as MiniCPM5-2B, Meru answers with the
list itself.

**What stays out.** HEIC files, which iPhone photos and some Mac screenshots use:
Ollama can't read them. Convert one to PNG or JPEG to search it. Images over
20 MB, and any file the usual rules skip, such as hidden files and secrets, stay
out too.

**Turn it off.** Set `images = false` under `[index]` in `~/.meru/config.toml`
and restart `merud`. The next scan drops the images from the index.

More detail: [ARCHITECTURE.md, "Image search"](../../ARCHITECTURE.md#image-search).
