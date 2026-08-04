#!/usr/bin/env bash
#
# Rebuilds ~/sal-demo into the starting state a tape expects. Each tape records a
# single command, so the state before that command has to be created here rather
# than on screen. Run by generate.sh before every tape.
#
# States:
#   fresh       a git repo with a remote and RDF sources, but no `sal init` yet
#   initialized fresh + `sal init`
#   committed   initialized + every file committed, so `sal build` sees a clean tree
#   built       committed + `sal build data/`
set -euo pipefail

STATE="${1:?usage: scenario.sh <fresh|initialized|committed|built>}"
case "$STATE" in
fresh | initialized | committed | built) ;;
*)
	echo "scenario.sh: unknown state '$STATE'" >&2
	exit 1
	;;
esac

DEMO_DIR="${SAL_DEMO_DIR:-$HOME/sal-demo}"
FIXTURES="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/fixtures"

# The demo project is disposable and recreated for every tape so that a GIF never
# depends on what an earlier GIF left behind.
rm -rf "$DEMO_DIR"
mkdir -p "$DEMO_DIR"
cp -R "$FIXTURES/." "$DEMO_DIR/"

cd "$DEMO_DIR"
git init -q -b main
# The remote is what SAL turns into the base IRI for relative subjects, so it shows
# up in `sal query` output and needs to be short and recognizable.
git remote add origin https://github.com/cgs-earth/sal-demo.git
git config user.email "demo@cgs-earth.org"
git config user.name "SAL Demo"

if [ "$STATE" != "fresh" ]; then
	sal init
fi

if [ "$STATE" = "committed" ] || [ "$STATE" = "built" ]; then
	git add -A
	git commit -qm "Add station data"
fi

if [ "$STATE" = "built" ]; then
	sal build data/
fi
