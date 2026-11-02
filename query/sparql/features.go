package sparql

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cgs-earth/sal/pkg"
)

// FeatureRunner is the OGC API Features surface over the triples table that
// `sal serve` answers /ogc from. A feature is an RDF subject with a geometry:
// its id is the subject IRI, so the same feature can be read at any snapshot
// of the table, and its properties are whichever predicates the request
// names.
type FeatureRunner interface {
	// Features answers one page of the collection's features, or the one
	// feature query.ID names.
	Features(ctx context.Context, query FeatureQuery) (FeaturePage, error)
	// FeaturesExtent reports the bounding box of every geometry at a snapshot
	// of the table, or of the current table for a zero snapshot.
	FeaturesExtent(ctx context.Context, snapshotID int64) (Feature, error)
	// Project names the data product the collection is served from.
	Project() ProjectInfo
}

// ProjectInfo is what names a data product on the OGC landing page: the
// title and description the project node of .sal/config.jsonld records,
// empty for a table on object storage, and the path of the table itself.
type ProjectInfo struct {
	Title       string
	Description string
	TablePath   string
}

// DefaultFeatureLimit is how many features a page holds when the request does
// not say; MaxGeometries bounds what it may ask for.
const DefaultFeatureLimit = 100

// MaxFeatureProperties bounds how many predicates one request may join, since
// each one is a join over the whole table.
const MaxFeatureProperties = 50

// The GeoSPARQL predicates that hang a geometry node off the feature it
// describes. A geometry whose subject is the object of one of them belongs to
// that triple's subject, so the feature is the thing described rather than
// the geometry node, which is usually a blank node.
var geometryOwnerPredicates = []string{
	"http://www.opengis.net/ont/geosparql#hasGeometry",
	"http://www.opengis.net/ont/geosparql#hasDefaultGeometry",
}

// FeatureQuery selects features: one page of them, narrowed by a bounding
// box and an rdf:type, or the one named by ID, at a snapshot of the table
// or the current one, each carrying the predicates in Properties.
type FeatureQuery struct {
	Limit  int
	Offset int
	BBox   *BBox
	// Properties are the predicate IRIs to join onto each feature, each
	// becoming a GeoJSON property keyed by the IRI.
	Properties []string
	// Type keeps only the features typed with this class IRI.
	Type string
	// SnapshotID reads the table at that snapshot; zero is the current table.
	SnapshotID int64
	// ID selects the one feature with this subject IRI, ignoring Limit,
	// Offset, and BBox.
	ID string
}

// FeatureItem is one feature as the OGC endpoint serves it: the subject IRI,
// the geometry as GeoJSON, and the requested properties as a JSON object
// keyed by predicate IRI, null where the feature lacks the predicate.
type FeatureItem struct {
	ID         string
	Geometry   json.RawMessage
	Properties json.RawMessage
}

// FeaturePage is a page of features and whether the collection has more past it.
type FeaturePage struct {
	Features []FeatureItem
	More     bool
}

// NoSnapshotError is the error Features reports for a snapshot the table
// does not have, which the endpoint answers as a 404.
type NoSnapshotError struct {
	SnapshotID int64
}

func (e NoSnapshotError) Error() string {
	return fmt.Sprintf("the triples table has no snapshot %d", e.SnapshotID)
}

