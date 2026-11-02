package serve

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	salsparql "github.com/cgs-earth/sal/query/sparql"
	"github.com/stretchr/testify/require"
)

// ogcGet fetches a route of the OGC endpoint and decodes its JSON body into out,
// returning the response for its status and headers.
func ogcGet(t *testing.T, server *httptest.Server, path string, out any) *http.Response {
	t.Helper()
	resp, err := http.Get(server.URL + path)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	if out != nil {
		require.NoError(t, json.Unmarshal(body, out), string(body))
	}
	return resp
}

// ogcLinks indexes a links array by rel.
func ogcLinks(links []ogcLink) map[string]ogcLink {
	byRel := map[string]ogcLink{}
	for _, link := range links {
		byRel[link.Rel] = link
	}
	return byRel
}

func sampleFeaturePage() salsparql.FeaturePage {
	return salsparql.FeaturePage{Features: []salsparql.FeatureItem{{
		ID:         "https://example.org/places/1",
		Geometry:   json.RawMessage(`{"type":"Point","coordinates":[1.0,2.0]}`),
		Properties: json.RawMessage(`{"https://schema.org/name":"One"}`),
	}}}
}

func TestOGCLandingPageLinksTheConformanceCollectionsAndItems(t *testing.T) {
	runner := &endpointRunner{project: salsparql.ProjectInfo{Title: "Rivers", Description: "Every river.", TablePath: "/data/sal/triples"}}
	server := httptest.NewServer(NewEndpoint(runner, "", ""))
	defer server.Close()

	var landing struct {
		Title       string    `json:"title"`
		Description string    `json:"description"`
		Links       []ogcLink `json:"links"`
	}
	resp := ogcGet(t, server, "/ogc", &landing)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	require.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
	require.Equal(t, "Rivers", landing.Title)
	require.Equal(t, "Every river.", landing.Description)
	links := ogcLinks(landing.Links)
	require.Equal(t, server.URL+"/ogc", links["self"].Href)
	require.Equal(t, server.URL+"/ogc/conformance", links["conformance"].Href)
	require.Equal(t, server.URL+"/ogc/collections", links["data"].Href)
	require.Equal(t, server.URL+"/ogc/collections/triples/items", links["items"].Href)
	require.Equal(t, geoJSONType, links["items"].Type)
	require.NotContains(t, links, "alternate", "without the UI there is no HTML representation to point at")

	// the trailing slash form is the same document
	resp = ogcGet(t, server, "/ogc/", &landing)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, server.URL+"/ogc", ogcLinks(landing.Links)["self"].Href)
}

func TestOGCLandingPageNamesTheTableWhenTheProjectHasNoTitle(t *testing.T) {
	runner := &endpointRunner{project: salsparql.ProjectInfo{TablePath: "gs://bucket/sal/triples"}}
	server := httptest.NewServer(NewEndpoint(runner, "", ""))
	defer server.Close()

	var landing struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	ogcGet(t, server, "/ogc", &landing)
	require.Equal(t, "triples", landing.Title)
	require.NotEmpty(t, landing.Description)
}

