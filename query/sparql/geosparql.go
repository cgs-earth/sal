package sparql

import (
	"fmt"
	"strings"

	rdflibgo "github.com/tggo/goRDFlib"
	rdflibsparql "github.com/tggo/goRDFlib/sparql"
)

// GeoSPARQL functions are answered by DuckDB's spatial extension. Only the
// functions with a direct ST_ analog are translated: the Simple Features
// relations, distance, the geometry constructors, and the measures and
// properties of a geometry GeoSPARQL 1.1 adds. Anything else is an error
// rather than an approximation.

const (
	geofNamespace = "http://www.opengis.net/def/function/geosparql/"
	geoNamespace  = "http://www.opengis.net/ont/geosparql#"
	wktLiteral    = geoNamespace + "wktLiteral"
	uomNamespace  = "http://www.opengis.net/def/uom/OGC/1.0/"
	sfNamespace   = "http://www.opengis.net/ont/sf#"
)

// geofRelations are the Simple Features relation functions, each a boolean over
// two geometries, keyed by lowercased local name.
var geofRelations = map[string]string{
	"sfequals":     "ST_Equals",
	"sfdisjoint":   "ST_Disjoint",
	"sfintersects": "ST_Intersects",
	"sftouches":    "ST_Touches",
	"sfcrosses":    "ST_Crosses",
	"sfwithin":     "ST_Within",
	"sfcontains":   "ST_Contains",
	"sfoverlaps":   "ST_Overlaps",
}

// geofConstructors are the geometry-valued functions whose arguments are all
// geometries, keyed by lowercased local name.
var geofConstructors = map[string]struct {
	sql   string
	arity int
}{
	"envelope":      {sql: "ST_Envelope", arity: 1},
	"boundary":      {sql: "ST_Boundary", arity: 1},
	"convexhull":    {sql: "ST_ConvexHull", arity: 1},
	"centroid":      {sql: "ST_Centroid", arity: 1},
	"intersection":  {sql: "ST_Intersection", arity: 2},
	"union":         {sql: "ST_Union", arity: 2},
	"difference":    {sql: "ST_Difference", arity: 2},
	"symdifference": {sql: "ST_SymDifference", arity: 2},
}

// geofMeasures are the measures of one geometry that GeoSPARQL 1.1 takes in a
// unit, keyed by lowercased local name: the planar function answers
// uom:degree, in the units of the stored coordinates, and the spheroidal one
// uom:metre, which the metric form of each (geof:metricArea, ...) always
// means.
var geofMeasures = map[string]struct{ planar, metric string }{
	"area":      {planar: "ST_Area", metric: "ST_Area_Spheroid"},
	"length":    {planar: "ST_Length", metric: "ST_Length_Spheroid"},
	"perimeter": {planar: "ST_Perimeter", metric: "ST_Perimeter_Spheroid"},
}

// geofProperties are the GeoSPARQL 1.1 functions that read one property of a
// geometry, keyed by lowercased local name, with what each yields.
var geofProperties = map[string]struct {
	sql  string
	kind exprKind
}{
	"minx":          {sql: "ST_XMin", kind: kindNumeric},
	"maxx":          {sql: "ST_XMax", kind: kindNumeric},
	"miny":          {sql: "ST_YMin", kind: kindNumeric},
	"maxy":          {sql: "ST_YMax", kind: kindNumeric},
	"minz":          {sql: "ST_ZMin", kind: kindNumeric},
	"maxz":          {sql: "ST_ZMax", kind: kindNumeric},
	"dimension":     {sql: "ST_Dimension", kind: kindNumeric},
	"numgeometries": {sql: "ST_NumGeometries", kind: kindNumeric},
	"isempty":       {sql: "ST_IsEmpty", kind: kindBoolean},
	"issimple":      {sql: "ST_IsSimple", kind: kindBoolean},
	"is3d":          {sql: "ST_HasZ", kind: kindBoolean},
	"ismeasured":    {sql: "ST_HasM", kind: kindBoolean},
	"aswkt":         {sql: "ST_AsText", kind: kindString},
	"asgeojson":     {sql: "ST_AsGeoJSON", kind: kindString},
}

