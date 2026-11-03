package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	salsparql "github.com/cgs-earth/sal/query/sparql"
)

// The OGC API Features endpoint sits under /ogc: a landing page, the
// conformance declaration, one collection named triples, and its items. A
// feature is an RDF subject with a geometry, identified by its subject IRI,
// so the same feature can be read at any snapshot of the table through the
// snapshot parameter; the properties parameter names the predicates each
// feature carries as GeoJSON properties, keyed by their full IRI.
const (
	ogcRoot         = "/ogc"
	ogcCollectionID = "triples"
	geoJSONType     = "application/geo+json"
	crs84           = "http://www.opengis.net/def/crs/OGC/1.3/CRS84"
)

// ogcConformance lists the conformance classes the endpoint implements: Core
// and GeoJSON of OGC API Features Part 1. There is no OpenAPI document, and
// HTML is only the UI's tab, so neither of those classes is claimed.
var ogcConformance = []string{
	"http://www.opengis.net/spec/ogcapi-features-1/1.0/conf/core",
	"http://www.opengis.net/spec/ogcapi-features-1/1.0/conf/geojson",
}

type ogcLink struct {
	Href  string `json:"href"`
	Rel   string `json:"rel"`
	Type  string `json:"type,omitempty"`
	Title string `json:"title,omitempty"`
}

type ogcCollection struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	ItemType    string     `json:"itemType"`
	CRS         []string   `json:"crs"`
	Extent      *ogcExtent `json:"extent,omitempty"`
	Links       []ogcLink  `json:"links"`
}

type ogcExtent struct {
	Spatial struct {
		BBox [][]float64 `json:"bbox"`
		CRS  string      `json:"crs"`
	} `json:"spatial"`
}

type ogcFeature struct {
	Type       string          `json:"type"`
	ID         string          `json:"id"`
	Geometry   json.RawMessage `json:"geometry"`
	Properties json.RawMessage `json:"properties"`
	Links      []ogcLink       `json:"links,omitempty"`
}

type ogcFeatureCollection struct {
	Type           string       `json:"type"`
	TimeStamp      string       `json:"timeStamp"`
	NumberReturned int          `json:"numberReturned"`
	Features       []ogcFeature `json:"features"`
	Links          []ogcLink    `json:"links"`
}

// ogcError is a request the endpoint refuses, answered with its status and a
// body in the shape OGC API suggests, `{"code", "description"}`.
type ogcError struct {
	status  int
	message string
}

func (e ogcError) Error() string {
	return e.message
}

func badRequest(format string, args ...any) ogcError {
	return ogcError{status: http.StatusBadRequest, message: fmt.Sprintf(format, args...)}
}

// featuresAPI is the handler set of the /ogc routes. ui answers a browser's
// navigation to the landing page with the UI's tab, and is nil without
// --with-ui, where every request gets JSON.
type featuresAPI struct {
	runner salsparql.FeatureRunner
	ui     http.Handler
}

// ogcRoute answers one route: the body and its content type, or an error the
// wrapper turns into a status.
type ogcRoute func(r *http.Request) (body any, contentType string, err error)

func (api featuresAPI) register(mux *http.ServeMux) {
	landing := api.route(api.landing, "f")
	mux.Handle(ogcRoot, landing)
	mux.Handle(ogcRoot+"/{$}", landing)
	mux.Handle(ogcRoot+"/conformance", api.route(api.conformance, "f"))
	mux.Handle(ogcRoot+"/collections", api.route(api.collections, "f"))
	mux.Handle(ogcRoot+"/collections/{collection}", api.route(api.collection, "f", "snapshot"))
	mux.Handle(ogcRoot+"/collections/{collection}/items", api.route(api.items, "f", "limit", "offset", "bbox", "snapshot", "properties", "type"))
	mux.Handle(ogcRoot+"/collections/{collection}/items/{id}", api.route(api.item, "f", "snapshot", "properties"))
	// anything else under the root is not a route of the API
	mux.Handle(ogcRoot+"/", api.route(func(r *http.Request) (any, string, error) {
		return nil, "", ogcError{status: http.StatusNotFound, message: fmt.Sprintf("%s is not a route of the OGC API Features endpoint at %s", r.URL.Path, ogcRoot)}
	}))
}

