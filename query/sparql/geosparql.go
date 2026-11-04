package sparql

import (
	"fmt"
	"strings"

	rdflibgo "github.com/tggo/goRDFlib"
	rdflibsparql "github.com/tggo/goRDFlib/sparql"
)

// GeoSPARQL FILTER functions are answered by DuckDB's spatial extension. Only the
// functions with a direct ST_ analog are translated: the Simple Features
// relations, distance, and the geometry constructors that take nothing but
// geometries. Anything else is an error rather than an approximation.

const (
	geofNamespace = "http://www.opengis.net/def/function/geosparql/"
	geoNamespace  = "http://www.opengis.net/ont/geosparql#"
	wktLiteral    = geoNamespace + "wktLiteral"
	uomNamespace  = "http://www.opengis.net/def/uom/OGC/1.0/"
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
	"intersection":  {sql: "ST_Intersection", arity: 2},
	"union":         {sql: "ST_Union", arity: 2},
	"difference":    {sql: "ST_Difference", arity: 2},
	"symdifference": {sql: "ST_SymDifference", arity: 2},
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
	{"envelope", "geof:envelope(a): the bounding box of a geometry, as ST_Envelope."},
	{"boundary", "geof:boundary(a): the boundary of a geometry, as ST_Boundary."},
	{"convexHull", "geof:convexHull(a): the convex hull of a geometry, as ST_ConvexHull."},
	{"intersection", "geof:intersection(a, b): the geometry two geometries share, as ST_Intersection."},
	{"union", "geof:union(a, b): the geometry two geometries make together, as ST_Union."},
	{"difference", "geof:difference(a, b): the part of a outside b, as ST_Difference."},
	{"symDifference", "geof:symDifference(a, b): the parts of a and b outside each other, as ST_SymDifference."},
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
// value, a constructor a geometry, and distance a number.
func geoFunctionSQL(call *rdflibsparql.FuncExpr, bindings map[string]sqlBinding) (sqlExpr, error) {
	local, ok := geofLocalName(call.Name)
	if !ok {
		return sqlExpr{}, fmt.Errorf("SPARQL function %q is not supported yet", strings.ToLower(call.Name))
	}
	if relation, ok := geofRelations[local]; ok {
		args, err := geometryArgsSQL(call, 2, bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		return sqlExpr{sql: relation + "(" + strings.Join(args, ", ") + ")", kind: kindBoolean}, nil
	}
	if constructor, ok := geofConstructors[local]; ok {
		args, err := geometryArgsSQL(call, constructor.arity, bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		return sqlExpr{sql: constructor.sql + "(" + strings.Join(args, ", ") + ")", kind: kindGeometry}, nil
	}
	if local == "distance" {
		sql, err := distanceSQL(call, bindings)
		return sqlExpr{sql: sql, kind: kindNumeric}, err
	}
	return sqlExpr{}, fmt.Errorf("GeoSPARQL function geof:%s is not supported yet", local)
}

// distanceSQL translates geof:distance. The unit argument the spec requires is
// optional here and defaults to degrees, the unit of the stored coordinates;
// metres are answered by metreDistanceSQL.
func distanceSQL(call *rdflibsparql.FuncExpr, bindings map[string]sqlBinding) (string, error) {
	if len(call.Args) != 2 && len(call.Args) != 3 {
		return "", fmt.Errorf("geof:distance takes two geometries and an optional unit, got %d arguments", len(call.Args))
	}
	metres := false
	if len(call.Args) == 3 {
		unit, ok := call.Args[2].(*rdflibsparql.IRIExpr)
		if !ok {
			return "", fmt.Errorf("the unit of geof:distance must be a uom: IRI")
		}
		switch unit.Value {
		case uomNamespace + "degree":
		case uomNamespace + "metre":
			metres = true
		default:
			return "", fmt.Errorf("geof:distance unit %q is not supported; use uom:degree or uom:metre", unit.Value)
		}
	}
	args, err := geometryArgsSQL(&rdflibsparql.FuncExpr{Name: call.Name, Args: call.Args[:2]}, 2, bindings)
	if err != nil {
		return "", err
	}
	if metres {
		return metreDistanceSQL(args[0], args[1]), nil
	}
	return "ST_Distance(" + args[0] + ", " + args[1] + ")", nil
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

func geometryArgsSQL(call *rdflibsparql.FuncExpr, arity int, bindings map[string]sqlBinding) ([]string, error) {
	local, _ := geofLocalName(call.Name)
	if len(call.Args) != arity {
		return nil, fmt.Errorf("geof:%s takes %d geometries, got %d arguments", local, arity, len(call.Args))
	}
	args := make([]string, 0, arity)
	for _, arg := range call.Args {
		sql, err := geometryOperandSQL(arg, bindings)
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
func geometryOperandSQL(expr rdflibsparql.Expr, bindings map[string]sqlBinding) (string, error) {
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
		geometry, err := geoFunctionSQL(e, bindings)
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