// crs84 is the one coordinate reference system the table stores, which
// geof:getSRID reports for every geometry.
const crs84 = "http://www.opengis.net/def/crs/OGC/1.3/CRS84"

// sfGeometryTypes maps what ST_GeometryType reports to the Simple Features
// class geof:geometryType names it by.
var sfGeometryTypes = map[string]string{
	"POINT":              "Point",
	"LINESTRING":         "LineString",
	"POLYGON":            "Polygon",
	"MULTIPOINT":         "MultiPoint",
	"MULTILINESTRING":    "MultiLineString",
	"MULTIPOLYGON":       "MultiPolygon",
	"GEOMETRYCOLLECTION": "GeometryCollection",
}

// geofUnsupported names the geof: functions sal knows it cannot answer and why,
// keyed by lowercased local name.
var geofUnsupported = map[string]string{
	"relate":    "DuckDB's spatial extension has no DE-9IM function to answer it",
	"transform": "every geometry is stored in CRS84 and nothing is reprojected",
}

// geofFunctions are the geof: functions the translator answers, as written in
// the GeoSPARQL specification with what each becomes, which is what the
// SPARQL editor completes `geof:` with and shows for the one under the
// caret. TestGeofFunctionsAreTheTranslatedOnes keeps it in step with the
// tables above.
var geofFunctions = []struct{ name, comment string }{
	{"sfEquals", "geof:sfEquals(a, b): whether two geometries are spatially equal, as ST_Equals."},
	{"sfDisjoint", "geof:sfDisjoint(a, b): whether two geometries share no point, as ST_Disjoint."},
	{"sfIntersects", "geof:sfIntersects(a, b): whether two geometries share a point, as ST_Intersects."},
	{"sfTouches", "geof:sfTouches(a, b): whether two geometries touch only at their boundaries, as ST_Touches."},
	{"sfCrosses", "geof:sfCrosses(a, b): whether two geometries cross, as ST_Crosses."},
	{"sfWithin", "geof:sfWithin(a, b): whether a lies within b, as ST_Within."},
	{"sfContains", "geof:sfContains(a, b): whether a contains b, as ST_Contains."},
	{"sfOverlaps", "geof:sfOverlaps(a, b): whether two geometries overlap, as ST_Overlaps."},
	{"distance", "geof:distance(a, b, unit): the distance between two geometries, in degrees (uom:degree, the default, as ST_Distance) or in great-circle metres between their nearest points (uom:metre)."},
	{"metricDistance", "geof:metricDistance(a, b): the great-circle distance in metres between the nearest points of two geometries, as geof:distance in uom:metre."},
	{"buffer", "geof:buffer(a, radius, unit): the geometry within the radius of a, in degrees (uom:degree, as ST_Buffer) or in metres (uom:metre, buffered in Web Mercator at the scale of a's centroid)."},
	{"metricBuffer", "geof:metricBuffer(a, radius): the geometry within the radius in metres of a, as geof:buffer in uom:metre."},
	{"envelope", "geof:envelope(a): the bounding box of a geometry, as ST_Envelope."},
	{"boundary", "geof:boundary(a): the boundary of a geometry, as ST_Boundary."},
	{"convexHull", "geof:convexHull(a): the convex hull of a geometry, as ST_ConvexHull."},
	{"centroid", "geof:centroid(a): the centroid of a geometry, as ST_Centroid."},
	{"intersection", "geof:intersection(a, b): the geometry two geometries share, as ST_Intersection."},
	{"union", "geof:union(a, b): the geometry two geometries make together, as ST_Union."},
	{"difference", "geof:difference(a, b): the part of a outside b, as ST_Difference."},
	{"symDifference", "geof:symDifference(a, b): the parts of a and b outside each other, as ST_SymDifference."},
	{"area", "geof:area(a, unit): the area of a geometry, in square degrees (uom:degree, as ST_Area) or square metres on the spheroid (uom:metre, as ST_Area_Spheroid)."},
	{"metricArea", "geof:metricArea(a): the area of a geometry in square metres, as ST_Area_Spheroid."},
	{"length", "geof:length(a, unit): the length of a geometry, in degrees (uom:degree, as ST_Length) or metres on the spheroid (uom:metre, as ST_Length_Spheroid)."},
	{"metricLength", "geof:metricLength(a): the length of a geometry in metres, as ST_Length_Spheroid."},
	{"perimeter", "geof:perimeter(a, unit): the perimeter of a geometry, in degrees (uom:degree, as ST_Perimeter) or metres on the spheroid (uom:metre, as ST_Perimeter_Spheroid)."},
	{"metricPerimeter", "geof:metricPerimeter(a): the perimeter of a geometry in metres, as ST_Perimeter_Spheroid."},
	{"minX", "geof:minX(a): the smallest x (longitude) of a geometry, as ST_XMin."},
	{"maxX", "geof:maxX(a): the largest x (longitude) of a geometry, as ST_XMax."},
	{"minY", "geof:minY(a): the smallest y (latitude) of a geometry, as ST_YMin."},
	{"maxY", "geof:maxY(a): the largest y (latitude) of a geometry, as ST_YMax."},
	{"minZ", "geof:minZ(a): the smallest z of a geometry, as ST_ZMin."},
	{"maxZ", "geof:maxZ(a): the largest z of a geometry, as ST_ZMax."},
	{"dimension", "geof:dimension(a): the topological dimension of a geometry, 0 for a point, 1 for a line, 2 for a polygon, as ST_Dimension."},
	{"coordinateDimension", "geof:coordinateDimension(a): how many coordinates each position of a geometry has, 2 plus one for z and one for m, from ST_HasZ and ST_HasM."},
	{"spatialDimension", "geof:spatialDimension(a): how many spatial coordinates each position of a geometry has, 2 or 3, from ST_HasZ."},
	{"numGeometries", "geof:numGeometries(a): how many geometries a collection holds, as ST_NumGeometries."},
	{"isEmpty", "geof:isEmpty(a): whether a geometry has no points, as ST_IsEmpty."},
	{"isSimple", "geof:isSimple(a): whether a geometry has no self-intersection or self-tangency, as ST_IsSimple."},
	{"is3D", "geof:is3D(a): whether a geometry has z coordinates, as ST_HasZ."},
	{"isMeasured", "geof:isMeasured(a): whether a geometry has m coordinates, as ST_HasM."},
	{"geometryType", "geof:geometryType(a): the Simple Features class of a geometry, sf:Point and so on, from ST_GeometryType."},
	{"getSRID", "geof:getSRID(a): the coordinate reference system of a geometry, which is CRS84 for every geometry sal stores."},
	{"asWKT", "geof:asWKT(a): a geometry as WKT, as ST_AsText."},
	{"asGeoJSON", "geof:asGeoJSON(a): a geometry as GeoJSON, as ST_AsGeoJSON."},
}

