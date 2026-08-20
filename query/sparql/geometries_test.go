package sparql

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGeometrySQLForTypedObjectsUsesTypedObjectColumns(t *testing.T) {
	sql := geometrySQL(TypedObjects, GeometryQuery{Limit: 100, Offset: 12})

	require.Contains(t, sql, "COALESCE(triples.object_iri, CAST(triples.object_float AS VARCHAR), triples.object_string, ST_AsText(triples.object_geometry)) AS object")
	require.Contains(t, sql, "ST_AsGeoJSON(triples.object_geometry) AS geometry")
	require.Contains(t, sql, "WHERE triples.object_geometry IS NOT NULL")
	require.Contains(t, sql, "LIMIT 100")
	require.Contains(t, sql, "OFFSET 12")
	require.NotContains(t, sql, "ST_GeomFromWKB")
	require.NotContains(t, sql, "ST_Intersects")
}

func TestGeometrySQLForSimpleObjectsUsesObjectColumn(t *testing.T) {
	sql := geometrySQL(SimpleObjects, GeometryQuery{Limit: 50})

	require.Contains(t, sql, "\n\ttriples.object AS object,\n")
	require.Contains(t, sql, "ST_AsGeoJSON(triples.object_geometry) AS geometry")
	require.Contains(t, sql, "LIMIT 50")
	require.Contains(t, sql, "OFFSET 0")
	require.NotContains(t, sql, "object_iri")
}

func TestGeometrySQLFiltersByBoundingBox(t *testing.T) {
	box := BBox{MinX: -90.5, MinY: 40, MaxX: -89, MaxY: 41.25}
	sql := geometrySQL(TypedObjects, GeometryQuery{Limit: 10, BBox: &box})

	require.Contains(t, sql, "AND ST_Intersects(triples.object_geometry, ST_MakeEnvelope(-90.5, 40, -89, 41.25))")
}

func TestParseBBoxReadsFourCoordinates(t *testing.T) {
	box, err := ParseBBox(" -90.5, 40 ,-89,41.25")
	require.NoError(t, err)
	require.Equal(t, BBox{MinX: -90.5, MinY: 40, MaxX: -89, MaxY: 41.25}, box)
}

func TestParseBBoxRejectsMalformedInput(t *testing.T) {
	_, err := ParseBBox("-90,40,-89")
	require.ErrorContains(t, err, "minX,minY,maxX,maxY")

	_, err = ParseBBox("-90,40,-89,north")
	require.ErrorContains(t, err, "not a number")

	_, err = ParseBBox("-89,40,-90,41")
	require.ErrorContains(t, err, "minimum past its maximum")
}

func TestExtentSQLReadsTheEnvelopeAndItsCorners(t *testing.T) {
	require.Contains(t, extentSQL, "ST_Extent_Agg(triples.object_geometry)")
	require.Contains(t, extentSQL, "ST_AsGeoJSON(envelope)")
	require.Contains(t, extentSQL, "ST_XMin(envelope)")
	require.Contains(t, extentSQL, "ST_YMax(envelope)")
	require.True(t, needsSpatial(extentSQL, SimpleObjects))
}