// route wraps a handler with what every OGC route shares: open CORS, GET and
// HEAD only, a 400 for a query parameter the route does not define, as the
// Core conformance class requires, the f parameter choosing the format, and
// the error body.
//
// The HTML representation of every route is the UI, where there is one: a
// browser navigating to the landing page, the collection, a page of items,
// or one item gets the app, which opens the Geospatial tab's feature view on that
// request and runs it, so an item URL pasted into a browser shows the
// feature with its properties and map. f=html asks for that whatever the
// client, f=json (or geojson) asks for JSON whatever the client, and a
// client that says neither is judged by what it sent, as /sparql is.
func (api featuresAPI) route(handle ogcRoute, known ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Accept")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD, OPTIONS")
			writeOGCError(w, ogcError{status: http.StatusMethodNotAllowed, message: "the OGC API Features endpoint only supports GET requests"})
			return
		}
		if api.ui != nil && isBrowserNavigation(r) && !wantsJSON(r) {
			api.ui.ServeHTTP(w, r)
			return
		}
		for name := range r.URL.Query() {
			if !slices.Contains(known, name) {
				writeOGCError(w, badRequest("unknown query parameter %q; %s accepts %s", name, r.URL.Path, strings.Join(known, ", ")))
				return
			}
		}
		switch format := r.URL.Query().Get("f"); format {
		case "", "json", "geojson":
		case "html":
			if api.ui == nil {
				writeOGCError(w, ogcError{status: http.StatusNotAcceptable, message: "the HTML representation is served by `sal serve --with-ui` only"})
				return
			}
			api.ui.ServeHTTP(w, r)
			return
		default:
			writeOGCError(w, badRequest("unknown format %q; f takes json, geojson, or html", format))
			return
		}
		body, contentType, err := handle(r)
		if err != nil {
			var refusal ogcError
			var noSnapshot salsparql.NoSnapshotError
			switch {
			case errors.As(err, &refusal):
				writeOGCError(w, refusal)
			case errors.As(err, &noSnapshot):
				writeOGCError(w, ogcError{status: http.StatusNotFound, message: err.Error()})
			default:
				writeOGCError(w, ogcError{status: http.StatusBadRequest, message: err.Error()})
			}
			return
		}
		w.Header().Set("Content-Type", contentType)
		if err := json.NewEncoder(w).Encode(body); err != nil {
			slog.Error("failed to write an OGC API Features response", "error", err)
		}
	})
}

// wantsJSON reports whether the f parameter asks for JSON outright.
func wantsJSON(r *http.Request) bool {
	format := r.URL.Query().Get("f")
	return format == "json" || format == "geojson"
}

// htmlAlternate is the link to the UI's reading of a document, for the
// clients that offer one; nil without the UI.
func (api featuresAPI) htmlAlternate(href string, title string) []ogcLink {
	if api.ui == nil {
		return nil
	}
	separator := "?"
	if strings.Contains(href, "?") {
		separator = "&"
	}
	return []ogcLink{{Href: href + separator + "f=html", Rel: "alternate", Type: "text/html", Title: title}}
}

func writeOGCError(w http.ResponseWriter, refusal ogcError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(refusal.status)
	body := struct {
		Code        string `json:"code"`
		Description string `json:"description"`
	}{Code: http.StatusText(refusal.status), Description: refusal.message}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("failed to write an OGC API Features error", "error", err)
	}
}

// ogcBase is the absolute URL of the endpoint's root as the client reached
// it, so that every link in a response is one the client can follow. A proxy
// in front of the server, such as Cloud Run's, says what the client used in
// the forwarding headers.
func ogcBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}
	host := r.Host
	if forwarded := r.Header.Get("X-Forwarded-Host"); forwarded != "" {
		host = forwarded
	}
	return scheme + "://" + host + ogcRoot
}

// title names the data product: the project node's title, else the table's
// own directory name, which is what the UI's landing card falls back to.
func (api featuresAPI) title() (title string, description string) {
	project := api.runner.Project()
	title = project.Title
	if title == "" {
		title = path.Base(strings.TrimRight(strings.ReplaceAll(project.TablePath, "\\", "/"), "/"))
	}
	if title == "" || title == "." || title == "/" {
		title = "SAL data product"
	}
	description = project.Description
	if description == "" {
		description = "The triples table a SAL project built, served as OGC API Features."
	}
	return title, description
}

func (api featuresAPI) landing(r *http.Request) (any, string, error) {
	base := ogcBase(r)
	title, description := api.title()
	links := []ogcLink{
		{Href: base, Rel: "self", Type: "application/json", Title: "This document"},
	}
	links = append(links, api.htmlAlternate(base, "The feature view of the SAL UI's Geospatial tab")...)
	links = append(links,
		ogcLink{Href: base + "/conformance", Rel: "conformance", Type: "application/json", Title: "The conformance classes the endpoint implements"},
		ogcLink{Href: base + "/collections", Rel: "data", Type: "application/json", Title: "The feature collections"},
		ogcLink{Href: base + "/collections/" + ogcCollectionID + "/items", Rel: "items", Type: geoJSONType, Title: "The features of the triples collection"},
	)
	return struct {
		Title       string    `json:"title"`
		Description string    `json:"description"`
		Links       []ogcLink `json:"links"`
	}{Title: title, Description: description, Links: links}, "application/json", nil
}

