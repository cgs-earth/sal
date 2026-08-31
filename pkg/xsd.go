package pkg

// XSD datatype classification shared by the build write path and the SPARQL
// translator. build/load uses it to pick the typed object column a literal is
// stored in, and query/sparql uses it to pick the column a query term is
// compared against; the two must agree or a query misses the rows build wrote.

const XSDNamespace = "http://www.w3.org/2001/XMLSchema#"

const (
	XSDByte     = XSDNamespace + "byte"
	XSDDateTime = XSDNamespace + "dateTime"
	XSDString   = XSDNamespace + "string"
)

// xsdIntegerTypes are the XSD integer-valued datatypes stored in the
// object_integer column. xsd:byte is left out since it has its own column, and
// xsd:unsignedLong is included even though its largest values overflow int64;
// a value that does not parse falls back to the string column.
var xsdIntegerTypes = map[string]struct{}{
	XSDNamespace + "integer":            {},
	XSDNamespace + "int":                {},
	XSDNamespace + "long":               {},
	XSDNamespace + "short":              {},
	XSDNamespace + "unsignedByte":       {},
	XSDNamespace + "unsignedShort":      {},
	XSDNamespace + "unsignedInt":        {},
	XSDNamespace + "unsignedLong":       {},
	XSDNamespace + "negativeInteger":    {},
	XSDNamespace + "nonNegativeInteger": {},
	XSDNamespace + "nonPositiveInteger": {},
	XSDNamespace + "positiveInteger":    {},
}

// xsdFloatTypes are the XSD datatypes stored in the object_float column.
var xsdFloatTypes = map[string]struct{}{
	XSDNamespace + "float":   {},
	XSDNamespace + "double":  {},
	XSDNamespace + "decimal": {},
}

// IsXSDIntegerType reports whether iri names an integer-valued XSD datatype
// stored in the object_integer column.
func IsXSDIntegerType(iri string) bool {
	_, ok := xsdIntegerTypes[iri]
	return ok
}

// IsXSDFloatType reports whether iri names a floating-point XSD datatype
// stored in the object_float column.
func IsXSDFloatType(iri string) bool {
	_, ok := xsdFloatTypes[iri]
	return ok
}

// IsXSDNumericType reports whether iri names any XSD datatype stored in one of
// the numeric object columns: object_byte, object_integer, or object_float.
func IsXSDNumericType(iri string) bool {
	return iri == XSDByte || IsXSDIntegerType(iri) || IsXSDFloatType(iri)
}
