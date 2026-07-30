install:
	go build -o ~/.local/bin

deadcode:
	deadcode ./...

# Builds and runs a real SAL module container, so it needs a running docker daemon
integration_test_salmodule:
	go test -tags integration -v -timeout 15m ./integration_tests/salmodule/...

integration_test_pyspark:
	cd integration_tests/pyspark/ && uv sync --locked && uv run pytest -s