func (api featuresAPI) conformance(*http.Request) (any, string, error) {
	return struct {
		ConformsTo []string `json:"conformsTo"`
	}{ConformsTo: ogcConformance}, "application/json", nil
}

func (api featuresAPI) collections(r *http.Request) (any, string, error) {
	collection, err := api.describeCollection(r, 0)
	if err != nil {
		return nil, "", err
	}
	base := ogcBase(r)
	return struct {
		Links       []ogcLink       `json:"links"`
		Collections []ogcCollection `json:"collections"`
	}{
		Links:       []ogcLink{{Href: base + "/collections", Rel: "self", Type: "application/json", Title: "This document"}},
		Collections: []ogcCollection{collection},
	}, "application/json", nil
}

func (api featuresAPI) collection(r *http.Request) (any, string, error) {
	if r.PathValue("collection") != ogcCollectionID {
		return nil, "", unknownCollection(r.PathValue("collection"))
	}
	snapshot, err := snapshotParam(r)
	if err != nil {
		return nil, "", err
	}
	collection, err := api.describeCollection(r, snapshot)
	if err != nil {
		return nil, "", err
	}
	return collection, "application/json", nil
}

func unknownCollection(name string) error {
	return ogcError{status: http.StatusNotFound, message: fmt.Sprintf("there is no collection %q; the one collection is %s", name, ogcCollectionID)}
}

// describeCollection builds the collection document, with the extent of the
// table's geometries at the snapshot, or no extent for a table without any.
func (api featuresAPI) describeCollection(r *http.Request, snapshot int64) (ogcCollection, error) {
	extent, err := api.runner.FeaturesExtent(r.Context(), snapshot)
	if err != nil {
		return ogcCollection{}, err
	}
	title, _ := api.title()
	base := ogcBase(r)
	suffix := ""
	if snapshot != 0 {
		suffix = "?snapshot=" + strconv.FormatInt(snapshot, 10)
	}
	collection := ogcCollection{
		ID:          ogcCollectionID,
		Title:       title,
		Description: "Every RDF subject of the triples table that has a geometry, one feature per subject under its IRI, with the predicates the properties parameter names as its properties.",
		ItemType:    "feature",
		CRS:         []string{crs84},
		Links: []ogcLink{
			{Href: base + "/collections/" + ogcCollectionID + suffix, Rel: "self", Type: "application/json", Title: "This document"},
			{Href: base + "/collections/" + ogcCollectionID + "/items" + suffix, Rel: "items", Type: geoJSONType, Title: "The features"},
		},
	}
	collection.Links = append(collection.Links, api.htmlAlternate(base+"/collections/"+ogcCollectionID+suffix, "The collection in the SAL UI")...)
	if len(extent.BBox) == 4 {
		collection.Extent = &ogcExtent{}
		collection.Extent.Spatial.BBox = [][]float64{extent.BBox}
		collection.Extent.Spatial.CRS = crs84
	}
	return collection, nil
}

// featureQuery reads the parameters items and a single item share.
func featureQuery(r *http.Request) (salsparql.FeatureQuery, error) {
	query := salsparql.FeatureQuery{}
	snapshot, err := snapshotParam(r)
	if err != nil {
		return query, err
	}
	query.SnapshotID = snapshot
	for _, property := range strings.Split(r.URL.Query().Get("properties"), ",") {
		property = strings.TrimSpace(property)
		if property == "" || slices.Contains(query.Properties, property) {
			continue
		}
		if !strings.Contains(property, ":") {
			return query, badRequest("property %q is not an IRI; properties takes a comma separated list of predicate IRIs", property)
		}
		query.Properties = append(query.Properties, property)
	}
	if len(query.Properties) > salsparql.MaxFeatureProperties {
		return query, badRequest("at most %d properties can be requested, got %d", salsparql.MaxFeatureProperties, len(query.Properties))
	}
	return query, nil
}

func snapshotParam(r *http.Request) (int64, error) {
	value := strings.TrimSpace(r.URL.Query().Get("snapshot"))
	if value == "" {
		return 0, nil
	}
	snapshot, err := strconv.ParseInt(value, 10, 64)
	if err != nil || snapshot <= 0 {
		return 0, badRequest("%q is not a snapshot ID; snapshot takes the ID of an Iceberg snapshot of the table", value)
	}
	return snapshot, nil
}

