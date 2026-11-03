package sparql

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// featureTriples is a DuckDB with a triples table holding a GeoSPARQL feature
// whose geometry hangs off a blank node, a subject with two geometries of its
// own, and typed literals of every column. The spatial extension would have
// to be downloaded, so object_geometry holds WKT text and macros stand in
// for the ST_ functions the feature SQL calls; everything else in the SQL,
// the joins, the grouping, and the JSON, is what runs for real.
func featureTriples(t *testing.T) *sql.DB {
	t.Helper()
	db := localDB(t)
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `
CREATE MACRO ST_AsGeoJSON(g) AS g;
CREATE MACRO ST_AsText(g) AS g;
CREATE MACRO ST_Collect(l) AS 'GEOMETRYCOLLECTION (' || array_to_string(l, ', ') || ')';
CREATE MACRO ST_Intersects(a, b) AS a LIKE 'POINT%';
CREATE MACRO ST_MakeEnvelope(a, b, c, d) AS NULL;`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `CREATE TABLE triples AS SELECT * FROM (VALUES
	 ('http://a', 'https://schema.org/name', NULL::VARCHAR, NULL::DOUBLE, NULL::BIGINT, NULL::INTEGER, NULL::TIMESTAMP, NULL::VARCHAR, 'alice', 'http://www.w3.org/2001/XMLSchema#string', NULL::VARCHAR, 'h1'),
	 ('http://a', 'https://schema.org/age', NULL, NULL, 30, NULL, NULL, NULL, NULL, 'http://www.w3.org/2001/XMLSchema#integer', NULL, 'h2'),
	 ('http://a', 'https://schema.org/height', NULL, 1.5, NULL, NULL, NULL, NULL, NULL, 'http://www.w3.org/2001/XMLSchema#double', NULL, 'h3'),
	 ('http://a', 'https://schema.org/knows', 'http://b', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, 'h4'),
	 ('http://a', 'https://schema.org/knows', 'http://c', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, 'h5'),
	 ('http://a', 'https://schema.org/active', NULL, NULL, NULL, NULL, NULL, NULL, 'true', 'http://www.w3.org/2001/XMLSchema#boolean', NULL, 'h6'),
	 ('http://a', 'http://www.opengis.net/ont/geosparql#hasGeometry', NULL, NULL, NULL, NULL, NULL, NULL, '_:g1', NULL, NULL, 'h7'),
	 ('_:g1', 'http://www.opengis.net/ont/geosparql#asWKT', NULL, NULL, NULL, NULL, NULL, 'POINT (1 2)', NULL, 'http://www.opengis.net/ont/geosparql#wktLiteral', NULL, 'h8'),
	 ('http://a', 'http://www.w3.org/1999/02/22-rdf-syntax-ns#type', 'https://schema.org/Person', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, 'h9'),
	 ('http://c', 'https://schema.org/birthDate', NULL, NULL, NULL, NULL, TIMESTAMP '1990-06-15 00:00:00', NULL, NULL, 'http://www.w3.org/2001/XMLSchema#dateTime', NULL, 'h10'),
	 ('http://c', 'https://schema.org/geo', NULL, NULL, NULL, NULL, NULL, 'POLYGON ((0 0, 1 0, 1 1, 0 0))', NULL, 'http://www.opengis.net/ont/geosparql#wktLiteral', NULL, 'h11'),
	 ('http://c', 'https://schema.org/geo', NULL, NULL, NULL, NULL, NULL, 'POINT (3 4)', NULL, 'http://www.opengis.net/ont/geosparql#wktLiteral', NULL, 'h12')
	) t(subject, predicate, object_iri, object_float, object_integer, object_byte, object_time, object_geometry, object_string, object_type, object_language, triple_hash)`)
	require.NoError(t, err)
	return db
}

func features(t *testing.T, db *sql.DB, query FeatureQuery) [][]string {
	t.Helper()
	if query.Limit == 0 {
		query.Limit = 10
	}
	statement := featuresSQL(TriplesView, query)
	header, rows, err := queryRows(context.Background(), db, statement)
	require.NoError(t, err, statement)
	require.Equal(t, []string{"id", "geometry", "properties"}, header)
	return rows
}

func TestFeaturesAreSubjectsWithAGeometryReportedUnderTheGeoSPARQLFeature(t *testing.T) {
	rows := features(t, featureTriples(t), FeatureQuery{})

	// the geometry node _:g1 is reported under http://a, the subject that
	// hangs it off geo:hasGeometry; http://c carries its own geometries
	require.Equal(t, [][]string{
		{"http://a", "POINT (1 2)", "{}"},
		{"http://c", "GEOMETRYCOLLECTION (POLYGON ((0 0, 1 0, 1 1, 0 0)), POINT (3 4))", "{}"},
	}, rows)
}