// Features reads one page of features, or the one feature the query names.
// One row past the limit is asked for so that a full page is told apart from
// the last one without counting the collection.
func (r DuckDBRunner) Features(ctx context.Context, query FeatureQuery) (FeaturePage, error) {
	if len(query.Properties) > MaxFeatureProperties {
		return FeaturePage{}, fmt.Errorf("at most %d properties can be requested, got %d", MaxFeatureProperties, len(query.Properties))
	}
	for _, property := range query.Properties {
		if !isAbsoluteIRI(property) {
			return FeaturePage{}, fmt.Errorf("property %q is not an absolute IRI", property)
		}
	}
	if query.Type != "" && !isAbsoluteIRI(query.Type) {
		return FeaturePage{}, fmt.Errorf("type %q is not an absolute IRI", query.Type)
	}
	if query.Limit <= 0 || query.Limit > MaxGeometries {
		query.Limit = DefaultFeatureLimit
	}
	if query.Offset < 0 {
		query.Offset = 0
	}
	source, err := r.sourceAt(ctx, query.SnapshotID)
	if err != nil {
		return FeaturePage{}, err
	}
	result, err := r.RunSQL(ctx, featuresSQL(source, query))
	if err != nil {
		return FeaturePage{}, err
	}
	page := FeaturePage{Features: make([]FeatureItem, 0, len(result.Rows))}
	for _, row := range result.Rows {
		if len(row) < 3 || strings.TrimSpace(row[1]) == "" {
			continue
		}
		if query.ID == "" && len(page.Features) == query.Limit {
			page.More = true
			break
		}
		page.Features = append(page.Features, FeatureItem{
			ID:         row[0],
			Geometry:   json.RawMessage(row[1]),
			Properties: json.RawMessage(row[2]),
		})
	}
	return page, nil
}

// FeaturesExtent is Extent at a snapshot of the table.
func (r DuckDBRunner) FeaturesExtent(ctx context.Context, snapshotID int64) (Feature, error) {
	source, err := r.sourceAt(ctx, snapshotID)
	if err != nil {
		return Feature{}, err
	}
	return r.extent(ctx, source)
}

// Project names the data product: the project node's title and description
// for the project's own table, nothing for one on object storage.
func (r DuckDBRunner) Project() ProjectInfo {
	info := ProjectInfo{TablePath: r.TablePath}
	if !isRemoteTablePath(r.TablePath) {
		if ontology := projectOntology(); ontology != nil {
			info.Title = ontology.Title
			info.Description = ontology.Description
		}
	}
	return info
}

// sourceAt is the FROM expression reading the table at a snapshot, checked to
// be one the table has, or the `triples` view for a zero snapshot.
func (r DuckDBRunner) sourceAt(ctx context.Context, snapshotID int64) (string, error) {
	if snapshotID == 0 {
		return TriplesView, nil
	}
	exists, err := r.HasSnapshot(ctx, snapshotID)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", NoSnapshotError{SnapshotID: snapshotID}
	}
	return snapshotScanSQL(r.TablePath, snapshotID), nil
}

// isAbsoluteIRI is the cheap check a predicate or class given on a URL gets:
// it has a scheme and none of the characters an IRI cannot hold.
func isAbsoluteIRI(value string) bool {
	scheme, _, found := strings.Cut(value, ":")
	return found && scheme != "" && !strings.ContainsAny(value, " \t\n\"<>{}|\\^`'")
}

