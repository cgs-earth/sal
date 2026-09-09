package build

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/iceberg-go"
	"github.com/apache/iceberg-go/table"
	"github.com/cgs-earth/sal/importation"
	"github.com/cgs-earth/sal/pkg"
	"github.com/twpayne/go-geom"
	"github.com/twpayne/go-geom/encoding/wkb"
)

const (
	stacVersion = "1.1.0"
	// StacIcebergExtension is the STAC extension the collection carries its
	// Iceberg fields under, and the schema those fields are shaped by.
	StacIcebergExtension    = "https://portolan-sdi.github.io/stac-iceberg-extension/v1.0.0/schema.json"
	stacTableExtension      = "https://stac-extensions.github.io/table/v1.2.0/schema.json"
	stacProjectionExtension = "https://stac-extensions.github.io/projection/v2.0.0/schema.json"
	// icebergMediaType is the unregistered vendor media type the Iceberg
	// extension gives a metadata.json asset; no IANA type exists for Iceberg.
	icebergMediaType = "application/vnd.apache.iceberg+json"
	stacMediaType    = "application/json"

	// StacCatalogFile and StacCollectionFile are the paths, relative to
	// pkg.SalStacDir, of the catalog and of the one collection it lists, the
	// triples table. The collection sits in a directory named after the table,
	// as STAC best practice has it, so a second table would get one of its own.
	StacCatalogFile    = "catalog.json"
	StacCollectionFile = "triples/collection.json"
)

// StacProject is what the catalog says about the project a table was built
// from, beyond what the table itself records.
type StacProject struct {
	// Name is the git project name, which is both the catalog id and the
	// Iceberg namespace the table sits in.
	Name string
	// Owner is the git project owner, recorded as the collection's provider.
	Owner string
	// Remote is the https URL of the git repository the data was built from.
	Remote string
	// Title is the project ontology's dc:title when the project has one, and
	// the project name otherwise.
	Title string
}

type stacLink struct {
	Rel   string `json:"rel"`
	Href  string `json:"href"`
	Type  string `json:"type"`
	Title string `json:"title,omitempty"`
}

type stacAsset struct {
	Href        string   `json:"href"`
	Type        string   `json:"type"`
	Roles       []string `json:"roles"`
	Description string   `json:"description"`
}

type stacProvider struct {
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
	URL   string   `json:"url"`
}

type stacExtent struct {
	Spatial struct {
		BBox [][]float64 `json:"bbox"`
	} `json:"spatial"`
	Temporal struct {
		Interval [][]*string `json:"interval"`
	} `json:"temporal"`
}

// stacPartitionField mirrors one field of the Iceberg partition spec the way
// the extension's iceberg:partition_spec items are shaped.
type stacPartitionField struct {
	Name      string `json:"name"`
	Transform string `json:"transform"`
	SourceID  int    `json:"source-id"`
	FieldID   int    `json:"field-id"`
}

type stacColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type stacCatalog struct {
	Type        string     `json:"type"`
	StacVersion string     `json:"stac_version"`
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Links       []stacLink `json:"links"`
}

// stacCollection is a STAC Collection carrying the Table, Projection, and
// Iceberg extensions, which between them describe the triples table's columns,
// its CRS, and how to open it.
type stacCollection struct {
	Type           string               `json:"type"`
	StacVersion    string               `json:"stac_version"`
	StacExtensions []string             `json:"stac_extensions"`
	ID             string               `json:"id"`
	Title          string               `json:"title"`
	Description    string               `json:"description"`
	License        string               `json:"license"`
	Keywords       []string             `json:"keywords"`
	Providers      []stacProvider       `json:"providers"`
	Extent         stacExtent           `json:"extent"`
	Links          []stacLink           `json:"links"`
	Assets         map[string]stacAsset `json:"assets"`

	ProjCode string `json:"proj:code"`

	TableColumns         []stacColumn `json:"table:columns"`
	TableRowCount        int64        `json:"table:row_count"`
	TablePrimaryGeometry string       `json:"table:primary_geometry"`

	IcebergCatalogType       string               `json:"iceberg:catalog_type"`
	IcebergAuthorizationType string               `json:"iceberg:authorization_type"`
	IcebergTableID           string               `json:"iceberg:table_id"`
	IcebergMetadataLocation  string               `json:"iceberg:metadata_location"`
	IcebergFormatVersion     int                  `json:"iceberg:format_version"`
	IcebergCurrentSnapshotID string               `json:"iceberg:current_snapshot_id"`
	IcebergPartitionSpec     []stacPartitionField `json:"iceberg:partition_spec,omitempty"`
}

