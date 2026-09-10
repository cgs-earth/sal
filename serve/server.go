package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/cgs-earth/sal/build"
	"github.com/cgs-earth/sal/pkg"
	salsparql "github.com/cgs-earth/sal/query/sparql"
	"github.com/cgs-earth/sal/salmodule"
)

const sparqlResultsJSON = "application/sparql-results+json"

// maxUIRows bounds how many rows of a SQL result are sent to the browser.
const maxUIRows = 1000

// UIRunner is the query surface the bundled web UI needs from the DuckDB backend.
type UIRunner interface {
	salsparql.SnapshotRunner
	salsparql.GeometryRunner
	salsparql.SQLRunner
	salsparql.SQLTranslator
	salsparql.StatsRunner
}

// Serve starts a read-only SPARQL Protocol HTTP endpoint backed by DuckDB, plus
// the /blobs endpoint serving the vocabulary and imported ontology documents
// pinned under blobDir and the /stac endpoint serving the STAC catalog build
// wrote under stacDir.
func Serve(ctx context.Context, addr string, runner salsparql.DuckDBRunner, blobDir string, stacDir string, withUI bool) error {
	handler := NewEndpoint(runner, blobDir, stacDir)
	if withUI {
		ui, err := NewEndpointWithUI(runner, blobDir, stacDir)
		if err != nil {
			return err
		}
		handler = ui
	}
	server := &http.Server{
		Addr:    addr,
		Handler: handler,
	}
	go func() {
		<-ctx.Done()
		if err := server.Shutdown(context.Background()); err != nil {
			slog.Error("failed to stop SPARQL endpoint", "error", err)
		}
	}()
	if withUI {
		pkg.Infof("Serving the SAL UI at http://localhost%s/, SPARQL endpoint at http://localhost%s/sparql, and STAC catalog at http://localhost%s/stac/catalog.json\n", addr, addr, addr)
	} else {
		pkg.Infof("Serving SPARQL endpoint at http://localhost%s/sparql and STAC catalog at http://localhost%s/stac/catalog.json\n", addr, addr)
	}
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// NewEndpoint returns an HTTP handler for the SPARQL Protocol query operation,
// at /sparql for the current snapshot and at /v{snapshot}/sparql for an earlier
// one, plus the /blobs endpoint serving the vocabulary and imported ontology
// documents pinned under blobDir and the /stac endpoint serving the STAC
// catalog build wrote under stacDir.
func NewEndpoint(runner salsparql.SnapshotRunner, blobDir string, stacDir string) http.Handler {
	mux := http.NewServeMux()
	handler := sparqlHandler{runner: runner}
	mux.Handle("/", snapshotSparqlHandler{runner: runner, fallback: handler})
	mux.Handle("/sparql", handler)
	mux.Handle("/blobs/", blobHandler{dir: blobDir})
	stac := stacHandler{dir: stacDir}
	mux.Handle("/stac", stac)
	mux.Handle("/stac/", stac)
	return mux
}

// NewEndpointWithUI returns an HTTP handler serving the embedded SAL UI at / along
// with the SPARQL endpoint, the JSON APIs the UI reads, the /blobs endpoint
// serving the vocabulary and imported ontology documents pinned under blobDir,
// and the /stac endpoint serving the STAC catalog under stacDir.
func NewEndpointWithUI(runner UIRunner, blobDir string, stacDir string) (http.Handler, error) {
	ui, err := uiHandler()
	if err != nil {
		return nil, err
	}
	blobs := blobHandler{dir: blobDir}
	mux := http.NewServeMux()
	// /sparql and /blobs name both an endpoint and a UI tab; browserRoute is what
	// decides which of the two a request meant.
	mux.Handle("/sparql", browserRoute{api: sparqlHandler{runner: runner}, ui: ui})
	mux.Handle("/geometries", geometryHandler{runner: runner})
	mux.Handle("/geometries/extent", extentHandler{runner: runner})
	mux.Handle("/api/sql", sqlHandler{runner: runner})
	mux.Handle("/api/sparql/translate", translateHandler{translator: runner})
	mux.Handle("/api/stats", statsHandler{runner: runner})
	mux.Handle("/api/salmodule", salmoduleHandler{inspect: salmodule.Inspect})
	mux.Handle("/blobs/", browserRoute{api: blobs, ui: ui})
	// Registered so that ServeMux answers the tab's own URL rather than redirecting
	// it to /blobs/, which is the endpoint's prefix and not a tab.
	mux.Handle("/blobs", browserRoute{api: blobs, ui: ui})
	// The catalog has no UI tab, so it is the API whatever asked for it.
	stac := stacHandler{dir: stacDir}
	mux.Handle("/stac", stac)
	mux.Handle("/stac/", stac)
	// The versioned SPARQL route has no UI tab of its own, so it is the API
	// whatever asked for it; everything else at the root is the app.
	mux.Handle("/", snapshotSparqlHandler{runner: runner, fallback: ui})
	return mux, nil
}

// blobHandler serves the vocabulary and imported ontology documents a project
// has pinned under .sal/data/blobs. PinnedVocabularies names a document by its
// SHA-256 digest, or, for a salmodule:// vocabulary, by the git commit hash of
// the module repository it was read from. A request may give either name bare
// or headed by the scheme its owl:versionIRI carries, "urn:sha256:" or
// "urn:git-commit-hash:"; the prefix is stripped before it is looked up. Range
// requests are honored via http.ServeContent.
type blobHandler struct {
	dir string
}

func (h blobHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "the blob endpoint only supports GET requests", http.StatusMethodNotAllowed)
		return
	}

	name := strings.TrimPrefix(r.URL.Path, "/blobs/")
	name = strings.TrimPrefix(name, "urn:sha256:")
	name = strings.TrimPrefix(name, "urn:git-commit-hash:")
	if !isBlobName(name) {
		http.NotFound(w, r)
		return
	}

	file, err := os.Open(filepath.Join(h.dir, name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() {
		if err := file.Close(); err != nil {
			slog.Error("failed to close blob file", "error", err)
		}
	}()

	info, err := file.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// a blob is an opaque pinned document, not necessarily text; setting this
	// keeps http.ServeContent from sniffing the content and reporting it as
	// text/plain
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, name, info.ModTime(), file)
}

