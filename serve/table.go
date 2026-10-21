package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	salsparql "github.com/cgs-earth/sal/query/sparql"
)

// openRemoteTable opens the query surface for a triples table on object
// storage. It is a variable so that tests can switch tables without DuckDB
// reading a bucket.
var openRemoteTable = func(ctx context.Context, location string) (UIRunner, error) {
	table, err := salsparql.LocateRemoteTable(location)
	if err != nil {
		return nil, err
	}
	return table.Runner(ctx, 0)
}

// tableSwitch is the UIRunner the web UI's handlers query through. It starts
// on the project's own table and can be pointed at a table on object storage
// and back, so that the data product `sal upload` wrote can be queried and its
// stats read from the UI already running, without serving it from another
// machine. Every method delegates to whichever table is current.
//
// The switch is per server, not per browser: every request after a switch
// reads the new table. A query already prepared on the shared DuckDB handle
// when the `triples` view is replaced can read the new table too, which a
// debugging UI accepts rather than holding every query under a lock.
type tableSwitch struct {
	local UIRunner
	mu    sync.RWMutex
	// remote is the runner over the table on object storage, nil while the
	// local table is current.
	remote UIRunner
	// remotePath is the location remote was opened at.
	remotePath string
}

// current is the runner the next request should go to.
func (s *tableSwitch) current() UIRunner {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.remote != nil {
		return s.remote
	}
	return s.local
}

// use points the switch at the table at location, or back at the local table
// when location is empty. The location is checked to name object storage
// before it is opened, here rather than only in the opener, so that a path on
// the machine running the server is refused whatever opens a table; the remote
// table is then opened, and so checked to be a readable triples table, before
// anything is switched, so a location that cannot be read leaves the current
// table in place.
func (s *tableSwitch) use(ctx context.Context, location string) error {
	location = strings.TrimSpace(location)
	if location == "" {
		s.mu.Lock()
		s.remote, s.remotePath = nil, ""
		s.mu.Unlock()
		return nil
	}
	if _, err := salsparql.LocateRemoteTable(location); err != nil {
		return err
	}
	runner, err := openRemoteTable(ctx, location)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.remote, s.remotePath = runner, location
	s.mu.Unlock()
	return nil
}

// location is the remote table the switch reads, or empty for the local one.
func (s *tableSwitch) location() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.remotePath
}

func (s *tableSwitch) Run(ctx context.Context, query string) (salsparql.Result, error) {
	return s.current().Run(ctx, query)
}

func (s *tableSwitch) HasSnapshot(ctx context.Context, snapshotID int64) (bool, error) {
	return s.current().HasSnapshot(ctx, snapshotID)
}

func (s *tableSwitch) AtSnapshot(snapshotID int64) salsparql.Runner {
	return s.current().AtSnapshot(snapshotID)
}

func (s *tableSwitch) Geometries(ctx context.Context, query salsparql.GeometryQuery) (salsparql.FeatureCollection, error) {
	return s.current().Geometries(ctx, query)
}

func (s *tableSwitch) Extent(ctx context.Context) (salsparql.Feature, error) {
	return s.current().Extent(ctx)
}

func (s *tableSwitch) RunSQL(ctx context.Context, sql string) (salsparql.Result, error) {
	return s.current().RunSQL(ctx, sql)
}

func (s *tableSwitch) Translate(query string) (string, error) {
	return s.current().Translate(query)
}

func (s *tableSwitch) Stats(ctx context.Context) (salsparql.TableStats, error) {
	return s.current().Stats(ctx)
}

func (s *tableSwitch) Blobs(ctx context.Context, limit int) (salsparql.BlobListing, error) {
	return s.current().Blobs(ctx, limit)
}

// tableLocation is what /api/table reports: the remote table the UI's queries
// read, empty when they read the project's own table.
type tableLocation struct {
	RemoteTablePath string `json:"remoteTablePath"`
}

// tableHandler answers GET /api/table with the table the UI's queries read and
// switches it on PUT, from a {"remoteTablePath": "gs://..."} body naming a
// table on object storage or an empty one meaning the local table. A location
// LocateRemoteTable refuses, a local path above all, answers 400 without
// touching DuckDB; one DuckDB cannot read as a triples table answers 400 with
// its error and leaves the current table in place.
type tableHandler struct {
	tables *tableSwitch
}

func (h tableHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, tableLocation{RemoteTablePath: h.tables.location()})
	case http.MethodPut:
		defer func() {
			if err := r.Body.Close(); err != nil {
				slog.Error("failed to close table request body", "error", err)
			}
		}()
		var request tableLocation
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("parse table request: %v", err))
			return
		}
		if err := h.tables.use(r.Context(), request.RemoteTablePath); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if location := h.tables.location(); location != "" {
			slog.Info("the UI now queries a table on object storage", "table", location)
		} else {
			slog.Info("the UI now queries the project's own table")
		}
		writeJSON(w, tableLocation{RemoteTablePath: h.tables.location()})
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "the table endpoint only supports GET and PUT requests", http.StatusMethodNotAllowed)
	}
}
