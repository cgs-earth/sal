#!/usr/bin/env bash
#
# Records the demo GIFs embedded in docs/src/content/docs/reference/commands.mdx.
#
#   ./docs/demos/generate.sh            record every tape
#   ./docs/demos/generate.sh build      record docs/demos/build.tape only
#
# Requires vhs, ttyd, ffmpeg, and duckdb on PATH. `sal` is built from the working
# tree into docs/demos/.bin so the GIFs always show the current code. Output lands in
# docs/public/demos, which Astro serves at /sal/demos/<name>.gif.
set -euo pipefail

DEMOS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$DEMOS_DIR/../.." && pwd)"
BIN_DIR="$DEMOS_DIR/.bin"
OUT_DIR="$REPO_ROOT/docs/public/demos"

# Each tape records one command against one starting state; see scenario.sh.
# Written as a case rather than an associative array so this runs under the bash 3.2
# that ships with macOS.
state_for() {
	case "$1" in
	help | init) echo fresh ;;
	validate) echo initialized ;;
	build) echo committed ;;
	query) echo built ;;
	*)
		echo "generate.sh: no tape named '$1'" >&2
		return 1
		;;
	esac
}

TAPES="help init validate build query"
if [ "$#" -gt 0 ]; then
	TAPES="$*"
fi

for name in $TAPES; do
	state_for "$name" >/dev/null
done

mkdir -p "$BIN_DIR" "$OUT_DIR"
go build -o "$BIN_DIR/sal" "$REPO_ROOT"
export PATH="$BIN_DIR:$PATH"

# Tapes reference their Output and Source paths relative to the repository root.
cd "$REPO_ROOT"

for name in $TAPES; do
	state="$(state_for "$name")"
	echo "==> recording $name ($state)"
	"$DEMOS_DIR/scenario.sh" "$state"
	vhs "docs/demos/$name.tape"
done

echo "==> wrote:"
ls -lh "$OUT_DIR"
