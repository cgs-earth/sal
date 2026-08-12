package export

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"

	salsparql "github.com/cgs-earth/sal/query/sparql"
	rdflibgo "github.com/tggo/goRDFlib"
	"github.com/tggo/goRDFlib/nt"
)

// exportBatchSize bounds how many triples accumulate in memory before being
// serialized and written out, so exporting a table larger than memory still
// works: the process holds at most one batch's worth of triples at a time.
const exportBatchSize = 5000

// wktLiteralDatatype is the GeoSPARQL datatype a WKT literal was typed with
// before build stored it as WKB in the object_geometry column.
const wktLiteralDatatype = "http://www.opengis.net/ont/geosparql#wktLiteral"

// ExportCmd streams the built triples table to standard out as N-Triples, so
// its data can be piped into another RDF-aware program.
type ExportCmd struct{}

func (cmd *ExportCmd) Run() error {
	ctx := context.Background()
	tbl, err := salsparql.LocateTriplesTable()
	if err != nil {
		return err
	}
	runner, err := tbl.Runner(ctx, 0)
	if err != nil {
		return err
	}

	out := bufio.NewWriter(os.Stdout)
	batch := rdflibgo.NewGraph()
	pending := 0
	flush := func() error {
		if pending == 0 {
			return nil
		}
		if err := nt.Serialize(batch, out); err != nil {
			return fmt.Errorf("export: %w", err)
		}
		batch = rdflibgo.NewGraph()
		pending = 0
		return nil
	}

	statement := salsparql.ExportSQL(runner.Layout)
	withSpatial := runner.Layout == salsparql.TypedObjects
	err = runner.StreamSQL(ctx, statement, withSpatial, func(row []sql.NullString) error {
		object := objectTerm(row[2:], runner.Layout)
		batch.Add(subjectTerm(row[0].String), rdflibgo.NewURIRefUnsafe(row[1].String), object)
		pending++
		if pending >= exportBatchSize {
			return flush()
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}
	return out.Flush()
}

// blankNodeID matches the exact shape stabilizeBlankNodes gives every blank
// node before a graph is written to the triples table: "sal_" followed by 24
// hex characters, optionally suffixed with a 4-digit disambiguator. This ID
// shape is a fixed encoding SAL always applies to every blank node before
// writing it (see build/load/blank_nodes.go), so decoding it back to N-Triples
// "_:" syntax does not change the value, only its term-kind syntax; deciding
// this any other way (e.g. "no scheme means blank node") is unsound, since SAL
// also writes relative IRIs with no scheme as ordinary subjects.
var blankNodeID = regexp.MustCompile(`^sal_[0-9a-f]{24}(_[0-9]{4})?$`)

// hasIRIScheme matches a leading RFC 3986 scheme. It is only a good signal
// for the simple layout's single object column, where a literal, an IRI, and
// (rarely) a blank node all land in the same text with nothing else to go on.
var hasIRIScheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+\-.]*:`)

// subjectTerm rebuilds the subject column as the IRI or blank node it names,
// writing the stored value exactly as-is (no base resolution): a relative IRI
// in the table is exported as a relative IRI, since export mirrors what build
// wrote rather than rewriting it. A subject is always an IRI or a blank node,
// and unlike a plain string literal it is never mistaken for the blank node ID
// shape by accident.
func subjectTerm(value string) rdflibgo.Subject {
	if blankNodeID.MatchString(value) {
		return rdflibgo.NewBNode(value)
	}
	return rdflibgo.NewURIRefUnsafe(value)
}

// objectTerm rebuilds the RDF term an object column (or columns) held,
// writing the stored value exactly as-is. The typed layout stores which union
// member is populated, so restoring an IRI, a numeric literal's xsd:double
// datatype, and a geometry literal's geosparql:wktLiteral datatype is exact;
// a blank node object is not, since it and a plain string literal both land
// in object_string with nothing left to tell them apart, so it comes back as
// a string literal. The simple layout has no such columns at all, so only
// IRI-shaped text is recovered as an IRI there; everything else, including a
// blank node object, comes back as a string literal.
func objectTerm(cols []sql.NullString, layout salsparql.ObjectLayout) rdflibgo.Term {
	if layout == salsparql.SimpleObjects {
		value := cols[0]
		switch {
		case blankNodeID.MatchString(value.String):
			return rdflibgo.NewBNode(value.String)
		case hasIRIScheme.MatchString(value.String):
			return rdflibgo.NewURIRefUnsafe(value.String)
		default:
			return rdflibgo.NewLiteral(value.String)
		}
	}

	iri, float, wkt, str := cols[0], cols[1], cols[2], cols[3]
	switch {
	case iri.Valid:
		return rdflibgo.NewURIRefUnsafe(iri.String)
	case wkt.Valid:
		return rdflibgo.NewLiteral(wkt.String, rdflibgo.WithDatatype(rdflibgo.NewURIRefUnsafe(wktLiteralDatatype)))
	case float.Valid:
		return rdflibgo.NewLiteral(float.String, rdflibgo.WithDatatype(rdflibgo.XSDDouble))
	default:
		return rdflibgo.NewLiteral(str.String)
	}
}
