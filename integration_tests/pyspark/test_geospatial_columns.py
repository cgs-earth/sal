import os
import subprocess
from pathlib import Path

import pytest
from shapely import wkb
from pyspark.sql import SparkSession
from pyspark.sql.types import Geometry, GeometryType


@pytest.fixture(scope="module")
def repo_root():
    """The root of the SAL git repository."""
    root = Path(__file__).parent.parent.parent
    assert (root / ".git").exists(), f"{root} appears to be the wrong directory and not the root"
    return root


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

    # build writes object_geometry with the Parquet GEOMETRY logical type, so
    # Spark must read it as its native GEOMETRY type rather than as raw bytes.
    geometry_type = triples.schema["object_geometry"].dataType
    assert isinstance(geometry_type, GeometryType), geometry_type
    assert geometry_type.srid == GeometryType.DEFAULT_SRID

    triples.createOrReplaceTempView("triples")
    rows = spark.sql(
        """
        SELECT
            subject,
            predicate,
            object_geometry,
            ST_SRID(object_geometry) AS srid,
            ST_AsBinary(object_geometry) AS geometry_wkb
        FROM triples
        WHERE object_geometry IS NOT NULL
        LIMIT 5
        """
    ).collect()

    assert len(rows) > 0
    assert any(
        row.predicate == "http://www.opengis.net/ont/geosparql#asWKT" for row in rows
    )

    print("First 5 object_geometry values as WKT:")
    for row in rows:
        assert isinstance(row.object_geometry, Geometry), row.object_geometry
        assert row.srid == GeometryType.DEFAULT_SRID
        assert row.object_geometry.getSrid() == row.srid
        assert row.object_geometry.getBytes() == bytes(row.geometry_wkb)
        geometry = wkb.loads(bytes(row.geometry_wkb))
        assert not geometry.is_empty
        print(geometry.wkt)