// WriteProjectStacCatalog describes the triples table `sal build` just
// committed as a STAC catalog under .sal/data/stac, so that a STAC client can
// discover the data product and an Iceberg reader can open it from the
// metadata location the collection records. base is the project base the
// ontology node in .sal/config.jsonld resolves against.
func WriteProjectStacCatalog(base string) error {
	tbl, err := pkg.GetSalIcebergTable()
	if err != nil {
		return err
	}
	dir, err := pkg.SalStacDir()
	if err != nil {
		return err
	}
	project, err := stacProjectInfo(base)
	if err != nil {
		return err
	}
	if err := writeStacCatalog(context.Background(), tbl, dir, project); err != nil {
		return err
	}
	slog.Info("Saved the STAC catalog describing the data product", "path", filepath.Join(dir, StacCatalogFile))
	return nil
}

// stacProjectInfo reads what the catalog says about the project from git and
// from the project ontology node of .sal/config.jsonld, when there is one.
func stacProjectInfo(base string) (StacProject, error) {
	name, err := pkg.GitProjectName()
	if err != nil {
		return StacProject{}, err
	}
	owner, err := pkg.GitProjectOwner()
	if err != nil {
		return StacProject{}, err
	}
	project := StacProject{Name: name, Owner: owner, Remote: strings.TrimSuffix(base, "/"), Title: name}

	configPath, err := pkg.SalConfigPath()
	if err != nil {
		return StacProject{}, err
	}
	ontology, err := importation.ReadOntology(configPath, base)
	if err != nil {
		return StacProject{}, err
	}
	if ontology != nil && ontology.Title != "" {
		project.Title = ontology.Title
	}
	return project, nil
}

// writeStacCatalog replaces whatever catalog dir held with a catalog of tbl. The
// directory is rewritten whole rather than updated in place, since a build is
// the only thing that writes it and a stale collection from an earlier layout
// would otherwise be left listed by nothing.
func writeStacCatalog(ctx context.Context, tbl *table.Table, dir string, project StacProject) error {
	collection, err := stacCollectionOf(ctx, tbl, project)
	if err != nil {
		return err
	}
	catalog := stacCatalog{
		Type:        "Catalog",
		StacVersion: stacVersion,
		ID:          project.Name,
		Title:       project.Title,
		Description: fmt.Sprintf("The SAL data product built from %s: the Apache Iceberg tables it is made of, and how to open them.", project.Remote),
		Links: []stacLink{
			{Rel: "root", Href: "./" + StacCatalogFile, Type: stacMediaType},
			{Rel: "self", Href: "./" + StacCatalogFile, Type: stacMediaType},
			{Rel: "child", Href: "./" + StacCollectionFile, Type: stacMediaType, Title: collection.Title},
		},
	}

	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := writeStacJSON(filepath.Join(dir, StacCatalogFile), catalog); err != nil {
		return err
	}
	return writeStacJSON(filepath.Join(dir, filepath.FromSlash(StacCollectionFile)), collection)
}

func writeStacJSON(path string, document any) error {
	content, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, append(content, '\n'), 0644)
}

// stacCollectionOf describes tbl as a STAC Collection. Everything about the
// table is read from the table itself: its schema, partition spec, metadata
// location, and snapshots from the metadata, and the row count and spatial
// extent from a scan of the geometry column.
func stacCollectionOf(ctx context.Context, tbl *table.Table, project StacProject) (*stacCollection, error) {
	current := tbl.CurrentSnapshot()
	if current == nil {
		return nil, fmt.Errorf("the triples table has no snapshot to describe")
	}
	metadataLocation, err := fileURL(tbl.MetadataLocation())
	if err != nil {
		return nil, err
	}
	rows, bbox, err := scanRowsAndExtent(ctx, tbl)
	if err != nil {
		return nil, err
	}

	// the earliest snapshot still in the table to the current one is when the
	// data product was built, which is the closest thing a triples table has
	// to a temporal extent of its own
	earliest := current
	for _, snapshot := range tbl.Metadata().Snapshots() {
		if snapshot.TimestampMs < earliest.TimestampMs {
			earliest = &snapshot
		}
	}
	start := snapshotTime(earliest)
	end := snapshotTime(current)

	collection := &stacCollection{
		Type:           "Collection",
		StacVersion:    stacVersion,
		StacExtensions: []string{stacProjectionExtension, stacTableExtension, StacIcebergExtension},
		ID:             tbl.Identifier()[len(tbl.Identifier())-1],
		Title:          project.Title,
		Description:    fmt.Sprintf("Every RDF statement of %s as one row per triple, with the object split across one column per datatype, built by SAL from %s.", project.Name, project.Remote),
		License:        "other",
		Keywords:       []string{"rdf", "triples", "iceberg", "sal"},
		Providers:      []stacProvider{{Name: project.Owner, Roles: []string{"producer"}, URL: project.Remote}},
		Links: []stacLink{
			{Rel: "root", Href: "../" + StacCatalogFile, Type: stacMediaType},
			{Rel: "parent", Href: "../" + StacCatalogFile, Type: stacMediaType},
			{Rel: "self", Href: "./collection.json", Type: stacMediaType},
		},
		Assets: map[string]stacAsset{
			"iceberg": {
				Href:        metadataLocation,
				Type:        icebergMediaType,
				Roles:       []string{"data", "metadata"},
				Description: fmt.Sprintf("Apache Iceberg v%d table metadata JSON", tbl.Metadata().Version()),
			},
		},
		TableRowCount:            rows,
		TablePrimaryGeometry:     "object_geometry",
		IcebergCatalogType:       "static",
		IcebergAuthorizationType: "none",
		IcebergTableID:           strings.Join(tbl.Identifier(), "."),
		IcebergMetadataLocation:  metadataLocation,
		IcebergFormatVersion:     tbl.Metadata().Version(),
		IcebergCurrentSnapshotID: strconv.FormatInt(current.SnapshotID, 10),
	}
	collection.Extent.Spatial.BBox = [][]float64{bbox}
	collection.Extent.Temporal.Interval = [][]*string{{&start, &end}}

	for _, field := range tbl.Schema().Fields() {
		collection.TableColumns = append(collection.TableColumns, stacColumn{Name: field.Name, Type: stacColumnType(field.Type)})
		if geometry, ok := field.Type.(iceberg.GeometryType); ok && field.Name == collection.TablePrimaryGeometry {
			collection.ProjCode = geometry.CRS()
		}
	}
	spec := tbl.Spec()
	for _, field := range spec.Fields() {
		collection.IcebergPartitionSpec = append(collection.IcebergPartitionSpec, stacPartitionField{
			Name:      field.Name,
			Transform: field.Transform.String(),
			SourceID:  field.SourceID(),
			FieldID:   field.FieldID,
		})
	}
	return collection, nil
}