// stacHandler serves the STAC catalog `sal build` writes under .sal/data/stac:
// the catalog at /stac/catalog.json, which /stac and /stac/ also answer with,
// and the collection of the triples table beneath it. The documents are served
// as written, so the collection's iceberg:metadata_location is the file URL of
// the table's metadata on the machine running the server, which a reader on the
// same machine can pass straight to DuckDB's iceberg_scan(). CORS is open, the
// same as the SPARQL endpoint, so a STAC browser on another origin can read it.
type stacHandler struct {
	dir string
}

func (h stacHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Accept")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD, OPTIONS")
		http.Error(w, "the STAC endpoint only supports GET requests", http.StatusMethodNotAllowed)
		return
	}

	// path.Clean resolves any ".." against the endpoint's root, so nothing the
	// client sends can name a file outside the catalog directory
	name := strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(r.URL.Path, "/stac")), "/")
	if name == "" {
		name = build.StacCatalogFile
	}
	if path.Ext(name) != ".json" {
		http.NotFound(w, r)
		return
	}

	file, err := os.Open(filepath.Join(h.dir, filepath.FromSlash(name)))
	if os.IsNotExist(err) {
		http.Error(w, "no STAC catalog has been built for this data product; run `sal build` to write one", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() {
		if err := file.Close(); err != nil {
			slog.Error("failed to close STAC document", "error", err)
		}
	}()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	http.ServeContent(w, r, name, info.ModTime(), file)
}

// isBlobName reports whether s is a hex string of the length a blob is named
// by: 64 characters for a SHA-256 digest, 40 for a git commit hash. This also
// guards against a request path escaping the blob directory, since a bare hex
// string has no path separators.
func isBlobName(s string) bool {
	if len(s) != 64 && len(s) != 40 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			if c < 'a' || c > 'f' {
				return false
			}
		}
	}
	return true
}

// ModuleInspector dereferences a SAL module reference to the ontology the module
// publishes. It is a field on the handler so that tests do not need docker.
type ModuleInspector func(ctx context.Context, reference string) (*salmodule.ModuleOntology, error)

type salmoduleHandler struct {
	inspect ModuleInspector
}