// featuresSQL builds the query behind a page of features over source, the
// `triples` view or a snapshot scan.
//
// A feature is a subject with a geometry-valued object. When that subject is
// itself the object of geo:hasGeometry or geo:hasDefaultGeometry, the feature
// is the subject of that triple instead, so a GeoSPARQL feature is reported
// under its own IRI and not under its geometry node's. A feature with several
// geometries carries them as one GeometryCollection; with one, that geometry
// as it is, since ST_Collect would wrap a lone point as a MultiPoint.
//
// Each requested predicate is a LEFT JOIN of the triples with that predicate,
// grouped by subject, so a missing one is null, a single value a scalar, and
// several values an array. A value keeps its type: a number is a JSON number,
// an xsd:boolean a JSON boolean, a date-time its ISO text, a geometry its WKT,
// an IRI, a blank node, and any other literal its text. json_object takes the
// predicate IRIs themselves as keys, which is what the response is keyed by.
func featuresSQL(source string, query FeatureQuery) string {
	var sql strings.Builder
	owners := make([]string, len(geometryOwnerPredicates))
	for i, predicate := range geometryOwnerPredicates {
		owners[i] = "'" + escapeSQLLiteral(predicate) + "'"
	}
	fmt.Fprintf(&sql, `WITH geometries AS (
	SELECT COALESCE(owner.subject, g.subject) AS feature, g.object_geometry AS geometry
	FROM %[1]s AS g
	LEFT JOIN %[1]s AS owner
		ON COALESCE(owner.object_iri, owner.object_string) = g.subject
		AND owner.predicate IN (%[2]s)
	WHERE g.object_geometry IS NOT NULL
),
grouped AS (
	SELECT feature, LIST(geometry) AS geometries
	FROM geometries
	GROUP BY feature
),
features AS (
	SELECT feature, CASE WHEN len(geometries) = 1 THEN geometries[1] ELSE ST_Collect(geometries) END AS geometry
	FROM grouped
)
SELECT
	f.feature AS id,
	ST_AsGeoJSON(f.geometry) AS geometry,
	%[3]s AS properties
FROM features AS f`, source, strings.Join(owners, ", "), propertiesObjectSQL(query.Properties))
	for i, property := range query.Properties {
		fmt.Fprintf(&sql, `
LEFT JOIN (
	SELECT t.subject, LIST(%s ORDER BY t.triple_hash) AS "values"
	FROM %s AS t
	WHERE t.predicate = '%s'
	GROUP BY t.subject
) AS p%d ON p%d.subject = f.feature`, propertyValueSQL("t"), source, escapeSQLLiteral(property), i, i)
	}
	conditions := []string{}
	if query.ID != "" {
		conditions = append(conditions, "f.feature = '"+escapeSQLLiteral(query.ID)+"'")
	} else if query.BBox != nil {
		conditions = append(conditions, "ST_Intersects(f.geometry, ST_MakeEnvelope("+bboxArgs(*query.BBox)+"))")
	}
	if query.Type != "" {
		conditions = append(conditions, fmt.Sprintf(`EXISTS (
	SELECT 1
	FROM %s AS t
	WHERE t.subject = f.feature
		AND t.predicate = '%s'
		AND t.object_iri = '%s'
)`, source, escapeSQLLiteral(RDFTypeIRI), escapeSQLLiteral(query.Type)))
	}
	if len(conditions) > 0 {
		sql.WriteString("\nWHERE " + strings.Join(conditions, "\n\tAND "))
	}
	sql.WriteString("\nORDER BY f.feature")
	if query.ID == "" {
		fmt.Fprintf(&sql, "\nLIMIT %d\nOFFSET %d", query.Limit+1, query.Offset)
	}
	return sql.String()
}

// propertiesObjectSQL is the JSON object a feature's properties are
// projected as: each requested predicate keyed by its IRI, read from the
// join named after its position; a lone value is unwrapped from the list.
func propertiesObjectSQL(properties []string) string {
	if len(properties) == 0 {
		return "'{}'::JSON"
	}
	pairs := make([]string, len(properties))
	for i, property := range properties {
		pairs[i] = fmt.Sprintf(`'%s', CASE WHEN len(p%d."values") = 1 THEN p%d."values"[1] ELSE to_json(p%d."values") END`, escapeSQLLiteral(property), i, i, i)
	}
	return "json_object(" + strings.Join(pairs, ", ") + ")"
}

// propertyValueSQL renders the object of a triple scan as the JSON value a
// feature property carries, typed from whichever object column holds it.
func propertyValueSQL(alias string) string {
	return fmt.Sprintf(`CASE
		WHEN %[1]s.object_iri IS NOT NULL THEN to_json(%[1]s.object_iri)
		WHEN %[1]s.object_float IS NOT NULL THEN to_json(%[1]s.object_float)
		WHEN %[1]s.object_integer IS NOT NULL THEN to_json(%[1]s.object_integer)
		WHEN %[1]s.object_byte IS NOT NULL THEN to_json(%[1]s.object_byte)
		WHEN %[1]s.object_time IS NOT NULL THEN to_json(%[2]s)
		WHEN %[1]s.object_geometry IS NOT NULL THEN to_json(ST_AsText(%[1]s.object_geometry))
		WHEN %[1]s.object_type = '%[3]s' THEN to_json(TRY_CAST(%[1]s.object_string AS BOOLEAN))
		ELSE to_json(%[1]s.object_string)
	END`, alias, timeTextExpr(alias+".object_time"), escapeSQLLiteral(pkg.XSDNamespace+"boolean"))
}
