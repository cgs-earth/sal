package load

import (
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
	batchSize int

	index   int
	current arrow.RecordBatch
	err     error
	rows    int64
}

// newGraphRecordReader snapshots graph triples and exposes them as Arrow record batches.
func newGraphRecordReader(graph *rdflibgo.Graph, schema *arrow.Schema, batchSize int) *graphRecordReader {
	r := &graphRecordReader{
		schema:    schema,
		pool:      memory.NewGoAllocator(),
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

		builder.Field(0).(*array.StringBuilder).Append(triple.Subject.String())
		builder.Field(1).(*array.StringBuilder).Append(triple.Predicate.String())
		builder.Field(2).(*array.StringBuilder).Append(triple.Object.String())
		count++
		r.rows++
	}
	if count == 0 {
		return nil, nil
	}

	return builder.NewRecordBatch(), nil
}

func (r *graphRecordReader) releaseCurrent() {
	if r.current != nil {
		r.current.Release()
		r.current = nil
	}
}
