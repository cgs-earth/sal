package export

import (
	"bytes"
	"database/sql"
	"testing"

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

// objectCols is one row of the object columns ExportSQL selects, so a test
// names the column it fills instead of counting positions.
type objectCols struct {
	iri, float, integer, byteVal, time, wkt, str, typ sql.NullString
}

func (c objectCols) row() []sql.NullString {
	return []sql.NullString{c.iri, c.float, c.integer, c.byteVal, c.time, c.wkt, c.str, c.typ}
}

func TestObjectTermRestoresAnIRI(t *testing.T) {
	object := objectTerm(objectCols{iri: valid("http://example.org/o")}.row())
	require.Equal(t, "<http://example.org/o>", object.N3())
}

// TestObjectTermKeepsARelativeIRIExactlyAsStored is the object position's
// counterpart to TestSubjectTermKeepsARelativeIRIExactlyAsStored: the
// object_iri column records this value as an IRI, and export writes it
// unchanged, matching the raw table exactly (no base resolution).
func TestObjectTermKeepsARelativeIRIExactlyAsStored(t *testing.T) {
	object := objectTerm(objectCols{iri: valid("5a1269257241ad980dab13f371fb4b111706285a94127ec3a3f055da9378cef0")}.row())
	require.Equal(t, "<5a1269257241ad980dab13f371fb4b111706285a94127ec3a3f055da9378cef0>", object.N3())
}

// TestObjectTermRestoresTheStoredDatatype covers each typed value column: the
// object_type column carries the exact datatype the literal was written with,
// so a float typed xsd:float does not come back as the xsd:double the column
// would otherwise imply.
func TestObjectTermRestoresTheStoredDatatype(t *testing.T) {
	object := objectTerm(objectCols{float: valid("42.0"), typ: valid("http://www.w3.org/2001/XMLSchema#float")}.row())
	require.Equal(t, `"42.0"^^<http://www.w3.org/2001/XMLSchema#float>`, object.N3())

	object = objectTerm(objectCols{integer: valid("42"), typ: valid("http://www.w3.org/2001/XMLSchema#int")}.row())
	require.Equal(t, `"42"^^<http://www.w3.org/2001/XMLSchema#int>`, object.N3())

	object = objectTerm(objectCols{byteVal: valid("-8"), typ: valid("http://www.w3.org/2001/XMLSchema#byte")}.row())
	require.Equal(t, `"-8"^^<http://www.w3.org/2001/XMLSchema#byte>`, object.N3())

	object = objectTerm(objectCols{time: valid("2002-05-30T15:30:10Z"), typ: valid("http://www.w3.org/2001/XMLSchema#dateTime")}.row())
	require.Equal(t, `"2002-05-30T15:30:10Z"^^<http://www.w3.org/2001/XMLSchema#dateTime>`, object.N3())

	// a typed literal stored as a string still exports with its datatype
	object = objectTerm(objectCols{str: valid("2026-06-02"), typ: valid("http://www.w3.org/2001/XMLSchema#date")}.row())
	require.Equal(t, `"2026-06-02"^^<http://www.w3.org/2001/XMLSchema#date>`, object.N3())
}

// TestObjectTermFallsBackToTheColumnDatatype covers a row written without
// object_type, where the populated column is all there is to type it with.
func TestObjectTermFallsBackToTheColumnDatatype(t *testing.T) {
	object := objectTerm(objectCols{float: valid("42.0")}.row())
	require.Equal(t, `"42.0"^^<http://www.w3.org/2001/XMLSchema#double>`, object.N3())

	// N3 renders xsd:integer with the bare Turtle shorthand, so check the term
	object = objectTerm(objectCols{integer: valid("42")}.row())
	literal, ok := object.(rdflibgo.Literal)
	require.True(t, ok)
	require.Equal(t, "42", literal.Lexical())
	require.Equal(t, rdflibgo.XSDInteger, literal.Datatype())
}

func TestObjectTermRestoresAGeometryLiteralAsGeoSPARQLWKT(t *testing.T) {
	object := objectTerm(objectCols{wkt: valid("POINT (1 2)"), typ: valid(wktLiteralDatatype)}.row())
	require.Equal(t, `"POINT (1 2)"^^<http://www.opengis.net/ont/geosparql#wktLiteral>`, object.N3())
}

func TestObjectTermFallsBackToAPlainStringLiteral(t *testing.T) {
	object := objectTerm(objectCols{str: valid("hello")}.row())
	require.Equal(t, `"hello"`, object.N3())
}

// An xsd:string type and a language-tagged rdf:langString both export as a
// plain literal: xsd:string is what a plain literal means, and the language
// tag itself is not stored, so retyping the value as langString would write
// an invalid tagless literal.
func TestObjectTermExportsStringAndLangStringAsPlainLiterals(t *testing.T) {
	object := objectTerm(objectCols{str: valid("hello"), typ: valid("http://www.w3.org/2001/XMLSchema#string")}.row())
	require.Equal(t, `"hello"`, object.N3())

	object = objectTerm(objectCols{str: valid("hello"), typ: valid(rdflibgo.RDFLangString.Value())}.row())
	require.Equal(t, `"hello"`, object.N3())
}

// TestObjectTermReadsTheStoredBlankNodePrefixAsABlankNode
// covers the object_string column, which is where build lands
// a blank node object; the stored "_:" prefix is what tells it apart from a
// plain string literal sharing that column.
func TestObjectTermReadsTheStoredBlankNodePrefixAsABlankNode(t *testing.T) {
	object := objectTerm(objectCols{str: valid("_:sal_0123456789abcdef01234567")}.row())
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
		objectTerm(objectCols{float: valid("3.5"), typ: valid("http://www.w3.org/2001/XMLSchema#double")}.row()),
	)

	var out bytes.Buffer
	require.NoError(t, nt.Serialize(graph, &out))
	require.Equal(t,
		"<http://example.org/s> <http://example.org/p> \"3.5\"^^<http://www.w3.org/2001/XMLSchema#double> .\n",
		out.String())
}
