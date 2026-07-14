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
	triples   []rdflibgo.Triple
	hashes    map[string]struct{}
	batchSize int

	index   int
	current arrow.RecordBatch
	err     error
	rows    int64
}

// newGraphRecordReader snapshots graph triples and exposes them as Arrow record batches.
func newGraphRecordReader(graph *rdflibgo.Graph, schema *arrow.Schema, batchSize int) *graphRecordReader {
	return newFilteredGraphRecordReader(graph, schema, batchSize, nil)
}

// newFilteredGraphRecordReader writes only triples whose hash is present in hashes.
func newFilteredGraphRecordReader(graph *rdflibgo.Graph, schema *arrow.Schema, batchSize int, hashes map[string]struct{}) *graphRecordReader {
	r := &graphRecordReader{
		schema:    schema,
		pool:      memory.NewGoAllocator(),
		hashes:    hashes,
		batchSize: batchSize,
	}
	graph.Triples(nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		r.triples = append(r.triples, triple)
		return true
	})
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
	for count < r.batchSize && r.index < len(r.triples) {
		triple := r.triples[r.index]
		r.index++

		subject := triple.Subject.String()
		predicate := triple.Predicate.String()
		object := graphTripleObject(triple.Object)
		hashValue := tripleHash(subject, predicate, object.o)
		if r.hashes != nil {
			if _, ok := r.hashes[hashValue]; !ok {
				continue
			}
		}

		builder.Field(0).(*array.StringBuilder).Append(subject)
		builder.Field(1).(*array.StringBuilder).Append(predicate)
		if err := appendObjectColumns(builder, object); err != nil {
			return nil, fmt.Errorf("serialize object for %s %s: %w", triple.Subject.String(), triple.Predicate.String(), err)
		}
		// triple_hash is the final schema field. It is generated from subject, predicate,
		// and the object term before typed object columns are derived, so storage type
		// markers like xsd:date or geometry WKB do not affect the row identity.
		lastIndex := r.schema.NumFields() - 1
		builder.Field(lastIndex).(*array.StringBuilder).Append(hashValue)
		count++
		r.rows++
	}
	if count == 0 {
		return nil, nil
	}

	return builder.NewRecordBatch(), nil
}

// tripleHash returns a stable SHA-256 row identifier from the RDF triple terms.
func tripleHash(subject string, predicate string, object string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("subject="))
	_, _ = hash.Write([]byte(subject))
	_, _ = hash.Write([]byte("\npredicate="))
	_, _ = hash.Write([]byte(predicate))
	_, _ = hash.Write([]byte("\nobject="))
	_, _ = hash.Write([]byte(object))
	return hex.EncodeToString(hash.Sum(nil))
}

func tripleHashForTriple(triple rdflibgo.Triple) string {
	return tripleHash(triple.Subject.String(), triple.Predicate.String(), graphTripleObject(triple.Object).o)
}

func graphTripleObject(object rdflibgo.Term) rdfObject {
	switch o := object.(type) {
	case rdflibgo.URIRef:
		return rdfObject{o: o.Value(), oKind: objectKindIRI}
	case rdflibgo.BNode:
		return rdfObject{o: o.Value(), oKind: objectKindBNode}
	case rdflibgo.Literal:
		return rdfObject{o: o.String(), oKind: objectKindLiteral, oDatatype: o.Datatype().Value()}
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