// geofLocalName returns the local name of a geof: function. The parser expands a
// prefixed function name to its IRI and upper-cases it, so the comparison is
// case-insensitive.
func geofLocalName(name string) (string, bool) {
	lowered := strings.ToLower(name)
	if !strings.HasPrefix(lowered, geofNamespace) {
		return "", false
	}
	return strings.TrimPrefix(lowered, geofNamespace), true
}

// geoFunctionSQL translates a geof: function call: a relation is a truth
// value, a constructor a geometry, a measure or a distance a number, and a
// property whatever the table above says.
func (t *translator) geoFunctionSQL(call *rdflibsparql.FuncExpr, bindings map[string]sqlBinding) (sqlExpr, error) {
	local, ok := geofLocalName(call.Name)
	if !ok {
		return sqlExpr{}, fmt.Errorf("SPARQL function %q is not supported yet", strings.ToLower(call.Name))
	}
	if relation, ok := geofRelations[local]; ok {
		args, err := t.geometryArgsSQL(call, 2, bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		return sqlExpr{sql: relation + "(" + strings.Join(args, ", ") + ")", kind: kindBoolean}, nil
	}
	if constructor, ok := geofConstructors[local]; ok {
		args, err := t.geometryArgsSQL(call, constructor.arity, bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		return sqlExpr{sql: constructor.sql + "(" + strings.Join(args, ", ") + ")", kind: kindGeometry}, nil
	}
	if property, ok := geofProperties[local]; ok {
		args, err := t.geometryArgsSQL(call, 1, bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		return sqlExpr{sql: property.sql + "(" + args[0] + ")", kind: property.kind}, nil
	}
	if measure, ok := geofMeasures[strings.TrimPrefix(local, "metric")]; ok {
		return t.measureSQL(call, local, measure.planar, measure.metric, bindings)
	}
	switch local {
	case "distance", "metricdistance":
		sql, err := t.distanceSQL(call, bindings)
		return sqlExpr{sql: sql, kind: kindNumeric}, err
	case "buffer", "metricbuffer":
		sql, err := t.bufferSQL(call, bindings)
		return sqlExpr{sql: sql, kind: kindGeometry}, err
	case "coordinatedimension", "spatialdimension":
		args, err := t.geometryArgsSQL(call, 1, bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		sql := "2 + CASE WHEN ST_HasZ(" + args[0] + ") THEN 1 ELSE 0 END"
		if local == "coordinatedimension" {
			sql += " + CASE WHEN ST_HasM(" + args[0] + ") THEN 1 ELSE 0 END"
		}
		return sqlExpr{sql: sql, kind: kindNumeric}, nil
	case "geometrytype":
		args, err := t.geometryArgsSQL(call, 1, bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		sql := "CASE CAST(ST_GeometryType(" + args[0] + ") AS VARCHAR)"
		for _, reported := range []string{"POINT", "LINESTRING", "POLYGON", "MULTIPOINT", "MULTILINESTRING", "MULTIPOLYGON", "GEOMETRYCOLLECTION"} {
			sql += " WHEN " + sqlString(reported) + " THEN " + sqlString(sfNamespace+sfGeometryTypes[reported])
		}
		return sqlExpr{sql: sql + " END", kind: kindIRI}, nil
	case "getsrid":
		args, err := t.geometryArgsSQL(call, 1, bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		return sqlExpr{sql: "CASE WHEN " + args[0] + " IS NOT NULL THEN " + sqlString(crs84) + " END", kind: kindIRI}, nil
	}
	if reason, ok := geofUnsupported[local]; ok {
		return sqlExpr{}, fmt.Errorf("GeoSPARQL function geof:%s is not supported: %s", local, reason)
	}
	return sqlExpr{}, fmt.Errorf("GeoSPARQL function geof:%s is not supported yet", local)
}

// metricUnit reads the unit argument of a function that takes one, reporting
// whether it asks for metres. Only uom:degree, the unit of the stored
// coordinates, and uom:metre are answered.
func metricUnit(local string, unit rdflibsparql.Expr) (bool, error) {
	iri, ok := unit.(*rdflibsparql.IRIExpr)
	if !ok {
		return false, fmt.Errorf("the unit of geof:%s must be a uom: IRI", local)
	}
	switch iri.Value {
	case uomNamespace + "degree":
		return false, nil
	case uomNamespace + "metre":
		return true, nil
	default:
		return false, fmt.Errorf("geof:%s unit %q is not supported; use uom:degree or uom:metre", local, iri.Value)
	}
}

// measureSQL translates geof:area, geof:length, and geof:perimeter, which
// take a geometry and a unit, and their metric forms, which take the
// geometry alone and measure in metres.
func (t *translator) measureSQL(call *rdflibsparql.FuncExpr, local string, planar string, metric string, bindings map[string]sqlBinding) (sqlExpr, error) {
	metres := strings.HasPrefix(local, "metric")
	if metres && len(call.Args) != 1 {
		return sqlExpr{}, fmt.Errorf("geof:%s takes one geometry, got %d arguments", local, len(call.Args))
	}
	if !metres {
		if len(call.Args) != 2 {
			return sqlExpr{}, fmt.Errorf("geof:%s takes a geometry and a unit, got %d arguments", local, len(call.Args))
		}
		var err error
		if metres, err = metricUnit(local, call.Args[1]); err != nil {
			return sqlExpr{}, err
		}
	}
	geometry, err := t.geometryOperandSQL(call.Args[0], bindings)
	if err != nil {
		return sqlExpr{}, err
	}
	function := planar
	if metres {
		function = metric
	}
	return sqlExpr{sql: function + "(" + geometry + ")", kind: kindNumeric}, nil
}

// distanceSQL translates geof:distance and geof:metricDistance. The unit
// argument the spec requires is optional here and defaults to degrees, the
// unit of the stored coordinates; metres are answered by metreDistanceSQL.
func (t *translator) distanceSQL(call *rdflibsparql.FuncExpr, bindings map[string]sqlBinding) (string, error) {
	local, _ := geofLocalName(call.Name)
	metres := local == "metricdistance"
	if (metres && len(call.Args) != 2) || (!metres && len(call.Args) != 2 && len(call.Args) != 3) {
		return "", fmt.Errorf("geof:%s takes two geometries and an optional unit, got %d arguments", local, len(call.Args))
	}
	if len(call.Args) == 3 {
		var err error
		if metres, err = metricUnit(local, call.Args[2]); err != nil {
			return "", err
		}
	}
	args, err := t.geometryArgsSQL(&rdflibsparql.FuncExpr{Name: call.Name, Args: call.Args[:2]}, 2, bindings)
	if err != nil {
		return "", err
	}
	if metres {
		return metreDistanceSQL(args[0], args[1]), nil
	}
	return "ST_Distance(" + args[0] + ", " + args[1] + ")", nil
}

// bufferSQL translates geof:buffer(a, radius, unit) and geof:metricBuffer(a,
// radius). A radius in degrees is ST_Buffer as it is. A radius in metres is
// buffered in Web Mercator, where a metre on the ground is 1/cos(latitude)
// projected units, at the latitude of the geometry's centroid; that is exact
// for a point and within a fraction of a percent for a geometry a few
// kilometres across, the same approximation metreDistanceSQL makes.
func (t *translator) bufferSQL(call *rdflibsparql.FuncExpr, bindings map[string]sqlBinding) (string, error) {
	local, _ := geofLocalName(call.Name)
	metres := local == "metricbuffer"
	if (metres && len(call.Args) != 2) || (!metres && len(call.Args) != 3) {
		return "", fmt.Errorf("geof:%s takes a geometry, a radius, and a unit, got %d arguments", local, len(call.Args))
	}
	if !metres {
		var err error
		if metres, err = metricUnit(local, call.Args[2]); err != nil {
			return "", err
		}
	}
	geometry, err := t.geometryOperandSQL(call.Args[0], bindings)
	if err != nil {
		return "", err
	}
	radius, err := t.numericSQL(call.Args[1], bindings)
	if err != nil {
		return "", err
	}
	if !metres {
		return "ST_Buffer(" + geometry + ", " + radius + ")", nil
	}
	mercator := "ST_Transform(" + geometry + ", 'EPSG:4326', 'EPSG:3857')"
	scale := "cos(radians(ST_Y(ST_Centroid(" + geometry + "))))"
	return "ST_Transform(ST_Buffer(" + mercator + ", " + radius + " / " + scale + "), 'EPSG:3857', 'EPSG:4326')", nil
}

// metreDistanceSQL is the great-circle distance in metres between two
// geometries of any kind. DuckDB's ST_Distance_Sphere takes points only, so
// the pair of nearest points is found first, in Web Mercator, which is
// conformal and so picks the same nearest points a local metric projection
// would for anything but a geometry spanning a continent, and the spherical
// distance is taken between those two points. A point is its own nearest
// point, so between points this is ST_Distance_Sphere exactly. Projecting
// through a per-row azimuthal projection centred on the geometry would be
// exact but builds a PROJ transformation per row, which costs seconds per
// thousand rows; the constant transformations here are cached by DuckDB.
func metreDistanceSQL(a, b string) string {
	mercator := func(geometry string) string { return "ST_Transform(" + geometry + ", 'EPSG:4326', 'EPSG:3857')" }
	nearest := func(on, to string) string {
		return "ST_Transform(ST_ClosestPoint(" + mercator(on) + ", " + mercator(to) + "), 'EPSG:3857', 'EPSG:4326')"
	}
	return "ST_Distance_Sphere(" + nearest(a, b) + ", " + nearest(b, a) + ")"
}

func (t *translator) geometryArgsSQL(call *rdflibsparql.FuncExpr, arity int, bindings map[string]sqlBinding) ([]string, error) {
	local, _ := geofLocalName(call.Name)
	if len(call.Args) != arity {
		return nil, fmt.Errorf("geof:%s takes %d geometries, got %d arguments", local, arity, len(call.Args))
	}
	args := make([]string, 0, arity)
	for _, arg := range call.Args {
		sql, err := t.geometryOperandSQL(arg, bindings)
		if err != nil {
			return nil, err
		}
		args = append(args, sql)
	}
	return args, nil
}

// geometryOperandSQL translates one geometry argument: a variable bound in
// object position, which is read as the stored geometry, a WKT literal, or a
// nested geometry-valued geof: call.
func (t *translator) geometryOperandSQL(expr rdflibsparql.Expr, bindings map[string]sqlBinding) (string, error) {
	switch e := expr.(type) {
	case *rdflibsparql.VarExpr:
		binding, ok := bindings[e.Name]
		if !ok {
			return "", fmt.Errorf("FILTER variable ?%s is not bound by a supported triple pattern", e.Name)
		}
		if !binding.typedObject() {
			return "", fmt.Errorf("?%s is bound as a %s, not as a geometry literal", e.Name, binding.column)
		}
		return binding.col("object_geometry"), nil
	case *rdflibsparql.LiteralExpr:
		wkt, err := wktFromLiteral(e.Value)
		if err != nil {
			return "", err
		}
		return "ST_GeomFromText(" + sqlString(wkt) + ")", nil
	case *rdflibsparql.FuncExpr:
		geometry, err := t.geoFunctionSQL(e, bindings)
		if err != nil {
			return "", err
		}
		if geometry.kind != kindGeometry {
			return "", fmt.Errorf("geof:%s yields a %s, not a geometry", strings.ToLower(strings.TrimPrefix(strings.ToLower(e.Name), geofNamespace)), map[exprKind]string{kindBoolean: "boolean", kindNumeric: "number"}[geometry.kind])
		}
		return geometry.sql, nil
	default:
		return "", fmt.Errorf("unsupported geometry argument %T", expr)
	}
}

// wktFromLiteral reads the WKT out of a geometry literal. A geo:wktLiteral may
// open with the IRI of its CRS; the stored geometries carry none, so it is
// dropped rather than compared.
func wktFromLiteral(term rdflibgo.Term) (string, error) {
	literal, ok := term.(rdflibgo.Literal)
	if !ok {
		return "", fmt.Errorf("a geometry argument must be a geo:wktLiteral, got %s", term.String())
	}
	datatype := literal.Datatype().Value()
	if datatype != "" && datatype != wktLiteral && datatype != rdflibgo.XSDString.Value() {
		return "", fmt.Errorf("a geometry literal must be a geo:wktLiteral, got a %s", datatype)
	}
	wkt := strings.TrimSpace(literal.Lexical())
	if strings.HasPrefix(wkt, "<") {
		if end := strings.Index(wkt, ">"); end >= 0 {
			wkt = strings.TrimSpace(wkt[end+1:])
		}
	}
	if wkt == "" {
		return "", fmt.Errorf("a geometry literal must not be empty")
	}
	return wkt, nil
}
