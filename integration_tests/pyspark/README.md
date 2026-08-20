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
go run . build --force build/testdata/correct/geo.ttl
```

## Run

From this directory:

```sh
uv run pytest -s
```

Use `-s` so pytest does not capture stdout. SAL writes `object_geometry` with
the Parquet `GEOMETRY` logical type, which Spark 4.1+ reads as its native
`GEOMETRY` column type, so the test asserts the column arrives as a Spark
`GeometryType` with the default `OGC:CRS84` SRID rather than as raw binary. It
renders the first five non-null values through Spark's `ST_AsBinary`, decodes
that WKB to WKT with Shapely, and prints them, from a Spark SQL query shaped
like:

```sql
SELECT subject, predicate, object_geometry,
       ST_SRID(object_geometry), ST_AsBinary(object_geometry)
FROM triples
WHERE object_geometry IS NOT NULL
```

## Notes

The test reads the generated Parquet data files from the discovered
`.sal/data/*/triples/data` path with PySpark and registers them as a temporary
`triples` view before running the SQL check.