// ServeHTTP clones, builds, and runs the module named by the module query
// parameter, answering with the JSON-LD ontology it printed. This can take
// minutes the first time a module is seen, since the image has to be built.
func (h salmoduleHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "the SAL module endpoint only supports GET requests", http.StatusMethodNotAllowed)
		return
	}
	reference := strings.TrimSpace(r.URL.Query().Get("module"))
	if reference == "" {
		writeJSONError(w, http.StatusBadRequest, "the SAL module request is missing a module parameter")
		return
	}

	ontology, err := h.inspect(r.Context(), reference)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, struct {
		Module   string          `json:"module"`
		Ontology json.RawMessage `json:"ontology"`
	}{Module: ontology.Namespace, Ontology: ontology.Document})
}

type sqlHandler struct {
	runner salsparql.SQLRunner
}

func (h sqlHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "the SQL endpoint only supports POST requests", http.StatusMethodNotAllowed)
		return
	}
	defer func() {
		if err := r.Body.Close(); err != nil {
			slog.Error("failed to close SQL request body", "error", err)
		}
	}()
	var request struct {
		SQL string `json:"sql"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("parse SQL request: %v", err))
		return
	}
	statement := strings.TrimRight(strings.TrimSpace(request.SQL), ";")
	if statement == "" {
		writeJSONError(w, http.StatusBadRequest, "SQL request is missing a sql field")
		return
	}

	result, err := h.runner.RunSQL(r.Context(), statement)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, truncateResult(result))
}

// truncateResult bounds a result to maxUIRows and says so in the message rather
// than silently dropping the tail.
func truncateResult(result salsparql.Result) salsparql.Result {
	total := len(result.Rows)
	result.Message = fmt.Sprintf("%d rows", total)
	if total > maxUIRows {
		result.Rows = result.Rows[:maxUIRows]
		result.Message = fmt.Sprintf("%d rows (showing the first %d)", total, maxUIRows)
	}
	return result
}

// translateHandler answers the SQL a SPARQL query would run as, so the UI's
// SPARQL tab can show what the endpoint does under the hood. Nothing is run:
// the translation is the same one /sparql performs before it queries DuckDB.
type translateHandler struct {
	translator salsparql.SQLTranslator
}

func (h translateHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "the SPARQL translate endpoint only supports POST requests", http.StatusMethodNotAllowed)
		return
	}
	defer func() {
		if err := r.Body.Close(); err != nil {
			slog.Error("failed to close SPARQL translate request body", "error", err)
		}
	}()
	var request struct {
		Query     string `json:"query"`
		Reasoning bool   `json:"reasoning"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("parse SPARQL translate request: %v", err))
		return
	}
	if strings.TrimSpace(request.Query) == "" {
		writeJSONError(w, http.StatusBadRequest, "SPARQL translate request is missing a query field")
		return
	}

	sql, err := h.translator.Translate(request.Query, request.Reasoning)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, struct {
		SQL string `json:"sql"`
	}{SQL: sql})
}

type statsHandler struct {
	runner salsparql.StatsRunner
}

func (h statsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "the stats endpoint only supports GET requests", http.StatusMethodNotAllowed)
		return
	}
	stats, err := h.runner.Stats(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, stats)
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("failed to write JSON response", "error", err)
	}
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": message}); err != nil {
		slog.Error("failed to write JSON error response", "error", err)
	}
}

// snapshotSparqlPath is the route of the SPARQL endpoint over an earlier
// snapshot, /v{snapshot}/sparql. It is matched by hand at the root rather than
// registered as a ServeMux pattern: a wildcard has to be a whole path segment,
// so the "v" cannot be part of one, and a bare "/{version}/sparql" conflicts
// with the "/blobs/" prefix.
var snapshotSparqlPath = regexp.MustCompile(`^/v([^/]*)/sparql$`)

// snapshotSparqlHandler is the SPARQL Protocol query operation against the
// triples table as it stood at the Iceberg snapshot the path names, so that a
// client can query the data product at a version a build has since moved past.
// A path that does not name a snapshot the table has answers 404; a path that
// is not the versioned route at all goes to fallback, whatever else the root
// serves.
type snapshotSparqlHandler struct {
	runner   salsparql.SnapshotRunner
	fallback http.Handler
}

