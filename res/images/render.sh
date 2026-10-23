#!/bin/sh
# Renders every res/images/*.mmd to the .svg beside it, with mermaid-cli.
#
# Run by hand after changing a diagram, on a machine with Node and a
# Chromium; commit the .mmd and the .svg together. The README shows the SVG
# rather than a mermaid block so the picture is the same on every viewer.
#
#   CHROMIUM=/path/to/chrome res/images/render.sh
set -eu
cd "$(dirname "$0")"
config=$(mktemp)
trap 'rm -f "$config"' EXIT
if [ -n "${CHROMIUM:-}" ]; then
	printf '{"executablePath":"%s","args":["--no-sandbox"]}\n' "$CHROMIUM" >"$config"
else
	printf '{"args":["--no-sandbox"]}\n' >"$config"
fi
for src in *.mmd; do
	npx -y @mermaid-js/mermaid-cli@11.4.2 -p "$config" -b transparent \
		-i "$src" -o "${src%.mmd}.svg"
done
