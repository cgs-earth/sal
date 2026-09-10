package build

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/apache/iceberg-go/catalog"
	"github.com/apache/iceberg-go/catalog/hadoop"
	"github.com/apache/iceberg-go/table"
	"github.com/cgs-earth/sal/build/load"
	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
)

const wktLiteralDatatype = "http://www.opengis.net/ont/geosparql#wktLiteral"

var stacTestProject = StacProject{
	Name:   "widgets",
	Owner:  "cgs-earth",
	Remote: "https://github.com/cgs-earth/widgets",
	Title:  "The Widgets",
}

// writeStacTestTable writes graph into a triples table in a temporary
// warehouse under the widgets namespace and loads it back the way build does.
func writeStacTestTable(t *testing.T, graph *rdflibgo.Graph) *table.Table {
	t.Helper()
	ctx := context.Background()
	cfg := &load.LoadConfig{
		BatchSize:          10,
		ParquetCompression: "snappy",
		Warehouse:          t.TempDir(),
		Namespace:          stacTestProject.Name,
	}
	require.NoError(t, load.WriteGraphToIceberg(ctx, graph, nil, cfg, map[string]string{"sal.hash": "abc"}))

	cat, err := hadoop.NewCatalog("local-catalog", cfg.Warehouse, nil)
	require.NoError(t, err)
	tbl, err := cat.LoadTable(ctx, catalog.ToIdentifier(cfg.Namespace, "triples"))
	require.NoError(t, err)
	return tbl
}

func stacTestGraph(withGeometries bool) *rdflibgo.Graph {
	graph := rdflibgo.NewGraph()
	widget := rdflibgo.NewURIRefUnsafe("https://example.com/widgets/1")
	graph.Add(widget, rdflibgo.NewURIRefUnsafe("https://schema.org/name"), rdflibgo.NewLiteral("A widget"))
	if withGeometries {
		asWKT := rdflibgo.NewURIRefUnsafe("http://www.opengis.net/ont/geosparql#asWKT")
		graph.Add(widget, asWKT, rdflibgo.NewLiteral("POINT (1 2)", rdflibgo.WithDatatype(rdflibgo.NewURIRefUnsafe(wktLiteralDatatype))))
		graph.Add(rdflibgo.NewURIRefUnsafe("https://example.com/widgets/2"), asWKT, rdflibgo.NewLiteral("LINESTRING (3 4, -5 6)", rdflibgo.WithDatatype(rdflibgo.NewURIRefUnsafe(wktLiteralDatatype))))
	}
	return graph
}

func readStacJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	var document map[string]any
	require.NoError(t, json.Unmarshal(content, &document))
	return document
}

func TestWriteStacCatalogDescribesTheTriplesTable(t *testing.T) {
	tbl := writeStacTestTable(t, stacTestGraph(true))
	dir := filepath.Join(t.TempDir(), "stac")

	require.NoError(t, writeStacCatalog(context.Background(), tbl, dir, stacTestProject))

	catalog := readStacJSON(t, filepath.Join(dir, "catalog.json"))
	require.Equal(t, "Catalog", catalog["type"])
	require.Equal(t, "widgets", catalog["id"])
	require.Equal(t, "The Widgets", catalog["title"])
	require.Contains(t, catalog["description"], "https://github.com/cgs-earth/widgets")
	links := catalog["links"].([]any)
	require.Len(t, links, 3)
	child := links[2].(map[string]any)
	require.Equal(t, "child", child["rel"])
	require.Equal(t, "./triples/collection.json", child["href"])

	collection := readStacJSON(t, filepath.Join(dir, "triples", "collection.json"))
	require.Equal(t, "Collection", collection["type"])
	require.Equal(t, "1.1.0", collection["stac_version"])
	require.Equal(t, "triples", collection["id"])
	require.Equal(t, "The Widgets", collection["title"])
	require.Contains(t, collection["stac_extensions"], StacIcebergExtension)
	require.Contains(t, collection["stac_extensions"], "https://stac-extensions.github.io/table/v1.2.0/schema.json")
	require.Equal(t, "other", collection["license"])
	providers := collection["providers"].([]any)
	require.Equal(t, "cgs-earth", providers[0].(map[string]any)["name"])
	require.Equal(t, "https://github.com/cgs-earth/widgets", providers[0].(map[string]any)["url"])

	// the extent covers both geometries, and the row count every triple
	extent := collection["extent"].(map[string]any)
	bbox := extent["spatial"].(map[string]any)["bbox"].([]any)[0]
	require.Equal(t, []any{-5.0, 2.0, 3.0, 6.0}, bbox)
	interval := extent["temporal"].(map[string]any)["interval"].([]any)[0].([]any)
	require.Len(t, interval, 2)
	require.NotNil(t, interval[0])
	require.NotNil(t, interval[1])
	require.Equal(t, 3.0, collection["table:row_count"])
	require.Equal(t, "object_geometry", collection["table:primary_geometry"])
	require.Equal(t, "OGC:CRS84", collection["proj:code"])

	columns := map[string]string{}
	for _, column := range collection["table:columns"].([]any) {
		columns[column.(map[string]any)["name"].(string)] = column.(map[string]any)["type"].(string)
	}
	require.Equal(t, "geometry", columns["object_geometry"])
	require.Equal(t, "int64", columns["object_integer"])
	require.Equal(t, "int32", columns["object_byte"])
	require.Equal(t, "string", columns["subject"])
	require.Equal(t, "timestamp", columns["object_time"])
	require.Equal(t, "string", columns["vocabulary"])

	// the Iceberg fields are what a reader needs to open the table
	require.Equal(t, "static", collection["iceberg:catalog_type"])
	require.Equal(t, "none", collection["iceberg:authorization_type"])
	require.Equal(t, "widgets.triples", collection["iceberg:table_id"])
	require.Equal(t, 3.0, collection["iceberg:format_version"])
	require.Equal(t, strconv.FormatInt(tbl.CurrentSnapshot().SnapshotID, 10), collection["iceberg:current_snapshot_id"])
	metadataLocation := collection["iceberg:metadata_location"].(string)
	require.True(t, strings.HasPrefix(metadataLocation, "file://"), metadataLocation)
	require.True(t, strings.HasSuffix(metadataLocation, filepath.Base(tbl.MetadataLocation())), metadataLocation)
	require.Equal(t, []any{map[string]any{
		"name":      "predicate_partition",
		"transform": "truncate[20]",
		"source-id": 2.0,
		"field-id":  1000.0,
	}}, collection["iceberg:partition_spec"])

	asset := collection["assets"].(map[string]any)["iceberg"].(map[string]any)
	require.Equal(t, metadataLocation, asset["href"])
	require.Equal(t, "application/vnd.apache.iceberg+json", asset["type"])
	require.Equal(t, []any{"data", "metadata"}, asset["roles"])

	// the links are relative, so the catalog reads the same on disk and served
	hrefs := map[string]string{}
	for _, link := range collection["links"].([]any) {
		hrefs[link.(map[string]any)["rel"].(string)] = link.(map[string]any)["href"].(string)
	}
	require.Equal(t, map[string]string{"root": "../catalog.json", "parent": "../catalog.json", "self": "./collection.json"}, hrefs)
}

