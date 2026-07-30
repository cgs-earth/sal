install:
	go build -o ~/.local/bin

deadcode:
	deadcode ./...

# Builds and runs a real SAL module container, so it needs a running docker daemon
integration_test_salmodule:
	go test -tags integration -v -timeout 15m ./integration_tests/salmodule/...

# This is moreso intended to be ran in CI since it will write to the sal data dir in the project
# TODO in the future explore making this write to a tmp dir
integration_test_pyspark:
	cd integration_tests/pyspark/ && uv sync --locked && uv run pytest -s