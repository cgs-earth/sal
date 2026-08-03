package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/cgs-earth/sal/pkg"
	salsparql "github.com/cgs-earth/sal/query/sparql"
)

const sparqlResultsJSON = "application/sparql-results+json"

// maxUIRows bounds how many rows of a SQL result are sent to the browser.
const maxUIRows = 1000

// UIRunner is the query surface the bundled web UI needs from the DuckDB backend.
type UIRunner interface {
	salsparql.Runner
	salsparql.GeometryRunner
	salsparql.SQLRunner
	salsparql.StatsRunner
}

// Serve starts a read-only SPARQL Protocol HTTP endpoint backed by DuckDB.
func Serve(ctx context.Context, addr string, tablePath string, layout salsparql.ObjectLayout, withUI bool) error {
	runner := salsparql.DuckDBRunner{
		TablePath: tablePath,
		Layout:    layout,
	}
	handler := NewEndpoint(runner)
	if withUI {
		ui, err := NewEndpointWithUI(runner)
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

// NewEndpoint returns an HTTP handler for the SPARQL Protocol query operation.
func NewEndpoint(runner salsparql.Runner) http.Handler {
	mux := http.NewServeMux()
	handler := sparqlHandler{runner: runner}
	mux.Handle("/", handler)
	mux.Handle("/sparql", handler)
	return mux
}

// NewEndpointWithUI returns an HTTP handler serving the embedded SAL UI at / along
// with the SPARQL endpoint and the JSON APIs the UI reads.
func NewEndpointWithUI(runner UIRunner) (http.Handler, error) {
	ui, err := uiHandler()
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("/sparql", sparqlHandler{runner: runner})
	mux.Handle("/geometries", geometryHandler{runner: runner})
	mux.Handle("/api/sql", sqlHandler{runner: runner})
	mux.Handle("/api/stats", statsHandler{runner: runner})
	mux.Handle("/", ui)
	return mux, nil
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

func (h geometryHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Accept")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, OPTIONS")
		http.Error(w, "geometry endpoint only supports GET requests", http.StatusMethodNotAllowed)
		return
	}
	limit := intQueryParam(r, "limit", 100)
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	offset := intQueryParam(r, "offset", 0)
	if offset < 0 {
		offset = 0
	}

	collection, err := h.runner.Geometries(r.Context(), limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/geo+json")
	if err := json.NewEncoder(w).Encode(collection); err != nil {
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
				Type:  sparqlBindingType(row[i]),
				Value: row[i],
			}
		}
		bindings = append(bindings, binding)
	}
	return sparqlJSONResponse{
		Head:    sparqlJSONHead{Vars: result.Header},
		Results: sparqlJSONResults{Bindings: bindings},
	}
}

func sparqlBindingType(value string) string {
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return "uri"
	}
	if strings.HasPrefix(value, "_:") {
		return "bnode"
	}
	return "literal"
}