func TestFeaturePropertiesKeepTheirTypesAndAreNullWhenAbsent(t *testing.T) {
	rows := features(t, featureTriples(t), FeatureQuery{Properties: []string{
		"https://schema.org/name",
		"https://schema.org/age",
		"https://schema.org/height",
		"https://schema.org/knows",
		"https://schema.org/active",
		"https://schema.org/birthDate",
		"https://schema.org/geo",
	}})

	require.Len(t, rows, 2)
	require.JSONEq(t, `{
		"https://schema.org/name": "alice",
		"https://schema.org/age": 30,
		"https://schema.org/height": 1.5,
		"https://schema.org/knows": ["http://b", "http://c"],
		"https://schema.org/active": true,
		"https://schema.org/birthDate": null,
		"https://schema.org/geo": null
	}`, rows[0][2])
	require.JSONEq(t, `{
		"https://schema.org/name": null,
		"https://schema.org/age": null,
		"https://schema.org/height": null,
		"https://schema.org/knows": null,
		"https://schema.org/active": null,
		"https://schema.org/birthDate": "1990-06-15T00:00:00Z",
		"https://schema.org/geo": ["POLYGON ((0 0, 1 0, 1 1, 0 0))", "POINT (3 4)"]
	}`, rows[1][2])
}

func TestFeaturesAreFilteredByTypeAndBoundingBox(t *testing.T) {
	db := featureTriples(t)

	rows := features(t, db, FeatureQuery{Type: "https://schema.org/Person"})
	require.Equal(t, [][]string{{"http://a", "POINT (1 2)", "{}"}}, rows)

	// the stand-in ST_Intersects matches points only, so the collection is left out
	box := BBox{MinX: 0, MinY: 0, MaxX: 2, MaxY: 3}
	rows = features(t, db, FeatureQuery{BBox: &box})
	require.Equal(t, [][]string{{"http://a", "POINT (1 2)", "{}"}}, rows)

	rows = features(t, db, FeatureQuery{Type: "https://schema.org/Place"})
	require.Empty(t, rows)
}

func TestFeaturesPageOneRowPastTheLimit(t *testing.T) {
	db := featureTriples(t)

	rows := features(t, db, FeatureQuery{Limit: 1})
	require.Len(t, rows, 2, "a page asks for one row past its limit to learn whether more follow")

	rows = features(t, db, FeatureQuery{Limit: 1, Offset: 1})
	require.Equal(t, "http://c", rows[0][0])
	require.Len(t, rows, 1)
}

func TestFeatureByIDIgnoresPagingAndTheBox(t *testing.T) {
	box := BBox{MinX: 100, MinY: 100, MaxX: 101, MaxY: 101}
	rows := features(t, featureTriples(t), FeatureQuery{ID: "http://c", BBox: &box, Offset: 5, Properties: []string{"https://schema.org/birthDate"}})

	require.Len(t, rows, 1)
	require.Equal(t, "http://c", rows[0][0])
	require.JSONEq(t, `{"https://schema.org/birthDate": "1990-06-15T00:00:00Z"}`, rows[0][2])

	sql := featuresSQL(TriplesView, FeatureQuery{ID: "http://c"})
	require.NotContains(t, sql, "LIMIT")
	require.NotContains(t, sql, "ST_Intersects")
}

func TestFeaturesSQLReadsASnapshotScanEverywhere(t *testing.T) {
	source := snapshotScanSQL("/tmp/table", 42)
	sql := featuresSQL(source, FeatureQuery{Limit: 5, Properties: []string{"https://schema.org/name"}, Type: "https://schema.org/Person"})

	require.NotContains(t, sql, "FROM triples")
	require.Equal(t, 4, strings.Count(sql, source), "the geometries, their owners, the property, and the type are all read at the snapshot")
	require.True(t, needsSpatial(sql))
}

func TestFeaturesRejectsAPropertyThatIsNotAnIRI(t *testing.T) {
	_, err := DuckDBRunner{}.Features(context.Background(), FeatureQuery{Properties: []string{"name"}})
	require.ErrorContains(t, err, `property "name" is not an absolute IRI`)

	_, err = DuckDBRunner{}.Features(context.Background(), FeatureQuery{Type: "a b"})
	require.ErrorContains(t, err, `type "a b" is not an absolute IRI`)

	tooMany := make([]string, MaxFeatureProperties+1)
	for i := range tooMany {
		tooMany[i] = "https://schema.org/name"
	}
	_, err = DuckDBRunner{}.Features(context.Background(), FeatureQuery{Properties: tooMany})
	require.ErrorContains(t, err, "at most 50 properties")
}

func TestNoSnapshotErrorNamesTheSnapshot(t *testing.T) {
	require.EqualError(t, NoSnapshotError{SnapshotID: 7}, "the triples table has no snapshot 7")
}
