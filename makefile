install:
	go build -o ~/.local/bin

# Rebuilds the React UI that `sal serve --with-ui` serves. serve/ui.go embeds
# serve/sal-ui/dist, so the output is committed; rerun this after editing the UI.
ui:
	cd serve/sal-ui && npm ci && npm run build

# Runs the UI against a `sal serve --with-ui` on port 8080 with hot reloading
ui_dev:
	cd serve/sal-ui && npm run dev

deadcode:
	deadcode ./...

# Builds and runs a real SAL module container, so it needs a running docker daemon
integration_test_salmodule:
	go test -tags integration -v -timeout 15m ./integration_tests/salmodule/...

# This is moreso intended to be ran in CI since it will write to the sal data dir in the project
# TODO in the future explore making this write to a tmp dir
integration_test_pyspark:
	cd integration_tests/pyspark/ && uv sync --locked && uv run pytest -s