func (api featuresAPI) items(r *http.Request) (any, string, error) {
	if r.PathValue("collection") != ogcCollectionID {
		return nil, "", unknownCollection(r.PathValue("collection"))
	}
	query, err := featureQuery(r)
	if err != nil {
		return nil, "", err
	}
	params := r.URL.Query()
	query.Limit = salsparql.DefaultFeatureLimit
	if value := strings.TrimSpace(params.Get("limit")); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 1 || limit > salsparql.MaxGeometries {
			return nil, "", badRequest("limit must be a number from 1 to %d, got %q", salsparql.MaxGeometries, value)
		}
		query.Limit = limit
	}
	if value := strings.TrimSpace(params.Get("offset")); value != "" {
		offset, err := strconv.Atoi(value)
		if err != nil || offset < 0 {
			return nil, "", badRequest("offset must be a number of features to skip, got %q", value)
		}
		query.Offset = offset
	}
	if value := strings.TrimSpace(params.Get("bbox")); value != "" {
		box, err := salsparql.ParseBBox(value)
		if err != nil {
			return nil, "", badRequest("%s", err.Error())
		}
		query.BBox = &box
	}
	if value := strings.TrimSpace(params.Get("type")); value != "" {
		if !strings.Contains(value, ":") {
			return nil, "", badRequest("type %q is not an IRI; type takes the IRI of a class", value)
		}
		query.Type = value
	}

	page, err := api.runner.Features(r.Context(), query)
	if err != nil {
		return nil, "", err
	}
	base := ogcBase(r)
	itemsURL := base + "/collections/" + ogcCollectionID + "/items"
	links := []ogcLink{
		{Href: pageLink(itemsURL, params, nil), Rel: "self", Type: geoJSONType, Title: "This page"},
		{Href: base + "/collections/" + ogcCollectionID, Rel: "collection", Type: "application/json", Title: "The collection"},
	}
	links = append(links, api.htmlAlternate(pageLink(itemsURL, params, nil), "This page in the SAL UI, run as a query of its feature view")...)
	if page.More {
		links = append(links, ogcLink{Href: pageLink(itemsURL, params, &[2]int{query.Offset + query.Limit, query.Limit}), Rel: "next", Type: geoJSONType, Title: "The next page"})
	}
	if query.Offset > 0 {
		links = append(links, ogcLink{Href: pageLink(itemsURL, params, &[2]int{max(query.Offset-query.Limit, 0), query.Limit}), Rel: "prev", Type: geoJSONType, Title: "The previous page"})
	}
	features := make([]ogcFeature, len(page.Features))
	for i, feature := range page.Features {
		features[i] = ogcFeature{Type: "Feature", ID: feature.ID, Geometry: feature.Geometry, Properties: feature.Properties}
	}
	return ogcFeatureCollection{
		Type:           "FeatureCollection",
		TimeStamp:      time.Now().UTC().Format(time.RFC3339),
		NumberReturned: len(features),
		Features:       features,
		Links:          links,
	}, geoJSONType, nil
}

// pageLink is the items URL with the request's own parameters, at another
// offset and limit when paging names one.
func pageLink(itemsURL string, params url.Values, paging *[2]int) string {
	values := url.Values{}
	for name, list := range params {
		values[name] = list
	}
	if paging != nil {
		values.Set("offset", strconv.Itoa(paging[0]))
		values.Set("limit", strconv.Itoa(paging[1]))
	}
	if len(values) == 0 {
		return itemsURL
	}
	return itemsURL + "?" + values.Encode()
}

func (api featuresAPI) item(r *http.Request) (any, string, error) {
	if r.PathValue("collection") != ogcCollectionID {
		return nil, "", unknownCollection(r.PathValue("collection"))
	}
	query, err := featureQuery(r)
	if err != nil {
		return nil, "", err
	}
	// the wildcard matched the escaped path segment and PathValue unescaped
	// it, which is how a subject IRI with slashes in it fits one segment
	query.ID = r.PathValue("id")
	page, err := api.runner.Features(r.Context(), query)
	if err != nil {
		return nil, "", err
	}
	if len(page.Features) == 0 {
		return nil, "", ogcError{status: http.StatusNotFound, message: fmt.Sprintf("no feature %q: there is no subject with that IRI and a geometry%s", query.ID, snapshotClause(query.SnapshotID))}
	}
	base := ogcBase(r)
	suffix := ""
	if r.URL.RawQuery != "" {
		suffix = "?" + r.URL.RawQuery
	}
	feature := page.Features[0]
	self := base + "/collections/" + ogcCollectionID + "/items/" + url.PathEscape(feature.ID) + suffix
	links := []ogcLink{
		{Href: self, Rel: "self", Type: geoJSONType, Title: "This feature"},
		{Href: base + "/collections/" + ogcCollectionID, Rel: "collection", Type: "application/json", Title: "The collection"},
	}
	links = append(links, api.htmlAlternate(self, "This feature in the SAL UI, with its properties and on a map")...)
	return ogcFeature{
		Type:       "Feature",
		ID:         feature.ID,
		Geometry:   feature.Geometry,
		Properties: feature.Properties,
		Links:      links,
	}, geoJSONType, nil
}

func snapshotClause(snapshot int64) string {
	if snapshot == 0 {
		return ""
	}
	return fmt.Sprintf(" at snapshot %d", snapshot)
}
