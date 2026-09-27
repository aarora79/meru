#!/bin/sh
# render.sh draws each figure in the level 100 and 200 HTML pages to a PNG in
# this folder, so 100.md and 200.md show the same pictures as the pages.
# Run it from anywhere with `make figures` after you change a figure's SVG.
# It needs only Google Chrome; set CHROME to its path if it lives elsewhere.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
pages=$(dirname "$here")
chrome=${CHROME:-"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# The figure draws at 760 CSS pixels inside a 20-pixel white margin, the
# width it has on the page, and Chrome doubles the pixels for sharp text.
width=800
inner=760

# render PAGE FIGURE-ID OUT writes the SVG inside <figure id="FIGURE-ID"> in
# PAGE, with PAGE's styles in light mode, to img/OUT.
render() {
	page="$pages/$1"
	svg="$work/$2.svg"
	awk -v id="$2" '
		index($0, "<figure id=\"" id "\"") { in_fig = 1 }
		in_fig && /<svg/ { in_svg = 1 }
		in_svg { print }
		in_svg && /<\/svg>/ { exit }
	' "$page" >"$svg"

	# The viewBox gives the drawing's shape, and so the height of the shot.
	box=$(sed -n 's/.*viewBox="0 0 \([0-9]*\) \([0-9]*\)".*/\1 \2/p' "$svg" | head -n 1)
	height=$(echo "$box" | awk -v w="$inner" -v m=40 '{ h = w * $2 / $1; printf "%d", (h == int(h) ? h : int(h) + 1) + m }')

	html="$work/$2.html"
	{
		echo '<!DOCTYPE html><html data-theme="light"><head><meta charset="utf-8">'
		sed -n '/<style>/,/<\/style>/p' "$page"
		echo "<style>html, body { margin: 0; background: #fff; }"
		echo ".shot { padding: 20px; } .shot svg { width: ${inner}px; height: auto; display: block; }</style>"
		echo '</head><body><div class="shot">'
		cat "$svg"
		echo '</div></body></html>'
	} >"$html"

	"$chrome" --headless=new --disable-gpu --hide-scrollbars \
		--force-device-scale-factor=2 --window-size="$width,$height" \
		--screenshot="$here/$3" "file://$html" 2>/dev/null
	echo "wrote img/$3"
}

render 100.html fig-parts 100-parts.png
render 100.html fig-ask 100-ask.png
render 200.html fig-overview 200-overview.png
render 200.html fig-turns 200-turns.png
render 200.html fig-storage 200-storage.png
render 200.html fig-search 200-search.png
render 200.html fig-dispatch 200-dispatch.png