// stacColumnType names an Iceberg column type the way the STAC Table extension
// does, in Arrow's vocabulary, so that int and long are told apart by width and
// a geometry is reported by its logical type rather than the binary it is
// stored as.
func stacColumnType(t iceberg.Type) string {
	name := t.String()
	switch {
	case name == "int":
		return "int32"
	case name == "long":
		return "int64"
	case strings.HasPrefix(name, "geometry"):
		return "geometry"
	default:
		return name
	}
}

func snapshotTime(snapshot *table.Snapshot) string {
	return time.UnixMilli(snapshot.TimestampMs).UTC().Format(time.RFC3339)
}

// fileURL turns the local path a filesystem catalog reports for its metadata
// into the file URL the collection records, and leaves a location that already
// has a scheme alone.
func fileURL(location string) (string, error) {
	if strings.Contains(location, "://") {
		return location, nil
	}
	abs, err := filepath.Abs(location)
	if err != nil {
		return "", err
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String(), nil
}

// worldBBox is the spatial extent of a table with no geometries. STAC requires
// a collection to state one, and a data product with no location of its own is
// not confined to anywhere in particular.
var worldBBox = []float64{-180, -90, 180, 90}

// scanRowsAndExtent reads the geometry column of the table once, counting every
// row and extending a bounding box over every geometry it finds. A scan rather
// than the snapshot summary is what makes the count exact: the summary's
// total-records does not subtract the equality deletes a rebuild writes.
func scanRowsAndExtent(ctx context.Context, tbl *table.Table) (int64, []float64, error) {
	_, records, err := tbl.Scan(
		table.WithSelectedFields("object_geometry"),
		table.WithCaseSensitive(true),
	).ToArrowRecords(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("scan the geometry column: %w", err)
	}

	var rows int64
	bounds := geom.NewBounds(geom.XY)
	for record, err := range records {
		if err != nil {
			return 0, nil, fmt.Errorf("read the geometry column: %w", err)
		}
		if record == nil {
			continue
		}
		rows += record.NumRows()
		column, err := binaryColumn(record)
		if err != nil {
			record.Release()
			return 0, nil, err
		}
		for i := 0; i < column.Len(); i++ {
			if column.IsNull(i) {
				continue
			}
			geometry, err := wkb.Unmarshal(column.Value(i))
			if err != nil {
				record.Release()
				return 0, nil, fmt.Errorf("read a geometry from the table: %w", err)
			}
			bounds.Extend(geometry)
		}
		record.Release()
	}

	if bounds.IsEmpty() {
		return rows, worldBBox, nil
	}
	return rows, []float64{bounds.Min(0), bounds.Min(1), bounds.Max(0), bounds.Max(1)}, nil
}

// wkbColumn is a binary Arrow column read one value at a time, which both
// array.Binary and array.LargeBinary are.
type wkbColumn interface {
	arrow.Array
	Value(i int) []byte
}

// binaryColumn is the WKB column of a record scanned for object_geometry alone,
// unwrapped from the GeoArrow extension type the reader may present it under.
func binaryColumn(record arrow.RecordBatch) (wkbColumn, error) {
	if record.NumCols() != 1 {
		return nil, fmt.Errorf("scan the geometry column: expected one column, got %d", record.NumCols())
	}
	column := record.Column(0)
	if extension, ok := column.(array.ExtensionArray); ok {
		column = extension.Storage()
	}
	binary, ok := column.(wkbColumn)
	if !ok {
		return nil, fmt.Errorf("scan the geometry column: expected binary WKB, got %s", column.DataType())
	}
	return binary, nil
}
