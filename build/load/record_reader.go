package load

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/cgs-earth/sal/pkg"
	geoarrow "github.com/geoarrow/geoarrow-go"
	"github.com/twpayne/go-geom/encoding/wkb"
	"github.com/twpayne/go-geom/encoding/wkt"
)

const geoSPARQLWKTLiteral = "http://www.opengis.net/ont/geosparql#wktLiteral"

// objectBuilders holds the builders of the typed object columns, indexed to
// match GetSchemas. object columns start after subject and predicate.
type objectBuilders struct {
	str     *array.StringBuilder
	iri     *array.StringBuilder
	geo     *geoarrow.WKBBuilder
	byt     *array.Int32Builder
	integer *array.Int64Builder
	float   *array.Float64Builder
	time    *array.TimestampBuilder
	typ     *array.StringBuilder
}

func newObjectBuilders(builder *array.RecordBuilder) objectBuilders {
	return objectBuilders{
		str:     builder.Field(2).(*array.StringBuilder),
		iri:     builder.Field(3).(*array.StringBuilder),
		geo:     builder.Field(4).(*geoarrow.WKBBuilder),
		byt:     builder.Field(5).(*array.Int32Builder),
		integer: builder.Field(6).(*array.Int64Builder),
		float:   builder.Field(7).(*array.Float64Builder),
		time:    builder.Field(8).(*array.TimestampBuilder),
		typ:     builder.Field(9).(*array.StringBuilder),
	}
}

// nullsExcept appends NULL to every typed object value column but the one the
// row's value was appended to, so exactly one value column is set per row.
// object_type is appended separately since it accompanies a value rather than
// being one.
func (b objectBuilders) nullsExcept(set interface{ AppendNull() }) {
	for _, builder := range []interface{ AppendNull() }{b.str, b.iri, b.geo, b.byt, b.integer, b.float, b.time} {
		if builder != set {
			builder.AppendNull()
		}
	}
}

// appendObjectFields serializes an RDF object into the Iceberg object union
// columns: an IRI into object_iri, a WKT literal into object_geometry, and a
// literal whose XSD datatype has a typed column into object_byte,
// object_integer, object_float, or object_time. Everything else -- blank
// nodes, strings, and any literal whose datatype has no typed column or whose
// lexical form does not parse as one -- lands in object_string. Exactly one
// value column is set on each row, and object_type records the literal's
// datatype IRI so the original RDF term can be rebuilt losslessly.
func appendObjectFields(builder *array.RecordBuilder, t rdfObject) error {
	b := newObjectBuilders(builder)

	if t.oKind == objectKindIRI {
		b.iri.Append(t.o)
		b.nullsExcept(b.iri)
		b.typ.AppendNull()
		return nil
	}

	if isWKTObject(t) {
		wkbBytes, err := wktObjectToWKB(t.o)
		if err != nil {
			return err
		}
		b.geo.Append(geoarrow.WKBBytes(wkbBytes))
		b.nullsExcept(b.geo)
		b.typ.Append(t.oDatatype)
		return nil
	}

	if t.oKind == objectKindLiteral && b.appendTypedLiteral(t) {
		b.typ.Append(t.oDatatype)
		return nil
	}

	// blank nodes, plain and string literals, and typed literals with no
	// (parseable) typed column
	b.str.Append(t.o)
	b.nullsExcept(b.str)
	if t.oKind == objectKindLiteral && t.oDatatype != "" {
		b.typ.Append(t.oDatatype)
	} else {
		b.typ.AppendNull()
	}
	return nil
}

// appendTypedLiteral stores a literal in the typed column its XSD datatype
// names. It reports false, appending nothing, when the datatype has no typed
// column or the lexical form does not parse as that datatype; such a literal
// is stored as a string instead, which object_type keeps lossless.
func (b objectBuilders) appendTypedLiteral(t rdfObject) bool {
	switch {
	case t.oDatatype == pkg.XSDByte:
		value, err := strconv.ParseInt(t.o, 10, 8)
		if err != nil {
			return false
		}
		b.byt.Append(int32(value))
		b.nullsExcept(b.byt)
	case pkg.IsXSDIntegerType(t.oDatatype):
		value, err := strconv.ParseInt(t.o, 10, 64)
		if err != nil {
			return false
		}
		b.integer.Append(value)
		b.nullsExcept(b.integer)
	case pkg.IsXSDFloatType(t.oDatatype):
		value, err := strconv.ParseFloat(t.o, 64)
		if err != nil {
			return false
		}
		b.float.Append(value)
		b.nullsExcept(b.float)
	case t.oDatatype == pkg.XSDDateTime:
		value, ok := parseXSDDateTime(t.o)
		if !ok {
			return false
		}
		b.time.Append(value)
		b.nullsExcept(b.time)
	default:
		return false
	}
	return true
}

// parseXSDDateTime parses an xsd:dateTime into the UTC microseconds the
// object_time column stores. Only a value with an explicit timezone and at
// most microsecond precision is stored natively: a zoneless dateTime would
// have a timezone invented for it, and sub-microsecond digits would be
// truncated, so both fall back to object_string to stay lossless.
func parseXSDDateTime(value string) (arrow.Timestamp, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Nanosecond()%1000 != 0 {
		return 0, false
	}
	return arrow.Timestamp(parsed.UTC().UnixMicro()), true
}

func isWKTObject(t rdfObject) bool {
	return t.oKind == objectKindLiteral && t.oDatatype == geoSPARQLWKTLiteral
}

// wktObjectToWKB converts a GeoSPARQL WKT literal value into WKB bytes.
func wktObjectToWKB(value string) ([]byte, error) {
	geom, err := wkt.Unmarshal(stripGeoSPARQLCRS(value))
	if err != nil {
		return nil, fmt.Errorf("parse WKT %q: %w", value, err)
	}
	wkbBytes, err := wkb.Marshal(geom, binary.LittleEndian)
	if err != nil {
		return nil, fmt.Errorf("marshal WKB: %w", err)
	}
	return wkbBytes, nil
}

func stripGeoSPARQLCRS(value string) string {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "<") {
		return value
	}
	end := strings.Index(value, ">")
	if end == -1 {
		return value
	}
	return strings.TrimSpace(value[end+1:])
}