func TestOGCLinksFollowTheForwardingHeaders(t *testing.T) {
	server := httptest.NewServer(NewEndpoint(&endpointRunner{}, "", ""))
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/ogc", nil)
	require.NoError(t, err)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "data.example.org")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	var landing struct {
		Links []ogcLink `json:"links"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&landing))
	require.NoError(t, resp.Body.Close())
	require.Equal(t, "https://data.example.org/ogc", ogcLinks(landing.Links)["self"].Href)
}

func TestOGCConformanceDeclaresCoreAndGeoJSON(t *testing.T) {
	server := httptest.NewServer(NewEndpoint(&endpointRunner{}, "", ""))
	defer server.Close()

	var conformance struct {
		ConformsTo []string `json:"conformsTo"`
	}
	resp := ogcGet(t, server, "/ogc/conformance", &conformance)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, []string{
		"http://www.opengis.net/spec/ogcapi-features-1/1.0/conf/core",
		"http://www.opengis.net/spec/ogcapi-features-1/1.0/conf/geojson",
	}, conformance.ConformsTo)
}

func TestOGCCollectionCarriesTheExtentAndTheItemsLink(t *testing.T) {
	runner := &endpointRunner{
		project: salsparql.ProjectInfo{Title: "Rivers"},
		extent:  salsparql.Feature{BBox: []float64{-90, 40, -89, 41}},
	}
	server := httptest.NewServer(NewEndpoint(runner, "", ""))
	defer server.Close()

	var listing struct {
		Links       []ogcLink       `json:"links"`
		Collections []ogcCollection `json:"collections"`
	}
	resp := ogcGet(t, server, "/ogc/collections", &listing)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, listing.Collections, 1)
	require.Equal(t, server.URL+"/ogc/collections", ogcLinks(listing.Links)["self"].Href)

	var collection ogcCollection
	resp = ogcGet(t, server, "/ogc/collections/triples", &collection)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, listing.Collections[0], collection)
	require.Equal(t, "triples", collection.ID)
	require.Equal(t, "Rivers", collection.Title)
	require.Equal(t, "feature", collection.ItemType)
	require.Equal(t, []string{crs84}, collection.CRS)
	require.NotNil(t, collection.Extent)
	require.Equal(t, [][]float64{{-90, 40, -89, 41}}, collection.Extent.Spatial.BBox)
	require.Equal(t, crs84, collection.Extent.Spatial.CRS)
	links := ogcLinks(collection.Links)
	require.Equal(t, server.URL+"/ogc/collections/triples", links["self"].Href)
	require.Equal(t, server.URL+"/ogc/collections/triples/items", links["items"].Href)
	require.Equal(t, geoJSONType, links["items"].Type)
	require.Zero(t, runner.extentSnapshot)
}

func TestOGCCollectionWithoutGeometriesHasNoExtent(t *testing.T) {
	server := httptest.NewServer(NewEndpoint(&endpointRunner{}, "", ""))
	defer server.Close()

	var collection ogcCollection
	ogcGet(t, server, "/ogc/collections/triples", &collection)
	require.Nil(t, collection.Extent)
}

func TestOGCCollectionAtASnapshotReadsTheExtentThereAndLinksItemsThere(t *testing.T) {
	runner := &endpointRunner{snapshots: []int64{42}, extent: salsparql.Feature{BBox: []float64{0, 0, 1, 1}}}
	server := httptest.NewServer(NewEndpoint(runner, "", ""))
	defer server.Close()

	var collection ogcCollection
	resp := ogcGet(t, server, "/ogc/collections/triples?snapshot=42", &collection)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, int64(42), runner.extentSnapshot)
	links := ogcLinks(collection.Links)
	require.Equal(t, server.URL+"/ogc/collections/triples?snapshot=42", links["self"].Href)
	require.Equal(t, server.URL+"/ogc/collections/triples/items?snapshot=42", links["items"].Href)

	var refusal struct {
		Code        string `json:"code"`
		Description string `json:"description"`
	}
	resp = ogcGet(t, server, "/ogc/collections/triples?snapshot=7", &refusal)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Equal(t, "the triples table has no snapshot 7", refusal.Description)
}

func TestOGCUnknownCollectionIs404(t *testing.T) {
	server := httptest.NewServer(NewEndpoint(&endpointRunner{}, "", ""))
	defer server.Close()

	for _, path := range []string{"/ogc/collections/rivers", "/ogc/collections/rivers/items", "/ogc/collections/rivers/items/1", "/ogc/nothing"} {
		var refusal struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		}
		resp := ogcGet(t, server, path, &refusal)
		require.Equal(t, http.StatusNotFound, resp.StatusCode, path)
		require.Equal(t, "Not Found", refusal.Code)
		require.NotEmpty(t, refusal.Description)
	}
}

func TestOGCItemsPassTheParametersToTheRunnerAndLinkThePages(t *testing.T) {
	runner := &endpointRunner{snapshots: []int64{42}, features: salsparql.FeaturePage{
		Features: sampleFeaturePage().Features,
		More:     true,
	}}
	server := httptest.NewServer(NewEndpoint(runner, "", ""))
	defer server.Close()

	query := "limit=1&offset=3&bbox=-90,40,-89,41&snapshot=42&type=https://schema.org/Place&properties=https://schema.org/name,%20https://schema.org/description,https://schema.org/name"
	var page ogcFeatureCollection
	resp := ogcGet(t, server, "/ogc/collections/triples/items?"+query, &page)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, geoJSONType, resp.Header.Get("Content-Type"))

	require.Equal(t, 1, runner.featureQuery.Limit)
	require.Equal(t, 3, runner.featureQuery.Offset)
	require.Equal(t, &salsparql.BBox{MinX: -90, MinY: 40, MaxX: -89, MaxY: 41}, runner.featureQuery.BBox)
	require.Equal(t, int64(42), runner.featureQuery.SnapshotID)
	require.Equal(t, "https://schema.org/Place", runner.featureQuery.Type)
	require.Equal(t, []string{"https://schema.org/name", "https://schema.org/description"}, runner.featureQuery.Properties, "properties are trimmed and deduplicated")
	require.Empty(t, runner.featureQuery.ID)

	require.Equal(t, "FeatureCollection", page.Type)
	require.Equal(t, 1, page.NumberReturned)
	require.NotEmpty(t, page.TimeStamp)
	require.Len(t, page.Features, 1)
	require.Equal(t, "Feature", page.Features[0].Type)
	require.Equal(t, "https://example.org/places/1", page.Features[0].ID)
	require.JSONEq(t, `{"type":"Point","coordinates":[1.0,2.0]}`, string(page.Features[0].Geometry))
	require.JSONEq(t, `{"https://schema.org/name":"One"}`, string(page.Features[0].Properties))

	links := ogcLinks(page.Links)
	require.Equal(t, server.URL+"/ogc/collections/triples", links["collection"].Href)
	self, err := url.Parse(links["self"].Href)
	require.NoError(t, err)
	require.Equal(t, "/ogc/collections/triples/items", self.Path)
	require.Equal(t, "3", self.Query().Get("offset"))
	next, err := url.Parse(links["next"].Href)
	require.NoError(t, err)
	require.Equal(t, "4", next.Query().Get("offset"))
	require.Equal(t, "1", next.Query().Get("limit"))
	require.Equal(t, "42", next.Query().Get("snapshot"), "the next page keeps every other parameter")
	require.Equal(t, "https://schema.org/name, https://schema.org/description,https://schema.org/name", next.Query().Get("properties"), "as given, since the runner is what trims and deduplicates")
	prev, err := url.Parse(links["prev"].Href)
	require.NoError(t, err)
	require.Equal(t, "2", prev.Query().Get("offset"))
}

func TestOGCItemsOnTheFirstAndLastPageLinkNeitherPrevNorNext(t *testing.T) {
	runner := &endpointRunner{features: sampleFeaturePage()}
	server := httptest.NewServer(NewEndpoint(runner, "", ""))
	defer server.Close()

	var page ogcFeatureCollection
	ogcGet(t, server, "/ogc/collections/triples/items", &page)
	require.Equal(t, salsparql.DefaultFeatureLimit, runner.featureQuery.Limit)
	links := ogcLinks(page.Links)
	require.Equal(t, server.URL+"/ogc/collections/triples/items", links["self"].Href)
	require.NotContains(t, links, "next")
	require.NotContains(t, links, "prev")
}

func TestOGCItemsRefuseBadParameters(t *testing.T) {
	server := httptest.NewServer(NewEndpoint(&endpointRunner{}, "", ""))
	defer server.Close()

	for query, message := range map[string]string{
		"limit=0":              "limit must be a number from 1 to 1000",
		"limit=1001":           "limit must be a number from 1 to 1000",
		"offset=-1":            "offset must be a number of features to skip",
		"bbox=1,2,3":           "minX,minY,maxX,maxY",
		"snapshot=latest":      `"latest" is not a snapshot ID`,
		"snapshot=0":           `"0" is not a snapshot ID`,
		"properties=name":      `property "name" is not an IRI`,
		"type=Place":           `type "Place" is not an IRI`,
		"f=xml":                `unknown format "xml"`,
		"datetime=2020":        `unknown query parameter "datetime"`,
		"properties=a:b&foo=1": `unknown query parameter "foo"`,
	} {
		var refusal struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		}
		resp := ogcGet(t, server, "/ogc/collections/triples/items?"+query, &refusal)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode, query)
		require.Equal(t, "Bad Request", refusal.Code, query)
		require.Contains(t, refusal.Description, message, query)
	}
}

func TestOGCItemsAtAMissingSnapshotIs404(t *testing.T) {
	server := httptest.NewServer(NewEndpoint(&endpointRunner{snapshots: []int64{42}}, "", ""))
	defer server.Close()

	var refusal struct {
		Description string `json:"description"`
	}
	resp := ogcGet(t, server, "/ogc/collections/triples/items?snapshot=41", &refusal)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Equal(t, "the triples table has no snapshot 41", refusal.Description)
}

func TestOGCItemReadsTheEscapedSubjectIRI(t *testing.T) {
	runner := &endpointRunner{snapshots: []int64{42}, features: sampleFeaturePage()}
	server := httptest.NewServer(NewEndpoint(runner, "", ""))
	defer server.Close()

	id := url.PathEscape("https://example.org/places/1")
	require.Contains(t, id, "%2F", "the slashes of the IRI are escaped into one path segment")
	var feature ogcFeature
	resp := ogcGet(t, server, "/ogc/collections/triples/items/"+id+"?snapshot=42&properties=https://schema.org/name", &feature)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, geoJSONType, resp.Header.Get("Content-Type"))
	require.Equal(t, "https://example.org/places/1", runner.featureQuery.ID)
	require.Equal(t, int64(42), runner.featureQuery.SnapshotID)
	require.Equal(t, []string{"https://schema.org/name"}, runner.featureQuery.Properties)

	require.Equal(t, "Feature", feature.Type)
	require.Equal(t, "https://example.org/places/1", feature.ID)
	links := ogcLinks(feature.Links)
	require.Equal(t, server.URL+"/ogc/collections/triples/items/"+id+"?snapshot=42&properties=https://schema.org/name", links["self"].Href)
	require.Equal(t, server.URL+"/ogc/collections/triples", links["collection"].Href)
}

func TestOGCItemThatDoesNotExistIs404(t *testing.T) {
	server := httptest.NewServer(NewEndpoint(&endpointRunner{snapshots: []int64{42}}, "", ""))
	defer server.Close()

	var refusal struct {
		Description string `json:"description"`
	}
	resp := ogcGet(t, server, "/ogc/collections/triples/items/"+url.PathEscape("https://example.org/nowhere")+"?snapshot=42", &refusal)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Equal(t, `no feature "https://example.org/nowhere": there is no subject with that IRI and a geometry at snapshot 42`, refusal.Description)
}

func TestOGCRoutesAnswerPreflightAndRefuseOtherMethods(t *testing.T) {
	server := httptest.NewServer(NewEndpoint(&endpointRunner{}, "", ""))
	defer server.Close()

	req, err := http.NewRequest(http.MethodOptions, server.URL+"/ogc/collections/triples/items", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))

	resp, err = http.Post(server.URL+"/ogc", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	require.Equal(t, "GET, HEAD, OPTIONS", resp.Header.Get("Allow"))

	resp, err = http.Head(server.URL + "/ogc/conformance")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestOGCHTMLIsRefusedWithoutTheUI(t *testing.T) {
	server := httptest.NewServer(NewEndpoint(&endpointRunner{}, "", ""))
	defer server.Close()

	var refusal struct {
		Description string `json:"description"`
	}
	resp := ogcGet(t, server, "/ogc?f=html", &refusal)
	require.Equal(t, http.StatusNotAcceptable, resp.StatusCode)
	require.Contains(t, refusal.Description, "--with-ui")
}

func TestOGCLandingPageWithUIServesTheTabToABrowserAndJSONToAClient(t *testing.T) {
	runner := &endpointUIRunner{}
	server := newUIServer(t, runner)
	defer server.Close()

	// a browser navigating to the tab gets the app
	req, err := http.NewRequest(http.MethodGet, server.URL+"/ogc", nil)
	require.NoError(t, err)
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Accept", "text/html")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "text/html")
	require.Contains(t, string(body), "<div id=\"root\">")

	// f=html is the same page whatever asked
	resp, err = http.Get(server.URL + "/ogc?f=html")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Contains(t, resp.Header.Get("Content-Type"), "text/html")

	// a client gets the landing page, which now also points at the tab
	var landing struct {
		Links []ogcLink `json:"links"`
	}
	resp = ogcGet(t, server, "/ogc", &landing)
	require.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	require.Equal(t, server.URL+"/ogc?f=html", ogcLinks(landing.Links)["alternate"].Href)

	// every route beneath it is the app for a browser too, which opens the
	// feature view on that request; f=json still asks for the JSON
	for _, path := range []string{"/ogc/collections", "/ogc/collections/triples", "/ogc/collections/triples/items?limit=5", "/ogc/collections/triples/items/" + url.PathEscape("https://example.org/places/1")} {
		req, err = http.NewRequest(http.MethodGet, server.URL+path, nil)
		require.NoError(t, err)
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		req.Header.Set("Accept", "text/html")
		resp, err = http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Contains(t, resp.Header.Get("Content-Type"), "text/html", path)
	}
	req, err = http.NewRequest(http.MethodGet, server.URL+"/ogc/collections/triples/items?f=json", nil)
	require.NoError(t, err)
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Accept", "text/html")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, geoJSONType, resp.Header.Get("Content-Type"))

	// a client is offered the HTML reading of every document
	runner.features = sampleFeaturePage()
	var page ogcFeatureCollection
	ogcGet(t, server, "/ogc/collections/triples/items?limit=5", &page)
	require.Equal(t, server.URL+"/ogc/collections/triples/items?limit=5&f=html", ogcLinks(page.Links)["alternate"].Href)
	var feature ogcFeature
	ogcGet(t, server, "/ogc/collections/triples/items/"+url.PathEscape("https://example.org/places/1"), &feature)
	require.Equal(t, server.URL+"/ogc/collections/triples/items/https:%2F%2Fexample.org%2Fplaces%2F1?f=html", ogcLinks(feature.Links)["alternate"].Href)
	var collection ogcCollection
	ogcGet(t, server, "/ogc/collections/triples", &collection)
	require.Equal(t, server.URL+"/ogc/collections/triples?f=html", ogcLinks(collection.Links)["alternate"].Href)
}

func TestOGCDocumentsWithoutTheUIOfferNoHTMLAlternate(t *testing.T) {
	server := httptest.NewServer(NewEndpoint(&endpointRunner{features: sampleFeaturePage()}, "", ""))
	defer server.Close()

	var page ogcFeatureCollection
	ogcGet(t, server, "/ogc/collections/triples/items", &page)
	require.NotContains(t, ogcLinks(page.Links), "alternate")
	var collection ogcCollection
	ogcGet(t, server, "/ogc/collections/triples", &collection)
	require.NotContains(t, ogcLinks(collection.Links), "alternate")
}
