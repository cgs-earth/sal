#!/usr/bin/env bash
#
# Copyright 2025 Lincoln Institute of Land Policy
# SPDX-License-Identifier: MIT
#
# Entrypoint for the demo image, the one built with `--build-arg DEMO=true`.
#
# `sal serve` needs a built Iceberg table to serve, and a deployment target like
# Cloud Run starts with nothing on disk, so the sample data is built here on
# startup and the requested subcommand then runs inside that project. Building it
# at start rather than baking a table into the image also keeps the table's
# recorded warehouse path in sync with wherever the demo project lives at runtime.
#
# Environment:
#   SAL_DEMO_DATA    0 to skip the sample data and run sal in the current directory
#   SAL_DEMO_DIR     where the demo project is created (default /app/demo)
#   SAL_DEMO_SOURCE  RDF copied into that project (default /app/demo-data)
#   SAL_DEMO_REMOTE  git remote sal derives the base IRI from
set -euo pipefail

# sal init and the vocabulary cache both write under $HOME, and the DuckDB
# extensions the image bakes in live in /root/.duckdb.
export HOME="${HOME:-/root}"

DEMO_DIR="${SAL_DEMO_DIR:-/app/demo}"
DEMO_SOURCE="${SAL_DEMO_SOURCE:-/app/demo-data}"
# The remote is what sal turns into the base IRI for relative subjects, so it is
# the prefix on every subject in the sample data.
DEMO_REMOTE="${SAL_DEMO_REMOTE:-https://github.com/cgs-earth/sal-demo.git}"
DEMO_DATA="${SAL_DEMO_DATA:-1}"

log() { printf '==> %s\n' "$*" >&2; }

# Recreates the demo project from the RDF baked into the image and builds it into
# an Iceberg table that `sal serve` can read.
build_demo_data() {
	log "building sample data from $DEMO_SOURCE into $DEMO_DIR"
	rm -rf "$DEMO_DIR"
	mkdir -p "$DEMO_DIR/data"
	cp -R "$DEMO_SOURCE/." "$DEMO_DIR/data/"

	cd "$DEMO_DIR"
	# sal requires a git repository with a remote: init refuses to run without
	# one, and build reads the remote to derive the project's base IRI.
	git init -q -b main
	git remote add origin "$DEMO_REMOTE"
	git config user.email "demo@cgs-earth.org"
	git config user.name "SAL Demo"

	/app/sal init
	# An import the demo can show off: it puts an owl:imports in .sal/ontology.ttl
	# for the stats view, and build merges the OWL vocabulary into the table. The
	# ontology is fetched at build time, so a container with no route to w3.org
	# fails here rather than serving a table without it.
	/app/sal import "https://www.w3.org/2002/07/owl#"
	git add -A
	# build refuses to snapshot a tree with uncommitted changes.
	git commit -qm "Add sample RDF data"
	/app/sal build data/

	# A second build on top of the first, so the demo has more than one snapshot
	# and the table shows what an edit to an existing triple looks like: the old
	# name triple stays in the history and the new one is added on top.
	log "renaming Example Organization 001 and rebuilding for a second snapshot"
	sed -i 's/schema:name "Example Organization 001"/schema:name "Test Change"/' data/large.ttl
	git add -A
	git commit -qm "Rename Example Organization 001 to Test Change"
	/app/sal build data/
}

case "$DEMO_DATA" in
0 | false | no)
	log "SAL_DEMO_DATA=$DEMO_DATA, running sal in $(pwd) instead of the demo project"
	exec /app/sal "$@"
	;;
esac

# `sal serve` listens on 8080 and takes no port flag, so a platform that assigns
# a different port cannot reach it.
if [ -n "${PORT:-}" ] && [ "$PORT" != "8080" ]; then
	log "warning: PORT=$PORT but sal serve only listens on 8080"
fi

if [ -d "$DEMO_DIR/.sal/data" ] && [ -n "$(ls -A "$DEMO_DIR/.sal/data" 2>/dev/null)" ]; then
	log "reusing the built data already in $DEMO_DIR/.sal/data"
else
	build_demo_data
fi

cd "$DEMO_DIR"
exec /app/sal "$@"
