# SAL

The Semantic Accessibility Layer cli, abbreviated SAL, provides a build tool for creating, validating, and deploying RDF data products.

This repository is currently a work in progress.

- Documentation can be found at: https://cgs-earth.github.io/sal/
- An AI generated wiki of the project can be found at: https://deepwiki.com/cgs-earth/sal 
[![Ask DeepWiki](https://deepwiki.com/badge.svg)](https://deepwiki.com/cgs-earth/sal)
- A test deployment can be found at: https://sal-demo-779026943077.us-central1.run.app/

## Installation

To install the latest release of SAL, or update the one you have, run:

```sh
curl -fsSL https://raw.githubusercontent.com/cgs-earth/sal/main/install.sh | sh
```

This downloads or updates the prebuilt binary for your machine (Linux amd64 or macOS Apple Silicon) from the [latest release](https://github.com/cgs-earth/sal/releases/latest), verifies its checksum, and puts it in `~/.local/bin`.

To build from source instead, run `go install github.com/cgs-earth/sal@latest`. Note this needs cgo and a C toolchain, since DuckDB is linked in, and the resulting binary has no web UI for `sal serve --with-ui`, since the UI is built separately from its sources.
