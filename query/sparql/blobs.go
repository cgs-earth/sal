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
// name is what GET /blobs/{hash} serves. The file/directory column is the
// subject's rdfs:label, the namespace of a vocabulary or the name of a copied
// file, ending in a slash for a copied directory, and the subject itself when
// it has none. A subject pinned at several versions
// over the table's history lists once per version, since each is a blob.
const blobsSQL = `SELECT
	COALESCE(MIN(%[1]s), version.subject) AS "file/directory",
	version.object_iri AS hash
FROM triples AS version
LEFT JOIN triples AS label
	ON label.subject = version.subject
	AND label.predicate = 'http://www.w3.org/2000/01/rdf-schema#label'
WHERE version.predicate = 'http://www.w3.org/2002/07/owl#versionIRI'
	AND (version.object_iri LIKE 'urn:sha256:%%' OR version.object_iri LIKE 'urn:git-commit-hash:%%')
GROUP BY version.subject, version.object_iri
ORDER BY "file/directory", hash
LIMIT %[2]d`

// BlobsSQL is the DuckDB statement listing the first limit blobs the table
// refers to, the one Blobs runs and the one the UI offers to run by hand when
// the table refers to more than the listing shows.
func BlobsSQL(limit int) string {
	return fmt.Sprintf(blobsSQL, bindingExpr("label", "object"), limit)
}

// Blob is one blob the table refers to, as the Blobs tab lists it.
type Blob struct {
	// File is what the blob is known as: a vocabulary's namespace, or the name
	// of a file or directory a SAL module task handed over, ending in a slash
	// for a directory. It is the listing's file/directory column.
	File string `json:"file"`
	// Hash is the owl:versionIRI naming the blob, urn:sha256:<digest> or
	// urn:git-commit-hash:<commit>, which /blobs/{hash} resolves.
	Hash string `json:"hash"`
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
		if len(row) < 2 {
			return BlobListing{}, fmt.Errorf("blob listing returned %d columns rather than file/directory and hash", len(row))
		}
		listing.Blobs = append(listing.Blobs, Blob{File: strings.TrimSpace(row[0]), Hash: strings.TrimSpace(row[1])})
	}
	return listing, nil
}
