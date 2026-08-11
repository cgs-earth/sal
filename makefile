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

ui_dev:
	@trap 'kill 0' EXIT INT TERM; (cd serve/sal-ui && npm run dev) & go run . serve --with-ui

init_sandbox:
	sbx kit add claude-sal-cli ./sal-sbx-dev-kit/