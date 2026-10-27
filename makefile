install:
	go build -o ~/.local/bin

# Rebuilds the React UI that `sal serve --with-ui` serves. serve/ui.go embeds
# serve/sal-ui/dist, so the output is committed; rerun this after editing the UI.
ui:
	cd serve/sal-ui && npm ci && npm run build

# Records the demo GIFs embedded in the command reference into docs/public/demos.
# CI does this on every push, so this is only needed to preview a tape locally.
# Needs vhs, ttyd, and ffmpeg on PATH.
.PHONY: demos
demos:
	./docs/demos/generate.sh

deadcode:
	deadcode ./...

# Builds and runs a real SAL module container, so it needs a running docker daemon
integration_test_salmodule:
	go test -tags integration -v -timeout 15m ./integration_tests/salmodule/...

# This is moreso intended to be ran in CI since it will write to the sal data dir in the project
# TODO in the future explore making this write to a tmp dir
integration_test_pyspark:
	cd integration_tests/pyspark/ && uv sync --locked && uv run pytest -s

# Hot reload for working on serve: Vite serves the UI from serve/sal-ui/src
# with hot module replacement on http://localhost:5173, proxying the API to
# `sal serve --with-ui` on 8080, which air (.air.toml) rebuilds and restarts on
# every Go change. Both stop together on Ctrl-C. SAL_DEV_PROJECT=path serves
# another project's table instead of this repository's.
.PHONY: dev
dev:
	@trap 'kill 0' EXIT INT TERM; \
	(cd serve/sal-ui && npm run dev) & \
	(sleep 6; printf '\n>>> Open http://localhost:5173 for the UI with hot reload. Port 8080 serves the committed dist, not your edits.\n\n') & \
	go run github.com/air-verse/air@latest

init_sandbox:
	sbx kit add claude-sal-cli ./sal-sbx-dev-kit/