package sparql

import (
	"context"
	"fmt"
	"strings"
)

// blobsSQL lists every blob the triples table refers to: each subject whose
// owl:versionIRI is a urn:sha256: or urn:git-commit-hash: name, which is how
// build records a pinned vocabulary, and how a file or directory a SAL module
// task handed over is named once it is copied into the blob store. Every such
// name is what GET /blobs/{hash} serves. The iri column is the subject's
// rdfs:label, the IRI the blob was known by before it was pinned: a
// vocabulary's namespace, or the file:/// IRI a task named a copied file or
// directory with, and the subject itself when it has no label. The modified
// column is the subject's dcterms:modified, which build records on a pinned
// vocabulary and LinkCopiedFiles on a copy, rendered as text the way a query
// projects it, and empty for a subject that has none. A subject pinned at
// several versions over the table's history lists once per version, since
// each is a blob.
const blobsSQL = `SELECT
	COALESCE(MIN(%[1]s), version.subject) AS iri,
	version.object_iri AS hash,
	MAX(%[2]s) AS modified
FROM triples AS version
LEFT JOIN triples AS label
	ON label.subject = version.subject
	AND label.predicate = 'http://www.w3.org/2000/01/rdf-schema#label'
LEFT JOIN triples AS modified
	ON modified.subject = version.subject
	AND modified.predicate = 'http://purl.org/dc/terms/modified'
WHERE version.predicate = 'http://www.w3.org/2002/07/owl#versionIRI'
	AND (version.object_iri LIKE 'urn:sha256:%%' OR version.object_iri LIKE 'urn:git-commit-hash:%%')
GROUP BY version.subject, version.object_iri
ORDER BY iri, hash
LIMIT %[3]d`

// BlobsSQL is the DuckDB statement listing the first limit blobs the table
// refers to, the one Blobs runs and the one the UI offers to run by hand when
// the table refers to more than the listing shows.
func BlobsSQL(limit int) string {
	return fmt.Sprintf(blobsSQL, bindingExpr("label", "object"), bindingExpr("modified", "object"), limit)
}

// Blob is one blob the table refers to, as the Blobs tab lists it.
type Blob struct {
	// IRI is what the blob was known by before it was pinned: a vocabulary's
	// namespace, or the file:/// IRI a SAL module task named a copied file or
	// directory with.
	IRI string `json:"iri"`
	// Hash is the owl:versionIRI naming the blob, urn:sha256:<digest> or
	// urn:git-commit-hash:<commit>, which /blobs/{hash} resolves.
	Hash string `json:"hash"`
	// Modified is the subject's dcterms:modified as text, when it has one:
	// when the vocabulary was pinned, or the mtime of a copied file.
	Modified string `json:"modified,omitempty"`
}

// BlobListing is the first limit blobs the table refers to.
type BlobListing struct {
	Blobs []Blob `json:"blobs"`
	// Truncated reports that the table refers to more blobs than were listed.
	Truncated bool `json:"truncated"`
	// SQL is the statement that lists the same blobs, for running by hand.
	SQL string `json:"sql"`
}

// BlobRunner lists the blobs a table refers to, which the UI's Blobs tab reads.
type BlobRunner interface {
	Blobs(ctx context.Context, limit int) (BlobListing, error)
}

// Blobs lists the first limit blobs the triples table refers to, in order of
// their names, and reports whether the table refers to more.
func (r DuckDBRunner) Blobs(ctx context.Context, limit int) (BlobListing, error) {
	// one row past the limit is asked for so that a full page is told apart from
	// a truncated one without counting the whole table
	result, err := r.RunSQL(ctx, BlobsSQL(limit+1))
	if err != nil {
		return BlobListing{}, err
	}
	listing := BlobListing{Blobs: []Blob{}, SQL: BlobsSQL(limit)}
	for _, row := range result.Rows {
		if len(listing.Blobs) == limit {
			listing.Truncated = true
			break
		}
		if len(row) < 3 {
			return BlobListing{}, fmt.Errorf("blob listing returned %d columns rather than iri, hash, and modified", len(row))
		}
		listing.Blobs = append(listing.Blobs, Blob{IRI: strings.TrimSpace(row[0]), Hash: strings.TrimSpace(row[1]), Modified: strings.TrimSpace(row[2])})
	}
	return listing, nil
}
