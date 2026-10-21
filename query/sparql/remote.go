package sparql

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// RemoteTableSchemes are the URL schemes a triples table on object storage may
// be queried at: the ones DuckDB's httpfs and azure extensions read, and so the
// ones `sal upload` writes a table to. A plain path, a file:// URL, or any other
// scheme is refused, so that a caller who can name a table to query, such as a
// browser on the `sal serve --with-ui` origin, cannot name a file on the
// machine running the server.
var RemoteTableSchemes = []string{"s3", "gs", "r2", "az", "https"}

// LocateRemoteTable resolves the root of a triples table on object storage,
// such as the gs://bucket/sal/triples `sal upload` wrote, into a TriplesTable
// that queries read over the network. It carries no warehouse, no namespace,
// and no imports: the data product is wherever the URL says, and nothing on the
// local machine describes it.
func LocateRemoteTable(location string) (TriplesTable, error) {
	location = strings.TrimSpace(location)
	parsed, err := url.Parse(location)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || !slices.Contains(RemoteTableSchemes, strings.ToLower(parsed.Scheme)) {
		return TriplesTable{}, fmt.Errorf("%q is not a table on object storage; a remote table is named by a %s URL such as gs://bucket/sal/triples", location, strings.Join(RemoteTableSchemes, ", "))
	}
	// the Cloud console's browser URL for a bucket is the one a user has at
	// hand, and it answers HTML behind a login rather than the objects
	if strings.EqualFold(parsed.Host, "storage.cloud.google.com") {
		return TriplesTable{}, fmt.Errorf("%s is the Google Cloud console, not the bucket; name the table as gs://%s or https://storage.googleapis.com%s, whichever `sal upload` wrote it with", location, strings.TrimSuffix(strings.TrimPrefix(parsed.Path, "/"), "/"), strings.TrimSuffix(parsed.Path, "/"))
	}
	return TriplesTable{Path: strings.TrimSuffix(location, "/")}, nil
}

// Remote reports whether the table is on object storage rather than in a local
// warehouse, which is what LocateRemoteTable produces and LocateTriplesTable
// never does.
func (t TriplesTable) Remote() bool {
	return t.Warehouse == ""
}

// isRemoteTablePath reports whether a table path is one LocateRemoteTable would
// accept, so that a query surface built from the path alone can tell a remote
// table from the project's own.
func isRemoteTablePath(tablePath string) bool {
	_, err := LocateRemoteTable(tablePath)
	return err == nil
}

// VerifyRemoteObjectColumns is VerifyObjectColumns for a table on object
// storage. There is no catalog to load the table through, so DuckDB reads its
// schema instead, which touches only the Iceberg metadata and never a data
// file, and which is also the first time the location is read at all: a bucket
// that cannot be reached, or a URL that is not an Iceberg table, fails here
// rather than on the first query.
func VerifyRemoteObjectColumns(ctx context.Context, location string) error {
	// no table path is passed so that the `triples` view is left pointing at
	// whatever table it reads now; a table that turns out unreadable must not
	// have replaced it
	db, err := instance.prepare(ctx, "", nil, false)
	if err != nil {
		return err
	}
	header, rows, err := queryRows(ctx, db, fmt.Sprintf("DESCRIBE SELECT * FROM iceberg_scan('%s', allow_moved_paths = true)", escapeSQLLiteral(location)))
	if err != nil {
		return fmt.Errorf("read the schema of the triples table at %s: %w", location, err)
	}
	nameColumn := slices.Index(header, "column_name")
	if nameColumn < 0 {
		return fmt.Errorf("read the schema of the triples table at %s: DESCRIBE returned no column_name column", location)
	}
	for _, row := range rows {
		if row[nameColumn] == "object_language" {
			return nil
		}
	}
	return fmt.Errorf("the triples table at %s was built by an older sal without the typed object columns and the object_language column this version reads; rebuild it with `sal clean --wipe` and `sal build` and upload it again", location)
}
