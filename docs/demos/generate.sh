#!/usr/bin/env bash
#
# Records the demo GIFs embedded in docs/src/content/docs/reference/commands.mdx.
#
#   ./docs/demos/generate.sh            record every tape
#   ./docs/demos/generate.sh build      record docs/demos/build.tape only
#
# Requires vhs, ttyd, and ffmpeg on PATH. `sal` is built from the working
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

# CI builds `sal` once and hands the same binary to every tape job, so those runs set
# SAL_DEMO_SKIP_BUILD rather than rebuilding it five times. Local runs always build,
# so `make demos` can never record an out of date binary.
if [ "${SAL_DEMO_SKIP_BUILD:-}" = "1" ]; then
	if [ ! -x "$BIN_DIR/sal" ]; then
		echo "generate.sh: SAL_DEMO_SKIP_BUILD=1 but no executable at $BIN_DIR/sal" >&2
		exit 1
	fi
	echo "==> using prebuilt $BIN_DIR/sal"
else
	go build -o "$BIN_DIR/sal" "$REPO_ROOT"
fi
export PATH="$BIN_DIR:$PATH"

# Tapes reference their Output and Source paths relative to the repository root.
cd "$REPO_ROOT"

for name in $TAPES; do
	state="$(state_for "$name")"
	echo "==> recording $name ($state)"
	"$DEMOS_DIR/scenario.sh" "$state"
	# DuckDB is linked into sal, but it downloads its extensions on first use.
	# Left to itself that happens inside the recording, which both slows the tape
	# and leaves a dead pause in the GIF.
	if [ "$name" = "query" ]; then
		"$BIN_DIR/sal" duckdb-extensions >/dev/null
	fi
	vhs "docs/demos/$name.tape"
done

echo "==> wrote:"
ls -lh "$OUT_DIR"
