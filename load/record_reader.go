package load

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"

	"github.com/apache/arrow-go/v18/arrow/array"
	geoarrow "github.com/geoarrow/geoarrow-go"
	"github.com/twpayne/go-geom/encoding/wkb"
	"github.com/twpayne/go-geom/encoding/wkt"
)

const geoSPARQLWKTLiteral = "http://www.opengis.net/ont/geosparql#wktLiteral"

func appendObjectColumns(builder *array.RecordBuilder, t rdfObject) error {
	if builder.Schema().NumFields() == 3 {
		builder.Field(2).(*array.StringBuilder).Append(t.o)
		return nil
	}
	return appendObjectFields(builder, t)
}

// appendObjectFields serializes an RDF object into the Iceberg object union columns.
func appendObjectFields(builder *array.RecordBuilder, t rdfObject) error {
	objectIRI := builder.Field(2).(*array.StringBuilder)
	objectFloat := builder.Field(3).(*array.Float64Builder)
	objectString := builder.Field(4).(*array.StringBuilder)
	objectGeometry := builder.Field(5).(*geoarrow.WKBBuilder)

	if t.oKind == objectKindIRI {
		objectIRI.Append(t.o)
		objectFloat.AppendNull()
		objectString.AppendNull()
		objectGeometry.AppendNull()
		return nil
	}

	if isWKTObject(t) {
		wkbBytes, err := wktObjectToWKB(t.o)
		if err != nil {
			return err
		}
		objectIRI.AppendNull()
		objectFloat.AppendNull()
		objectString.AppendNull()
		objectGeometry.Append(geoarrow.WKBBytes(wkbBytes))
		return nil
	}

	if t.oKind == objectKindLiteral {
		objectValue, err := strconv.ParseFloat(t.o, 64)
		if err == nil {
			objectIRI.AppendNull()
			objectFloat.Append(objectValue)
			objectString.AppendNull()
			objectGeometry.AppendNull()
			return nil
		}
	}

	objectIRI.AppendNull()
	objectFloat.AppendNull()
	objectString.Append(t.o)
	objectGeometry.AppendNull()
	return nil
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
