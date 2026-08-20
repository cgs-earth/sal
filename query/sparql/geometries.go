package sparql

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// GeometryRunner is the read-only geometry surface of the triples table that the
// map in `sal serve --with-ui` draws from.
type GeometryRunner interface {
	Geometries(ctx context.Context, query GeometryQuery) (FeatureCollection, error)
	Extent(ctx context.Context) (Feature, error)
}

// MaxGeometries bounds a page of geometries, the same way maxUIRows bounds a SQL
// result sent to the browser.
const MaxGeometries = 1000

type FeatureCollection struct {
	Type     string    `json:"type"`
	Features []Feature `json:"features"`
}

type Feature struct {
	Type string `json:"type"`
	// BBox is set on the dataset extent only, as [minX, minY, maxX, maxY].
	BBox       []float64         `json:"bbox,omitempty"`
	Geometry   json.RawMessage   `json:"geometry"`
	Properties map[string]string `json:"properties"`
}

// BBox is a longitude/latitude bounding box.
type BBox struct {
	MinX, MinY, MaxX, MaxY float64
}

// ParseBBox reads a bounding box written as "minX,minY,maxX,maxY".
func ParseBBox(value string) (BBox, error) {
	parts := strings.Split(value, ",")
	if len(parts) != 4 {
		return BBox{}, fmt.Errorf("a bbox must be minX,minY,maxX,maxY, got %q", value)
	}
	coordinates := make([]float64, 4)
	for i, part := range parts {
		coordinate, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			return BBox{}, fmt.Errorf("bbox coordinate %q is not a number", part)
		}
		coordinates[i] = coordinate
	}
	box := BBox{MinX: coordinates[0], MinY: coordinates[1], MaxX: coordinates[2], MaxY: coordinates[3]}
	if box.MinX > box.MaxX || box.MinY > box.MaxY {
		return BBox{}, fmt.Errorf("bbox %q has its minimum past its maximum", value)
	}
	return box, nil
}

// GeometryQuery selects a page of the table's geometries, optionally only those
// intersecting a bounding box.
type GeometryQuery struct {
	Limit  int
	Offset int
	BBox   *BBox
}

// Geometries reads a page of object geometries as GeoJSON features, each carrying
// the subject and predicate of the triple it is the object of.
func (r DuckDBRunner) Geometries(ctx context.Context, query GeometryQuery) (FeatureCollection, error) {
	if query.Limit <= 0 || query.Limit > MaxGeometries {
		query.Limit = MaxGeometries
	}
	if query.Offset < 0 {
		query.Offset = 0
	}
	result, err := r.RunSQL(ctx, geometrySQL(query))
	if err != nil {
		return FeatureCollection{}, err
	}
	features := make([]Feature, 0, len(result.Rows))
	for _, row := range result.Rows {
		if len(row) < 4 || strings.TrimSpace(row[3]) == "" {
			continue
		}
		features = append(features, Feature{
			Type:     "Feature",
			Geometry: json.RawMessage(row[3]),
			Properties: map[string]string{
				"subject":   row[0],
				"predicate": row[1],
				"object":    row[2],
			},
		})
	}
	return FeatureCollection{
		Type:     "FeatureCollection",
		Features: features,
	}, nil
}

// geometrySQL builds the map data query. The object_geometry column is read by
// DuckDB as GEOMETRY already, so it goes straight into the ST_ functions
// without a WKB conversion.
func geometrySQL(query GeometryQuery) string {
	sql := fmt.Sprintf(`
SELECT
	triples.subject,
	triples.predicate,
	%s AS object,
	ST_AsGeoJSON(triples.object_geometry) AS geometry
FROM triples
WHERE triples.object_geometry IS NOT NULL`, objectTextExpr("triples"))
	if query.BBox != nil {
		sql += fmt.Sprintf("\n  AND ST_Intersects(triples.object_geometry, ST_MakeEnvelope(%s))", bboxArgs(*query.BBox))
	}
	return sql + fmt.Sprintf("\nLIMIT %d\nOFFSET %d", query.Limit, query.Offset)
}

func bboxArgs(box BBox) string {
	return fmt.Sprintf("%s, %s, %s, %s", floatSQL(box.MinX), floatSQL(box.MinY), floatSQL(box.MaxX), floatSQL(box.MaxY))
}

func floatSQL(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// Extent reports the bounding box of every geometry in the table as a GeoJSON
// feature whose geometry is the envelope and whose bbox is its corners. A table
// with no geometries reports a null geometry and no bbox.
func (r DuckDBRunner) Extent(ctx context.Context) (Feature, error) {
	result, err := r.RunSQL(ctx, extentSQL)
	if err != nil {
		return Feature{}, err
	}
	feature := Feature{
		Type:       "Feature",
		Geometry:   json.RawMessage("null"),
		Properties: map[string]string{"geometries": "0"},
	}
	if len(result.Rows) == 0 || len(result.Rows[0]) < 6 || strings.TrimSpace(result.Rows[0][0]) == "" {
		return feature, nil
	}
	row := result.Rows[0]
	feature.Geometry = json.RawMessage(row[0])
	feature.Properties["geometries"] = row[5]
	feature.BBox = make([]float64, 4)
	for i, value := range row[1:5] {
		coordinate, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return Feature{}, fmt.Errorf("read the extent of the geometries: %w", err)
		}
		feature.BBox[i] = coordinate
	}
	return feature, nil
}

// extentSQL aggregates every geometry into one envelope, read back both as
// GeoJSON to draw and as its four corners to fit a map to.
const extentSQL = `
WITH extent AS (
	SELECT ST_Extent_Agg(triples.object_geometry) AS envelope, COUNT(*) AS geometries
	FROM triples
	WHERE triples.object_geometry IS NOT NULL
)
SELECT
	ST_AsGeoJSON(envelope) AS geometry,
	ST_XMin(envelope) AS min_x,
	ST_YMin(envelope) AS min_y,
	ST_XMax(envelope) AS max_x,
	ST_YMax(envelope) AS max_y,
	geometries
FROM extent`
