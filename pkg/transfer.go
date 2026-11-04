package pkg

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// The attributes a transfer span carries: how many files and bytes it moves
// in all, known before it starts, and how many it has moved so far, updated
// as it goes. The progress pane reads them off the live span to draw the
// transfer, and a trace exporter sees the final counts.
const (
	TransferFilesAttribute     = "sal.oci.files"
	TransferBytesAttribute     = "sal.oci.bytes"
	TransferFilesDoneAttribute = "sal.oci.files_done"
	TransferBytesDoneAttribute = "sal.oci.bytes_done"
)

// transferReportEvery is how many bytes a transfer moves between two
// updates of its span, so that a large file still advances the pane
// without the span being written on every read.
const transferReportEvery = 256 * 1024

// transferLogEvery is how many files a transfer completes between two log
// lines saying how far it is, for a run with no pane to watch.
const transferLogEvery = 5

// Transfer counts the files and bytes an OCI push or pull has moved and
// reports them on the span that covers it as it goes: the span's attributes
// are what the progress pane draws, and every few files a log line says how
// far along the transfer is for a run with no pane. It is safe to use from
// the goroutines that move the files concurrently.
type Transfer struct {
	span      trace.Span
	verb      string
	started   time.Time
	files     int64
	bytes     int64
	filesDone atomic.Int64
	bytesDone atomic.Int64
	// reported is bytesDone as of the last span update
	reported atomic.Int64
}

// NewTransfer starts counting a transfer of files files holding bytes bytes
// on span. verb is the past participle the log lines use, "Pulled" or
// "Uploaded".
func NewTransfer(span trace.Span, verb string, files int, bytes int64) *Transfer {
	t := &Transfer{span: span, verb: verb, started: time.Now(), files: int64(files), bytes: bytes}
	span.SetAttributes(
		attribute.Int64(TransferFilesAttribute, t.files),
		attribute.Int64(TransferBytesAttribute, t.bytes),
		attribute.Int64(TransferFilesDoneAttribute, 0),
		attribute.Int64(TransferBytesDoneAttribute, 0),
	)
	return t
}

// Add counts n more bytes moved.
func (t *Transfer) Add(n int64) {
	if n <= 0 {
		return
	}
	done := t.bytesDone.Add(n)
	if done-t.reported.Load() >= transferReportEvery {
		t.reported.Store(done)
		t.span.SetAttributes(attribute.Int64(TransferBytesDoneAttribute, done))
	}
}

// Discard takes n bytes back out of the count, which is what a failed
// attempt at a file does before the file is tried again, so that the bytes
// read twice are counted once.
func (t *Transfer) Discard(n int64) {
	if n <= 0 {
		return
	}
	done := t.bytesDone.Add(-n)
	t.reported.Store(done)
	t.span.SetAttributes(attribute.Int64(TransferBytesDoneAttribute, done))
}

// FileDone counts one more file moved whole, and logs how far along the
// transfer is every few files, except at the end, which the caller reports.
func (t *Transfer) FileDone() {
	done := t.filesDone.Add(1)
	bytes := t.bytesDone.Load()
	t.reported.Store(bytes)
	t.span.SetAttributes(attribute.Int64(TransferFilesDoneAttribute, done), attribute.Int64(TransferBytesDoneAttribute, bytes))
	if done%transferLogEvery == 0 && done < t.files {
		slog.Info(fmt.Sprintf("%s %d/%d files (%s of %s, %s)", t.verb, done, t.files, BytesToHumanReadable(bytes), BytesToHumanReadable(t.bytes), TransferRate(bytes, time.Since(t.started))))
	}
}

// Rate is the average speed of the transfer so far.
func (t *Transfer) Rate() string {
	return TransferRate(t.bytesDone.Load(), time.Since(t.started))
}

// TransferRate writes the average speed of moving bytes in elapsed, per
// second, in the largest unit that keeps it readable.
func TransferRate(bytes int64, elapsed time.Duration) string {
	if elapsed <= 0 {
		return "0 B/s"
	}
	return BytesToHumanReadable(int64(float64(bytes)/elapsed.Seconds())) + "/s"
}

// FilesDone is how many files the transfer has moved whole so far.
func (t *Transfer) FilesDone() int64 { return t.filesDone.Load() }

// BytesDone is how many bytes the transfer has moved so far.
func (t *Transfer) BytesDone() int64 { return t.bytesDone.Load() }

// Reader counts every byte read through r.
func (t *Transfer) Reader(r io.Reader) io.Reader {
	return &countingReader{Reader: r, transfer: t}
}

// countingReader counts what is read through it on the transfer, and keeps
// its own count so a failed read can be taken back.
type countingReader struct {
	io.Reader
	transfer *Transfer
	read     int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += int64(n)
	r.transfer.Add(int64(n))
	return n, err
}

// Transport counts the bytes of every request body sent through next, which
// is how an upload is followed: the registry client buffers what it pushes
// so it can answer an auth challenge, so the reader handed to it is read
// before anything is sent, and only the request body says what was. A body
// the client sends again after a challenge is counted again, so the pane
// clamps what it draws to the total.
func (t *Transfer) Transport(next http.RoundTripper) http.RoundTripper {
	return &countingTransport{next: next, transfer: t}
}

type countingTransport struct {
	next     http.RoundTripper
	transfer *Transfer
}

func (c *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil && req.Body != http.NoBody {
		body := req.Body
		req = req.Clone(req.Context())
		req.Body = &countingBody{ReadCloser: body, reader: c.transfer.Reader(body)}
	}
	return c.next.RoundTrip(req)
}

// countingBody reads through the transfer's counting reader and closes the
// body it wraps.
type countingBody struct {
	io.ReadCloser
	reader io.Reader
}

func (b *countingBody) Read(p []byte) (int, error) { return b.reader.Read(p) }