func TestWriteStacCatalogUsesTheWorldExtentWithoutGeometries(t *testing.T) {
	tbl := writeStacTestTable(t, stacTestGraph(false))
	dir := filepath.Join(t.TempDir(), "stac")

	require.NoError(t, writeStacCatalog(context.Background(), tbl, dir, stacTestProject))

	collection := readStacJSON(t, filepath.Join(dir, "triples", "collection.json"))
	bbox := collection["extent"].(map[string]any)["spatial"].(map[string]any)["bbox"].([]any)[0]
	require.Equal(t, []any{-180.0, -90.0, 180.0, 90.0}, bbox)
	require.Equal(t, 1.0, collection["table:row_count"])
}

func TestWriteStacCatalogReplacesAnEarlierCatalog(t *testing.T) {
	tbl := writeStacTestTable(t, stacTestGraph(false))
	dir := filepath.Join(t.TempDir(), "stac")
	stale := filepath.Join(dir, "old-table", "collection.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(stale), 0755))
	require.NoError(t, os.WriteFile(stale, []byte("{}"), 0644))

	require.NoError(t, writeStacCatalog(context.Background(), tbl, dir, stacTestProject))

	require.NoFileExists(t, stale)
	require.FileExists(t, filepath.Join(dir, "catalog.json"))
}

func TestStacProjectInfoTakesTheTitleFromTheProjectOntology(t *testing.T) {
	project := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = project
		out, err := command.CombinedOutput()
		require.NoErrorf(t, err, "git %v: %s", args, out)
	}
	git("init")
	git("remote", "add", "origin", "https://github.com/cgs-earth/sal-stac-test-project.git")
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".sal", "data"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".sal", "config.jsonld"), []byte(`{
  "@context": { "dc": "http://purl.org/dc/elements/1.1/", "owl": "http://www.w3.org/2002/07/owl#" },
  "@graph": [
    { "@id": ".", "@type": "owl:Ontology", "dc:title": "Test Project Ontology" }
  ]
}`), 0644))
	t.Chdir(project)

	info, err := stacProjectInfo("https://github.com/cgs-earth/sal-stac-test-project/")

	require.NoError(t, err)
	require.Equal(t, StacProject{
		Name:   "sal-stac-test-project",
		Owner:  "cgs-earth",
		Remote: "https://github.com/cgs-earth/sal-stac-test-project",
		Title:  "Test Project Ontology",
	}, info)
}

func TestStacProjectInfoFallsBackToTheProjectName(t *testing.T) {
	project := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = project
		out, err := command.CombinedOutput()
		require.NoErrorf(t, err, "git %v: %s", args, out)
	}
	git("init")
	git("remote", "add", "origin", "https://github.com/cgs-earth/sal-stac-test-project.git")
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".sal", "data"), 0755))
	t.Chdir(project)

	info, err := stacProjectInfo("https://github.com/cgs-earth/sal-stac-test-project/")

	require.NoError(t, err)
	require.Equal(t, "sal-stac-test-project", info.Title)
}

func TestBuildWritesTheStacCatalogLast(t *testing.T) {
	project := newPinsTestProject(t)
	servePinsTestVocabulary(t)

	_, err := (&BuildCmd{Format: GraphExportFormatIceberg}).Run()
	require.NoError(t, err)

	collection := readStacJSON(t, filepath.Join(project, ".sal", "data", "stac", "triples", "collection.json"))
	require.Equal(t, "sal-pins-test-project.triples", collection["iceberg:table_id"])
	require.Equal(t, "sal-pins-test-project", collection["title"])
	// the two source triples plus the provenance statements of the pinned vocabularies
	require.Greater(t, collection["table:row_count"], 2.0)
	catalog := readStacJSON(t, filepath.Join(project, ".sal", "data", "stac", "catalog.json"))
	require.Equal(t, "sal-pins-test-project", catalog["id"])
}
