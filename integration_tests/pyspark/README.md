# PySpark Integration Tests

This directory contains PySpark integration checks for SAL's generated Iceberg
data files under the project-local `.sal/data` directory.

## Setup

Install dependencies with `uv` from this directory:

```sh
uv sync
```

The test expects to run from a SAL git project that already has a `.sal`
directory. It invokes SAL through:

```sh
go run . build --typed --force build/testdata/correct/geo.ttl
```

The `--typed` flag is required because the geospatial schema writes
`object_geometry`.

## Run

From this directory:

```sh
uv run pytest -s
```

Use `-s` so pytest does not capture stdout. The geospatial test uses Shapely to
convert WKB values to WKT and prints the first five non-null `object_geometry`
values from a Spark SQL query shaped like:

```sql
SELECT subject, predicate, object_geometry
FROM triples
WHERE object_geometry IS NOT NULL
```

## Notes

The test reads the generated Parquet data files from the discovered
`.sal/data/*/triples/data` path with PySpark and registers them as a temporary
`triples` view before running the SQL check.
