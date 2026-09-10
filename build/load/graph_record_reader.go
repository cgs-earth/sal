package load

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync/atomic"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	rdflibgo "github.com/tggo/goRDFlib"
)

type graphRecordReader struct {
	refCount atomic.Int64

	schema    *arrow.Schema
	pool      memory.Allocator
	tableRows []tableRow
	hashes    map[string]struct{}
	batchSize int

	hashIndex       int
	vocabularyIndex int

	index   int
	current arrow.RecordBatch
	err     error
	rows    int64
}

// newGraphRecordReader snapshots graph triples and exposes them as Arrow record batches.
func newGraphRecordReader(graph *rdflibgo.Graph, schema *arrow.Schema, batchSize int) *graphRecordReader {
	return newFilteredGraphRecordReader(tableRows(graph, nil), schema, batchSize, nil)
}

// newFilteredGraphRecordReader writes only rows whose hash is present in hashes.
func newFilteredGraphRecordReader(rows []tableRow, schema *arrow.Schema, batchSize int, hashes map[string]struct{}) *graphRecordReader {
	r := &graphRecordReader{
		schema:          schema,
		pool:            memory.NewGoAllocator(),
		tableRows:       rows,
		hashes:          hashes,
		batchSize:       batchSize,
		hashIndex:       schema.FieldIndices("triple_hash")[0],
		vocabularyIndex: schema.FieldIndices("vocabulary")[0],
	}
	r.refCount.Store(1)
	return r
}

func (r *graphRecordReader) Retain() {
	r.refCount.Add(1)
}

func (r *graphRecordReader) Release() {
	if r.refCount.Add(-1) != 0 {
		return
	}
	r.releaseCurrent()
}

func (r *graphRecordReader) Schema() *arrow.Schema {
	return r.schema
}

func (r *graphRecordReader) Next() bool {
	r.releaseCurrent()
	if r.err != nil {
		return false
	}

	rec, err := r.nextBatch()
	if err != nil {
		r.err = err
		return false
	}
	r.current = rec
	return rec != nil
}

func (r *graphRecordReader) RecordBatch() arrow.RecordBatch {
	return r.current
}

func (r *graphRecordReader) Record() arrow.RecordBatch {
	return r.RecordBatch()
}

func (r *graphRecordReader) Err() error {
	return r.err
}

func (r *graphRecordReader) RowsRead() int64 {
	return r.rows
}

// nextBatch converts the next slice of graph triples into an Arrow record batch.
func (r *graphRecordReader) nextBatch() (arrow.RecordBatch, error) {
	if r.batchSize <= 0 {
		return nil, fmt.Errorf("batch size must be greater than zero")
	}

	builder := array.NewRecordBuilder(r.pool, r.schema)
	defer builder.Release()

	count := 0
	for count < r.batchSize && r.index < len(r.tableRows) {
		row := r.tableRows[r.index]
		r.index++

		if r.hashes != nil {
			if _, ok := r.hashes[row.hash]; !ok {
				continue
			}
		}
		triple := row.triple
		object := graphTripleObject(triple.Object)

		builder.Field(0).(*array.StringBuilder).Append(storedSubject(triple.Subject))
		builder.Field(1).(*array.StringBuilder).Append(triple.Predicate.String())
		if err := appendObjectFields(builder, object); err != nil {
			return nil, fmt.Errorf("serialize object for %s %s: %w", triple.Subject.String(), triple.Predicate.String(), err)
		}
		// triple_hash is generated from the subject, predicate, and the object's
		// lexical form, datatype, and language tag, before typed object columns
		// are derived, so the storage representation (geometry WKB, float
		// rendering) does not affect the row identity but the datatype and
		// language do: two triples differing only in datatype or language are
		// distinct rows, matching what object_type and object_language record.
		// The vocabulary is not part of it either: the same statement is one row
		// whether the project or a pinned vocabulary states it.
		builder.Field(r.hashIndex).(*array.StringBuilder).Append(row.hash)
		vocabulary := builder.Field(r.vocabularyIndex).(*array.StringBuilder)
		if row.vocabulary == "" {
			vocabulary.AppendNull()
		} else {
			vocabulary.Append(row.vocabulary)
		}
		count++
		r.rows++
	}
	if count == 0 {
		return nil, nil
	}

	return builder.NewRecordBatch(), nil
}

// tripleHash returns a stable SHA-256 row identifier from the RDF triple
// terms. The object's datatype and language tag are part of the identity
// (both empty for an IRI or a blank node object, and the language empty for
// any literal but an rdf:langString), since the table stores them in
// object_type and object_language and a literal with the same lexical form
// but a different datatype or language is a different triple.
func tripleHash(subject string, predicate string, object string, datatype string, language string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("subject="))
	_, _ = hash.Write([]byte(subject))
	_, _ = hash.Write([]byte("\npredicate="))
	_, _ = hash.Write([]byte(predicate))
	_, _ = hash.Write([]byte("\nobject="))
	_, _ = hash.Write([]byte(object))
	_, _ = hash.Write([]byte("\ndatatype="))
	_, _ = hash.Write([]byte(datatype))
	_, _ = hash.Write([]byte("\nlanguage="))
	_, _ = hash.Write([]byte(language))
	return hex.EncodeToString(hash.Sum(nil))
}

func tripleHashForTriple(triple rdflibgo.Triple) string {
	object := graphTripleObject(triple.Object)
	return tripleHash(storedSubject(triple.Subject), triple.Predicate.String(), object.o, object.oDatatype, object.oLanguage)
}

// storedSubject renders a subject the way the triples table stores it: a blank
// node keeps N-Triples "_:" syntax so readers can tell it apart from an IRI,
// which SAL may store schemeless (relative) and therefore cannot distinguish
// by shape alone. The in-memory BNode keeps its bare label, since RDF
// serializers add the "_:" themselves.
func storedSubject(subject rdflibgo.Subject) string {
	if blank, ok := subject.(rdflibgo.BNode); ok {
		return "_:" + blank.Value()
	}
	return subject.String()
}

func graphTripleObject(object rdflibgo.Term) rdfObject {
	switch o := object.(type) {
	case rdflibgo.URIRef:
		return rdfObject{o: o.Value(), oKind: objectKindIRI}
	case rdflibgo.BNode:
		return rdfObject{o: "_:" + o.Value(), oKind: objectKindBNode}
	case rdflibgo.Literal:
		return rdfObject{o: o.String(), oKind: objectKindLiteral, oDatatype: o.Datatype().Value(), oLanguage: o.Language()}
	default:
		return rdfObject{o: object.String(), oKind: objectKindLiteral}
	}
}

func (r *graphRecordReader) releaseCurrent() {
	if r.current != nil {
		r.current.Release()
		r.current = nil
	}
}
