package export

import (
	"bytes"
	"database/sql"
	"testing"

	salsparql "github.com/cgs-earth/sal/query/sparql"
	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
	"github.com/tggo/goRDFlib/nt"
)

func TestSubjectTermKeepsAnAbsoluteIRI(t *testing.T) {
	subject := subjectTerm("http://example.org/s")
	require.Equal(t, "<http://example.org/s>", subject.N3())
}

func TestSubjectTermReadsTheStoredBlankNodePrefixAsABlankNode(t *testing.T) {
	subject := subjectTerm("_:sal_0123456789abcdef01234567")
	require.Equal(t, "_:sal_0123456789abcdef01234567", subject.N3())

	suffixed := subjectTerm("_:sal_0123456789abcdef01234567_0002")
	require.Equal(t, "_:sal_0123456789abcdef01234567_0002", suffixed.N3())
}

// TestSubjectTermKeepsARelativeIRIExactlyAsStored guards against two things
// at once: reading a schemeless value as a blank node just because it has no
// scheme (SAL itself writes relative IRIs as subjects), and rewriting it
// against some guessed base. Export mirrors whatever build actually wrote; if
// a subject in the table is relative, the export is relative too.
func TestSubjectTermKeepsARelativeIRIExactlyAsStored(t *testing.T) {
	subject := subjectTerm("5a1269257241ad980dab13f371fb4b111706285a94127ec3a3f055da9378cef0")
	require.Equal(t, "<5a1269257241ad980dab13f371fb4b111706285a94127ec3a3f055da9378cef0>", subject.N3())
}

func valid(value string) sql.NullString {
	return sql.NullString{String: value, Valid: true}
}

var invalid = sql.NullString{}

func TestObjectTermForSimpleObjectsKeepsAnAbsoluteIRI(t *testing.T) {
	object := objectTerm([]sql.NullString{valid("http://example.org/o")}, salsparql.SimpleObjects)
	require.Equal(t, "<http://example.org/o>", object.N3())
}

func TestObjectTermForSimpleObjectsFallsBackToAStringLiteral(t *testing.T) {
	object := objectTerm([]sql.NullString{valid("hello world")}, salsparql.SimpleObjects)
	require.Equal(t, `"hello world"`, object.N3())
}

func TestObjectTermForSimpleObjectsReadsTheStoredBlankNodePrefixAsABlankNode(t *testing.T) {
	object := objectTerm([]sql.NullString{valid("_:sal_0123456789abcdef01234567")}, salsparql.SimpleObjects)
	require.Equal(t, "_:sal_0123456789abcdef01234567", object.N3())
}

// TestObjectTermForSimpleObjectsCannotTellARelativeIRIApartFromALiteral
// documents a real, unavoidable limitation of the simple (untyped) layout: it
// has only one text column for every object kind, so a relative IRI with no
// scheme and a plain string literal are indistinguishable and both come back
// as a string literal. This is why the same raw value can render as an IRI in
// subject position and a literal in object position for a table built without
// --typed — the table itself lost that distinction when it was written, and
// export cannot invent it back without guessing.
func TestObjectTermForSimpleObjectsCannotTellARelativeIRIApartFromALiteral(t *testing.T) {
	object := objectTerm([]sql.NullString{valid("5a1269257241ad980dab13f371fb4b111706285a94127ec3a3f055da9378cef0")}, salsparql.SimpleObjects)
	require.Equal(t, `"5a1269257241ad980dab13f371fb4b111706285a94127ec3a3f055da9378cef0"`, object.N3())
}

func TestObjectTermForTypedObjectsRestoresAnIRI(t *testing.T) {
	object := objectTerm([]sql.NullString{valid("http://example.org/o"), invalid, invalid, invalid}, salsparql.TypedObjects)
	require.Equal(t, "<http://example.org/o>", object.N3())
}

// TestObjectTermForTypedObjectsKeepsARelativeIRIExactlyAsStored is the typed
// layout's counterpart to TestSubjectTermKeepsARelativeIRIExactlyAsStored: the
// object_iri column records this value as an IRI, and export writes it
// unchanged, matching the raw table exactly (no base resolution).
func TestObjectTermForTypedObjectsKeepsARelativeIRIExactlyAsStored(t *testing.T) {
	object := objectTerm([]sql.NullString{valid("5a1269257241ad980dab13f371fb4b111706285a94127ec3a3f055da9378cef0"), invalid, invalid, invalid}, salsparql.TypedObjects)
	require.Equal(t, "<5a1269257241ad980dab13f371fb4b111706285a94127ec3a3f055da9378cef0>", object.N3())
}

func TestObjectTermForTypedObjectsRestoresANumericLiteralAsXSDDouble(t *testing.T) {
	object := objectTerm([]sql.NullString{invalid, valid("42.0"), invalid, invalid}, salsparql.TypedObjects)
	require.Equal(t, `"42.0"^^<http://www.w3.org/2001/XMLSchema#double>`, object.N3())
}

func TestObjectTermForTypedObjectsRestoresAGeometryLiteralAsGeoSPARQLWKT(t *testing.T) {
	object := objectTerm([]sql.NullString{invalid, invalid, valid("POINT (1 2)"), invalid}, salsparql.TypedObjects)
	require.Equal(t, `"POINT (1 2)"^^<http://www.opengis.net/ont/geosparql#wktLiteral>`, object.N3())
}

func TestObjectTermForTypedObjectsFallsBackToAPlainStringLiteral(t *testing.T) {
	object := objectTerm([]sql.NullString{invalid, invalid, invalid, valid("hello")}, salsparql.TypedObjects)
	require.Equal(t, `"hello"`, object.N3())
}

// TestObjectTermForTypedObjectsReadsTheStoredBlankNodePrefixAsABlankNode
// covers the typed layout's object_string column, which is where build lands
// a blank node object; the stored "_:" prefix is what tells it apart from a
// plain string literal sharing that column.
func TestObjectTermForTypedObjectsReadsTheStoredBlankNodePrefixAsABlankNode(t *testing.T) {
	object := objectTerm([]sql.NullString{invalid, invalid, invalid, valid("_:sal_0123456789abcdef01234567")}, salsparql.TypedObjects)
	require.Equal(t, "_:sal_0123456789abcdef01234567", object.N3())
}

// TestExportedTriplesSerializeAsValidNTriples checks the terms this package
// rebuilds actually come out in the exact <subject> <predicate> <object>
// shape the export command promises, including proper literal quoting, once
// handed to the same N-Triples serializer the export command writes with.
func TestExportedTriplesSerializeAsValidNTriples(t *testing.T) {
	graph := rdflibgo.NewGraph()
	graph.Add(
		subjectTerm("http://example.org/s"),
		rdflibgo.NewURIRefUnsafe("http://example.org/p"),
		objectTerm([]sql.NullString{invalid, valid("3.5"), invalid, invalid}, salsparql.TypedObjects),
	)

	var out bytes.Buffer
	require.NoError(t, nt.Serialize(graph, &out))
	require.Equal(t,
		"<http://example.org/s> <http://example.org/p> \"3.5\"^^<http://www.w3.org/2001/XMLSchema#double> .\n",
		out.String())
}
