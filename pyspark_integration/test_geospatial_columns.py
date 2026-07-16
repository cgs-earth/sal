import os
import subprocess
from pathlib import Path

import pytest
from shapely import wkb
from pyspark.sql import SparkSession


@pytest.fixture(scope="module")
def repo_root():
    return Path(__file__).resolve().parents[1]


@pytest.fixture(scope="module")
def warehouse(repo_root):
    return repo_root / ".sal" / "data"


@pytest.fixture(scope="module")
def built_table(repo_root, warehouse):
    subprocess.run(
        [
            "go",
            "run",
            ".",
            "build",
            "--typed",
            "--force",
            "build/testdata/correct/geo.ttl",
        ],
        cwd=repo_root,
        check=True,
    )
    return latest_triples_table(warehouse)


@pytest.fixture(scope="module")
def spark():
    os.environ.setdefault("PYSPARK_SUBMIT_ARGS", "pyspark-shell")
    session = (
        SparkSession.builder.appName("sal-geospatial-columns")
        .master("local[1]")
        .config("spark.ui.enabled", "false")
        .config("spark.sql.shuffle.partitions", "1")
        .getOrCreate()
    )
    yield session
    session.stop()


def latest_triples_table(warehouse):
    tables = [
        path.parent.parent for path in warehouse.glob("*/triples/metadata/version-hint.text")
    ]
    if not tables:
        raise AssertionError(f"no Iceberg triples table found under {warehouse}")
    return max(tables, key=lambda path: path.stat().st_mtime)


def test_build_writes_queryable_geospatial_objects(spark, built_table):
    triples = spark.read.parquet(str(built_table / "data"))
    assert "object_geometry" in triples.columns

    triples.createOrReplaceTempView("triples")
    rows = spark.sql(
        """
        SELECT subject, predicate, object_geometry
        FROM triples
        WHERE object_geometry IS NOT NULL
        LIMIT 5
        """
    ).collect()

    print("First 5 object_geometry values as WKT:")
    for geometry in [row.object_geometry for row in rows]:
        print(wkb.loads(bytes(geometry)).wkt)

    assert len(rows) > 0
    assert any(
        row.predicate == "http://www.opengis.net/ont/geosparql#asWKT" for row in rows
    )