func (h snapshotSparqlHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	match := snapshotSparqlPath.FindStringSubmatch(r.URL.Path)
	if match == nil {
		h.fallback.ServeHTTP(w, r)
		return
	}
	// A preflight never touches the table, so it is answered before the
	// snapshot is looked up: a browser client that is refused CORS on an
	// unknown snapshot would see a network error instead of the 404.
	if r.Method == http.MethodOptions {
		sparqlHandler{runner: h.runner}.ServeHTTP(w, r)
		return
	}
	snapshotID, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil || snapshotID <= 0 {
		http.Error(w, fmt.Sprintf("%q is not a snapshot ID; the table at a snapshot is queried at /v<snapshot id>/sparql", match[1]), http.StatusNotFound)
		return
	}
	exists, err := h.runner.HasSnapshot(r.Context(), snapshotID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !exists {
		http.Error(w, fmt.Sprintf("the triples table has no snapshot %d", snapshotID), http.StatusNotFound)
		return
	}
	sparqlHandler{runner: h.runner.AtSnapshot(snapshotID)}.ServeHTTP(w, r)
}

type sparqlHandler struct {
	runner salsparql.Runner
}

func (h sparqlHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type")
	w.Header().Set("Accept-Post", "application/sparql-query, application/x-www-form-urlencoded")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST, OPTIONS")
		http.Error(w, "SPARQL endpoint only supports GET and POST query requests", http.StatusMethodNotAllowed)
		return
	}
	if !acceptsSPARQLJSON(r.Header.Get("Accept")) {
		http.Error(w, "only application/sparql-results+json responses are supported", http.StatusNotAcceptable)
		return
	}

	query, reasoning, err := queryFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), statusForQueryRequestError(err))
		return
	}
	result, err := h.runner.Run(r.Context(), query, reasoning)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", sparqlResultsJSON)
	if err := json.NewEncoder(w).Encode(sparqlJSONResult(result)); err != nil {
		slog.Error("failed to write SPARQL JSON result", "error", err)
	}
}

type geometryHandler struct {
	runner salsparql.GeometryRunner
}

// ServeHTTP answers a page of the table's geometries as GeoJSON, narrowed to the
// ones intersecting the bbox parameter when one is given.
func (h geometryHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !allowGeoJSONRequest(w, r) {
		return
	}
	query := salsparql.GeometryQuery{
		Limit:  intQueryParam(r, "limit", salsparql.MaxGeometries),
		Offset: intQueryParam(r, "offset", 0),
	}
	if query.Limit <= 0 || query.Limit > salsparql.MaxGeometries {
		query.Limit = salsparql.MaxGeometries
	}
	if query.Offset < 0 {
		query.Offset = 0
	}
	if bbox := strings.TrimSpace(r.URL.Query().Get("bbox")); bbox != "" {
		box, err := salsparql.ParseBBox(bbox)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		query.BBox = &box
	}

	collection, err := h.runner.Geometries(r.Context(), query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeGeoJSON(w, collection)
}

type extentHandler struct {
	runner salsparql.GeometryRunner
}

// ServeHTTP answers the bounding box of every geometry in the table as a GeoJSON
// feature, so the map can show and fit to the dataset's spatial extent.
func (h extentHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !allowGeoJSONRequest(w, r) {
		return
	}
	extent, err := h.runner.Extent(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeGeoJSON(w, extent)
}

// allowGeoJSONRequest sets the CORS headers of the geometry endpoints and
// answers a preflight or a non-GET request itself, reporting whether the
// handler should go on to answer the request.
func allowGeoJSONRequest(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Accept")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return false
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, OPTIONS")
		http.Error(w, "geometry endpoint only supports GET requests", http.StatusMethodNotAllowed)
		return false
	}
	return true
}

func writeGeoJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/geo+json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("failed to write GeoJSON result", "error", err)
	}
}

