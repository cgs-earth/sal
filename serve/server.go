package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/cgs-earth/sal/pkg"
	salsparql "github.com/cgs-earth/sal/query/sparql"
	"github.com/cgs-earth/sal/salmodule"
)

const sparqlResultsJSON = "application/sparql-results+json"

// maxUIRows bounds how many rows of a SQL result are sent to the browser.
const maxUIRows = 1000

// UIRunner is the query surface the bundled web UI needs from the DuckDB backend.
type UIRunner interface {
	salsparql.Runner
	salsparql.GeometryRunner
	salsparql.SQLRunner
	salsparql.SQLTranslator
	salsparql.StatsRunner
}

// Serve starts a read-only SPARQL Protocol HTTP endpoint backed by DuckDB, plus
// the /blobs endpoint serving the vocabulary and imported ontology documents
// pinned under blobDir.
func Serve(ctx context.Context, addr string, runner salsparql.DuckDBRunner, blobDir string, withUI bool) error {
	handler := NewEndpoint(runner, blobDir)
	if withUI {
		ui, err := NewEndpointWithUI(runner, blobDir)
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
		pkg.Infof("Serving the SAL UI at http://localhost%s/ and SPARQL endpoint at http://localhost%s/sparql\n", addr, addr)
	} else {
		pkg.Infof("Serving SPARQL endpoint at http://localhost%s/sparql\n", addr)
	}
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// NewEndpoint returns an HTTP handler for the SPARQL Protocol query operation,
// plus the /blobs endpoint serving the vocabulary and imported ontology
// documents pinned under blobDir.
func NewEndpoint(runner salsparql.Runner, blobDir string) http.Handler {
	mux := http.NewServeMux()
	handler := sparqlHandler{runner: runner}
	mux.Handle("/", handler)
	mux.Handle("/sparql", handler)
	mux.Handle("/blobs/", blobHandler{dir: blobDir})
	return mux
}

// NewEndpointWithUI returns an HTTP handler serving the embedded SAL UI at / along
// with the SPARQL endpoint, the JSON APIs the UI reads, and the /blobs endpoint
// serving the vocabulary and imported ontology documents pinned under blobDir.
func NewEndpointWithUI(runner UIRunner, blobDir string) (http.Handler, error) {
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
	mux.Handle("/", ui)
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
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("parse SPARQL translate request: %v", err))
		return
	}
	if strings.TrimSpace(request.Query) == "" {
		writeJSONError(w, http.StatusBadRequest, "SPARQL translate request is missing a query field")
		return
	}

	sql, err := h.translator.Translate(request.Query)
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

	query, err := queryFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), statusForQueryRequestError(err))
		return
	}
	result, err := h.runner.Run(r.Context(), query)
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

func queryFromRequest(r *http.Request) (string, error) {
	switch r.Method {
	case http.MethodGet:
		return requiredQuery(r.URL.Query().Get("query"))
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
				return "", fmt.Errorf("read SPARQL query body: %w", err)
			}
			return requiredQuery(string(b))
		case "application/x-www-form-urlencoded", "":
			if err := r.ParseForm(); err != nil {
				return "", fmt.Errorf("parse SPARQL form request: %w", err)
			}
			return requiredQuery(r.Form.Get("query"))
		default:
			return "", errUnsupportedMediaType
		}
	default:
		return "", fmt.Errorf("unsupported method %s", r.Method)
	}
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
