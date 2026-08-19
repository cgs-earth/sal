package export

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"strings"

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

// blankNodePrefix is the N-Triples "_:" syntax build stores every blank node
// with (see build/load's storedSubject and graphTripleObject). "_" cannot
// start an RFC 3986 scheme, so a stored IRI, even a relative one, can never
// begin with it; the prefix alone identifies a blank node.
const blankNodePrefix = "_:"

// hasIRIScheme matches a leading RFC 3986 scheme. It is only a good signal
// for the simple layout's single object column, where a literal, an IRI, and
// (rarely) a blank node all land in the same text with nothing else to go on.
var hasIRIScheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+\-.]*:`)

// subjectTerm rebuilds the subject column as the IRI or blank node it names,
// writing the stored value exactly as-is (no base resolution): a relative IRI
// in the table is exported as a relative IRI, since export mirrors what build
// wrote rather than rewriting it.
func subjectTerm(value string) rdflibgo.Subject {
	if id, ok := strings.CutPrefix(value, blankNodePrefix); ok {
		return rdflibgo.NewBNode(id)
	}
	return rdflibgo.NewURIRefUnsafe(value)
}

// objectTerm rebuilds the RDF term an object column (or columns) held,
// writing the stored value exactly as-is. The typed layout stores which union
// member is populated, so restoring an IRI, a numeric literal's xsd:double
// datatype, and a geometry literal's geosparql:wktLiteral datatype is exact.
// A blank node object lands in object_string (the simple layout's single
// object column) carrying the "_:" prefix build stored it with, which is what
// tells it apart from a plain string literal there; only a literal whose own
// text starts with "_:" would be misread, and nothing SAL builds writes one.
func objectTerm(cols []sql.NullString, layout salsparql.ObjectLayout) rdflibgo.Term {
	if layout == salsparql.SimpleObjects {
		value := cols[0]
		switch {
		case strings.HasPrefix(value.String, blankNodePrefix):
			return rdflibgo.NewBNode(strings.TrimPrefix(value.String, blankNodePrefix))
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
	case strings.HasPrefix(str.String, blankNodePrefix):
		return rdflibgo.NewBNode(strings.TrimPrefix(str.String, blankNodePrefix))
	default:
		return rdflibgo.NewLiteral(str.String)
	}
}
