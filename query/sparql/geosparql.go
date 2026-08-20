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

// geoFunctionSQL translates a geof: function call. boolean reports whether the
// result is a truth value, which is what decides whether the call can stand on
// its own in a FILTER or has to be compared against something.
func geoFunctionSQL(call *rdflibsparql.FuncExpr, bindings map[string]sqlBinding, layout ObjectLayout) (sql string, boolean bool, err error) {
	local, ok := geofLocalName(call.Name)
	if !ok {
		return "", false, fmt.Errorf("SPARQL function %q is not supported yet", strings.ToLower(call.Name))
	}
	if relation, ok := geofRelations[local]; ok {
		args, err := geometryArgsSQL(call, 2, bindings, layout)
		if err != nil {
			return "", false, err
		}
		return relation + "(" + strings.Join(args, ", ") + ")", true, nil
	}
	if constructor, ok := geofConstructors[local]; ok {
		args, err := geometryArgsSQL(call, constructor.arity, bindings, layout)
		if err != nil {
			return "", false, err
		}
		return constructor.sql + "(" + strings.Join(args, ", ") + ")", false, nil
	}
	if local == "distance" {
		sql, err := distanceSQL(call, bindings, layout)
		return sql, false, err
	}
	return "", false, fmt.Errorf("GeoSPARQL function geof:%s is not supported yet", local)
}

// distanceSQL translates geof:distance. The unit argument the spec requires is
// optional here and defaults to degrees, the unit of the stored coordinates;
// metres are answered with the spherical distance, which DuckDB only computes
// between points.
func distanceSQL(call *rdflibsparql.FuncExpr, bindings map[string]sqlBinding, layout ObjectLayout) (string, error) {
	if len(call.Args) != 2 && len(call.Args) != 3 {
		return "", fmt.Errorf("geof:distance takes two geometries and an optional unit, got %d arguments", len(call.Args))
	}
	function := "ST_Distance"
	if len(call.Args) == 3 {
		unit, ok := call.Args[2].(*rdflibsparql.IRIExpr)
		if !ok {
			return "", fmt.Errorf("the unit of geof:distance must be a uom: IRI")
		}
		switch unit.Value {
		case uomNamespace + "degree":
		case uomNamespace + "metre":
			function = "ST_Distance_Sphere"
		default:
			return "", fmt.Errorf("geof:distance unit %q is not supported; use uom:degree or uom:metre", unit.Value)
		}
	}
	args, err := geometryArgsSQL(&rdflibsparql.FuncExpr{Name: call.Name, Args: call.Args[:2]}, 2, bindings, layout)
	if err != nil {
		return "", err
	}
	return function + "(" + strings.Join(args, ", ") + ")", nil
}

func geometryArgsSQL(call *rdflibsparql.FuncExpr, arity int, bindings map[string]sqlBinding, layout ObjectLayout) ([]string, error) {
	local, _ := geofLocalName(call.Name)
	if len(call.Args) != arity {
		return nil, fmt.Errorf("geof:%s takes %d geometries, got %d arguments", local, arity, len(call.Args))
	}
	args := make([]string, 0, arity)
	for _, arg := range call.Args {
		sql, err := geometryOperandSQL(arg, bindings, layout)
		if err != nil {
			return nil, err
		}
		args = append(args, sql)
	}
	return args, nil
}

// geometryOperandSQL translates one geometry argument: a variable bound in
// object position, a WKT literal, or a nested geometry-valued geof: call.
func geometryOperandSQL(expr rdflibsparql.Expr, bindings map[string]sqlBinding, layout ObjectLayout) (string, error) {
	switch e := expr.(type) {
	case *rdflibsparql.VarExpr:
		binding, ok := bindings[e.Name]
		if !ok {
			return "", fmt.Errorf("FILTER variable ?%s is not bound by a supported triple pattern", e.Name)
		}
		if binding.column != "object" {
			return "", fmt.Errorf("?%s is bound as a %s, not as a geometry literal", e.Name, binding.column)
		}
		// The typed layout stores a WKT literal as a geometry already; the
		// simple layout keeps its text, which has to be parsed on every row.
		if layout == TypedObjects {
			return binding.alias + ".object_geometry", nil
		}
		return "ST_GeomFromText(" + binding.alias + ".object)", nil
	case *rdflibsparql.LiteralExpr:
		wkt, err := wktFromLiteral(e.Value)
		if err != nil {
			return "", err
		}
		return "ST_GeomFromText(" + sqlString(wkt) + ")", nil
	case *rdflibsparql.FuncExpr:
		sql, boolean, err := geoFunctionSQL(e, bindings, layout)
		if err != nil {
			return "", err
		}
		if boolean {
			return "", fmt.Errorf("geof:%s yields a boolean, not a geometry", strings.ToLower(strings.TrimPrefix(strings.ToLower(e.Name), geofNamespace)))
		}
		return sql, nil
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