func intQueryParam(r *http.Request, name string, fallback int) int {
	value := strings.TrimSpace(r.URL.Query().Get(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

// queryFromRequest reads the query a SPARQL Protocol request carries, and
// whether it asks to reason over the pinned vocabularies, which the
// `reasoning` parameter says: in the URL of a GET or a direct POST, and in
// the URL or the body of a form POST.
func queryFromRequest(r *http.Request) (string, bool, error) {
	var query string
	var values url.Values
	switch r.Method {
	case http.MethodGet:
		values = r.URL.Query()
		query = values.Get("query")
	case http.MethodPost:
		contentType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
		switch contentType {
		case "application/sparql-query":
			defer func() {
				if err := r.Body.Close(); err != nil {
					slog.Error("failed to close SPARQL request body", "error", err)
				}
			}()
			b, err := io.ReadAll(r.Body)
			if err != nil {
				return "", false, fmt.Errorf("read SPARQL query body: %w", err)
			}
			values = r.URL.Query()
			query = string(b)
		case "application/x-www-form-urlencoded", "":
			if err := r.ParseForm(); err != nil {
				return "", false, fmt.Errorf("parse SPARQL form request: %w", err)
			}
			values = r.Form
			query = values.Get("query")
		default:
			return "", false, errUnsupportedMediaType
		}
	default:
		return "", false, fmt.Errorf("unsupported method %s", r.Method)
	}
	query, err := requiredQuery(query)
	if err != nil {
		return "", false, err
	}
	reasoning, err := reasoningParam(values)
	if err != nil {
		return "", false, err
	}
	return query, reasoning, nil
}

// reasoningParam reads the `reasoning` parameter, false when absent.
func reasoningParam(values url.Values) (bool, error) {
	value := strings.TrimSpace(values.Get("reasoning"))
	if value == "" {
		return false, nil
	}
	reasoning, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("the reasoning parameter must be true or false, not %q", value)
	}
	return reasoning, nil
}

var errUnsupportedMediaType = errors.New("POST requests must use application/sparql-query or application/x-www-form-urlencoded")

func requiredQuery(query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("SPARQL request is missing a query parameter or body")
	}
	return query, nil
}

func statusForQueryRequestError(err error) int {
	if errors.Is(err, errUnsupportedMediaType) {
		return http.StatusUnsupportedMediaType
	}
	return http.StatusBadRequest
}

func acceptsSPARQLJSON(accept string) bool {
	accept = strings.TrimSpace(accept)
	if accept == "" {
		return true
	}
	for _, part := range strings.Split(accept, ",") {
		mediaType := strings.ToLower(strings.TrimSpace(strings.Split(part, ";")[0]))
		switch mediaType {
		case "*/*", "application/*", sparqlResultsJSON, "application/json":
			return true
		}
	}
	return false
}

type sparqlJSONHead struct {
	Vars []string `json:"vars"`
}

type sparqlJSONBinding struct {
	Type  string `json:"type"`
	Value string `json:"value"`
	// Datatype is reported for the one literal shape that can be told apart by
	// its value alone: a geometry, which the table renders as WKT.
	Datatype string `json:"datatype,omitempty"`
}

type sparqlJSONResults struct {
	Bindings []map[string]sparqlJSONBinding `json:"bindings"`
}

type sparqlJSONResponse struct {
	Head    sparqlJSONHead    `json:"head"`
	Results sparqlJSONResults `json:"results"`
}

func sparqlJSONResult(result salsparql.Result) sparqlJSONResponse {
	bindings := make([]map[string]sparqlJSONBinding, 0, len(result.Rows))
	for _, row := range result.Rows {
		binding := make(map[string]sparqlJSONBinding)
		for i, name := range result.Header {
			if i >= len(row) {
				continue
			}
			binding[name] = sparqlJSONBinding{
				Type:     sparqlBindingType(row[i]),
				Value:    row[i],
				Datatype: sparqlBindingDatatype(row[i]),
			}
		}
		bindings = append(bindings, binding)
	}
	return sparqlJSONResponse{
		Head:    sparqlJSONHead{Vars: result.Header},
		Results: sparqlJSONResults{Bindings: bindings},
	}
}

// iriScheme matches a URI scheme followed by an authority, e.g. "salmodule://"
// or "oci://". The rows DuckDB returns are plain strings, so IRIs can only be
// told apart from literals by shape; requiring the "//" (or the urn: scheme,
// which never has one) keeps prose literals like "note: see below" out.
var iriScheme = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://`)

// wktLiteral matches the WKT DuckDB's ST_AsText writes, so that a client such as
// the bundled map can recognize a geometry binding without parsing every value.
var wktLiteral = regexp.MustCompile(`(?i)^(POINT|LINESTRING|POLYGON|MULTIPOINT|MULTILINESTRING|MULTIPOLYGON|GEOMETRYCOLLECTION)\s*(Z|M|ZM)?\s*(\(|EMPTY)`)

const wktLiteralDatatype = "http://www.opengis.net/ont/geosparql#wktLiteral"

func sparqlBindingDatatype(value string) string {
	if wktLiteral.MatchString(value) {
		return wktLiteralDatatype
	}
	return ""
}

func sparqlBindingType(value string) string {
	if strings.HasPrefix(value, "_:") {
		return "bnode"
	}
	if strings.HasPrefix(value, "urn:") || iriScheme.MatchString(value) {
		return "uri"
	}
	return "literal"
}
