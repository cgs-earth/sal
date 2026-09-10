package sparql

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cgs-earth/sal/pkg"
	rdflibgo "github.com/tggo/goRDFlib"
	rdflibsparql "github.com/tggo/goRDFlib/sparql"
)

type sqlBinding struct {
	alias  string
	column string
}

// ToSQL converts a read-only SPARQL SELECT over supported triple patterns
// into SQL that runs against the DuckDB triples view.
func ToSQL(input string) (string, error) {
	return toSQL(input, tableSources{})
}

// tableSources decides which table a triple pattern scans: the runner's own
// snapshot for a pattern in the WHERE clause, or the snapshot the endpoint of
// a SERVICE clause names for a pattern inside one. Either is the `triples`
// view for the current snapshot and an iceberg_scan of the table at an
// earlier one, scanned inline rather than through a view of its own so the
// translated SQL says which snapshot it reads. Every pattern reads the same
// rows, so a property path over rdfs:subClassOf or rdfs:subPropertyOf walks
// exactly the hierarchy the project asserted or imported with owl:imports.
type tableSources struct {
	tablePath  string
	snapshotID int64
}

// source is the FROM expression a pattern scans.
func (s tableSources) source(service string) (string, error) {
	snapshotID := s.snapshotID
	if service != "" {
		id, err := serviceSnapshot(service)
		if err != nil {
			return "", err
		}
		snapshotID = id
	}
	if snapshotID == 0 {
		return TriplesView, nil
	}
	return snapshotScanSQL(s.tablePath, snapshotID), nil
}

// servicePath is the path of a SPARQL endpoint of this server: /sparql for the
// current snapshot, /v{snapshot}/sparql for an earlier one. It has to agree
// with the routes `sal serve` registers.
var servicePath = regexp.MustCompile(`^/(?:v(\d+)/)?sparql$`)

// serviceSnapshot reads the snapshot a SERVICE endpoint names, zero for the
// current one. Only this server's own endpoints are supported, and they are
// recognized by path alone, so the host a query writes them with does not
// matter; a SERVICE naming any other endpoint is an error rather than a
// federated query, since nothing here is fetched over HTTP.
func serviceSnapshot(service string) (int64, error) {
	endpoint, err := url.Parse(service)
	if err != nil {
		return 0, fmt.Errorf("SERVICE <%s> is not a URL: %w", service, err)
	}
	match := servicePath.FindStringSubmatch(endpoint.Path)
	if match == nil {
		return 0, fmt.Errorf("SERVICE <%s> is not a SPARQL endpoint of this server; only /sparql and /v<snapshot id>/sparql can be queried, which read the table at the current or the named Iceberg snapshot", service)
	}
	if match[1] == "" {
		return 0, nil
	}
	snapshotID, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil || snapshotID <= 0 {
		return 0, fmt.Errorf("SERVICE <%s> does not name a snapshot ID", service)
	}
	return snapshotID, nil
}

// serviceMarker heads the IRI a SERVICE endpoint reaches the translator in.
// goRDFlib rejects SERVICE outright but parses GRAPH, so rewriteService turns
// `SERVICE <iri>` into `GRAPH <urn:x-sal-service:iri>` before parsing and
// graphPatternParts reads the marked GraphPattern back as a service. A
// GRAPH clause a query wrote itself carries no marker and stays unsupported.
const serviceMarker = "urn:x-sal-service:"

// serviceClause matches `SERVICE [SILENT] <iri>`. The character before the
// keyword rules out a variable or prefixed name that happens to be called
// service, which in subject position is followed by a predicate IRI just as
// the keyword is followed by an endpoint.
var serviceClause = regexp.MustCompile(`(?i)(^|[^\w?$:])SERVICE\s+(?:SILENT\s+)?<([^<>\s]*)>`)

func rewriteService(input string) string {
	return serviceClause.ReplaceAllString(input, "${1}GRAPH <"+serviceMarker+"${2}>")
}

// patternTriple is a triple pattern with the SERVICE endpoint it was written
// under, empty for one in the query's own WHERE clause. A pattern written with
// a property path carries the step the path reduced to instead of a
// predicate, which says what its scan matches and in which direction.
type patternTriple struct {
	rdflibsparql.Triple
	service string
	step    *pathStep
}

// groupParts is a group graph pattern flattened into what the translator works
// from: its triple patterns, its FILTER expressions, and the groups a MINUS
// subtracts from it.
type groupParts struct {
	triples []patternTriple
	filters []rdflibsparql.Expr
	minuses []minusGroup
}

// minusGroup is the right side of a MINUS together with the variables its left
// side binds. The parser nests `{ A MINUS { B } C }` as Join(Minus(A, B), C), so
// B is subtracted from A's solutions alone and never sees C's variables;
// recording A's variables here is what keeps C out of the correlation once the
// whole query has been flattened.
type minusGroup struct {
	pattern  rdflibsparql.Pattern
	leftVars map[string]bool
}

// translator carries what every part of one query's translation needs: the
// query's prefixes, the tables its patterns scan, a count of the EXISTS
// subqueries written so far so that each one aliases its scans apart from the
// others and from the enclosing query, and a count of the hidden variables
// sequence paths have been chained through.
type translator struct {
	prefixes   map[string]string
	sources    tableSources
	subqueries int
	hidden     int
}

// patternSQL is what a list of triple patterns translates to: one aliased scan
// per pattern in from, their constants and joins in where, the variable each
// alias and column binds, and the variables in the order they were first seen,
// which is what SELECT * projects. correlated reports whether any variable was
// already bound by an enclosing query, which a MINUS needs to know.
type patternSQL struct {
	from       []string
	where      []string
	bindings   map[string]sqlBinding
	discovered []string
	correlated bool
}

// toSQL is ToSQL with each triple pattern scanning the table sources decides
// on: the runner's own snapshot, or the one a SERVICE endpoint names. Every
// triple pattern becomes one aliased scan of its source, joined to the others,
// so a pattern under a SERVICE joins the rest of the query the same way any
// other pattern does, which is what lets one query compare two snapshots. A
// MINUS or a FILTER [NOT] EXISTS becomes an EXISTS subquery correlated to the
// enclosing query, which is what lets one query diff two snapshots.
func toSQL(input string, sources tableSources) (string, error) {
	parsed, err := rdflibsparql.Parse(rewriteService(input))
	if err != nil {
		if strings.Contains(err.Error(), "SERVICE not supported") {
			return "", fmt.Errorf("SERVICE must name its endpoint as a full IRI in angle brackets, such as SERVICE <http://localhost:8080/v<snapshot id>/sparql> { ... }")
		}
		return "", fmt.Errorf("parse SPARQL query: %w", err)
	}
	if parsed.Type != "SELECT" {
		return "", fmt.Errorf("only read-only SPARQL SELECT queries are supported")
	}
	if len(parsed.GroupBy) > 0 || parsed.Having != nil || len(parsed.OrderBy) > 0 || parsed.Offset > 0 {
		return "", fmt.Errorf("SPARQL solution modifiers are not supported yet")
	}

	t := &translator{prefixes: parsed.Prefixes, sources: sources}
	parts, err := t.graphPatternParts(parsed.Where)
	if err != nil {
		return "", err
	}
	if len(parts.triples) == 0 {
		return "", fmt.Errorf("SPARQL query must include at least one triple pattern")
	}

	patterns, err := t.patternSQL(parts.triples, "t", nil)
	if err != nil {
		return "", err
	}
	from, where, bindings := patterns.from, patterns.where, patterns.bindings
	clauses, err := t.clauseSQL(parts, bindings)
	if err != nil {
		return "", err
	}
	where = append(where, clauses...)

	projected := parsed.Variables
	if len(projected) == 0 {
		projected = patterns.discovered
	}
	if len(projected) == 0 {
		return "", fmt.Errorf("SPARQL SELECT must project at least one variable")
	}
	// A projection expression, (expr AS ?name), is listed among the variables
	// under the name it binds; the expression itself is what is projected.
	projectExprs := make(map[string]rdflibsparql.Expr, len(parsed.ProjectExprs))
	for _, projectExpr := range parsed.ProjectExprs {
		projectExprs[projectExpr.Var] = projectExpr.Expr
	}
	selects := make([]string, 0, len(projected))
	for _, name := range projected {
		if binding, ok := bindings[name]; ok {
			selects = append(selects, binding.projection()+" AS "+quoteIdent(name))
			continue
		}
		expr, ok := projectExprs[name]
		if !ok {
			return "", fmt.Errorf("projected variable ?%s is not bound by a supported triple pattern", name)
		}
		call, ok := expr.(*rdflibsparql.FuncExpr)
		if !ok {
			return "", fmt.Errorf("SPARQL projection expressions other than a function call, such as (LANG(?o) AS ?%s), are not supported yet", name)
		}
		sql, _, err := functionSQL(call, bindings)
		if err != nil {
			return "", err
		}
		selects = append(selects, sql+" AS "+quoteIdent(name))
	}

	sql := "SELECT " + strings.Join(selects, ", ") + "\nFROM " + strings.Join(from, "\nCROSS JOIN ")
	if len(where) > 0 {
		sql += "\nWHERE " + strings.Join(where, "\n  AND ")
	}
	if parsed.Distinct {
		sql = strings.Replace(sql, "SELECT ", "SELECT DISTINCT ", 1)
	}
	if parsed.Limit >= 0 {
		sql += "\nLIMIT " + strconv.Itoa(parsed.Limit)
	}
	return sql, nil
}

// patternSQL translates triple patterns into aliased scans, numbered from zero
// under aliasPrefix. A variable seen before in the same list becomes a join; one
// bound by an enclosing query, in outer, becomes a correlation instead, so that
// a MINUS or EXISTS group is evaluated per solution of the query it belongs to.
// A path step scans the same source with its subject and object columns
// swapped for an inverse, and a repeated step scans the closure of its edges
// instead, a derived table whose start and finish the pattern's ends bind.
func (t *translator) patternSQL(triples []patternTriple, aliasPrefix string, outer map[string]sqlBinding) (patternSQL, error) {
	result := patternSQL{bindings: make(map[string]sqlBinding)}
	for i, triple := range triples {
		alias := fmt.Sprintf("%s%d", aliasPrefix, i)
		source, err := t.sources.source(triple.service)
		if err != nil {
			return patternSQL{}, err
		}
		type part struct {
			term   string
			column string
		}
		parts := []part{{triple.Subject, "subject"}, {triple.Predicate, "predicate"}, {triple.Object, "object"}}
		switch {
		case triple.step == nil:
			result.from = append(result.from, source+" AS "+alias)
		case triple.step.closure():
			var identity string
			var lateral bool
			// only a zero-length match needs to know what it is the identity of
			if triple.step.zero {
				if identity, lateral, err = t.closureIdentity(triple, result.bindings, outer); err != nil {
					return patternSQL{}, err
				}
			}
			from := closureSQL(source, *triple.step, identity) + " AS " + alias
			if lateral {
				from = "LATERAL " + from
			}
			result.from = append(result.from, from)
			parts = []part{{triple.Subject, "start"}, {triple.Object, "finish"}}
		default:
			result.from = append(result.from, source+" AS "+alias)
			result.where = append(result.where, predicateClause(alias, *triple.step))
			parts = []part{{triple.Subject, "subject"}, {triple.Object, "object"}}
			if triple.step.inverse {
				parts = []part{{triple.Subject, "object"}, {triple.Object, "subject"}}
			}
		}
		var correlations []correlation
		for _, part := range parts {
			if name := variableName(part.term); name != "" {
				binding := sqlBinding{alias: alias, column: part.column}
				if previous, ok := result.bindings[name]; ok {
					result.where = append(result.where, previous.expr()+" = "+binding.expr())
				} else if enclosing, ok := outer[name]; ok {
					result.bindings[name] = binding
					result.correlated = true
					correlations = append(correlations, correlation{inner: binding, outer: enclosing})
				} else {
					result.bindings[name] = binding
					if !isHiddenVariable(name) {
						result.discovered = append(result.discovered, name)
					}
				}
				continue
			}
			clauses, err := constantClauses(alias, part.column, part.term, t.prefixes)
			if err != nil {
				return patternSQL{}, err
			}
			result.where = append(result.where, clauses...)
		}
		result.where = append(result.where, correlationSQL(alias, correlations)...)
	}
	return result, nil
}

// closureIdentity is what a zero-length path match is the identity of: the
// endpoint the pattern already has a value for, as a constant or as the
// expression an earlier scan or the enclosing query binds it to. lateral
// reports that the expression names another scan, which the derived table
// then has to be joined LATERAL to see. Nothing bound is "", and the closure
// falls back to the nodes its edges touch.
func (t *translator) closureIdentity(triple patternTriple, bindings map[string]sqlBinding, outer map[string]sqlBinding) (identity string, lateral bool, err error) {
	for _, end := range []string{triple.Subject, triple.Object} {
		if name := variableName(end); name != "" {
			if binding, ok := bindings[name]; ok {
				return binding.expr(), true, nil
			}
			if binding, ok := outer[name]; ok {
				return binding.expr(), true, nil
			}
			continue
		}
		term, err := parseSPARQLTerm(end, t.prefixes)
		if err != nil {
			return "", false, err
		}
		return sqlString(term.value), false, nil
	}
	return "", false, nil
}

// correlation ties a variable a subquery's pattern binds to the binding the
// enclosing query already has for it.
type correlation struct {
	inner sqlBinding
	outer sqlBinding
}

// correlationSQL is the clauses that tie one subquery scan to the enclosing
// query. A pattern whose subject, predicate, and object all correlate to the
// same positions of one enclosing scan asks whether that exact triple exists,
// which triple_hash answers in one comparison, exactly and without rendering
// any object. Anything else is correlated position by position: subject and
// predicate by column, and an object by the RDF term it holds, so that a
// literal differing only in its language tag or datatype, or a geometry, which
// renders as NULL everywhere but ST_AsText, still compares as a different term.
func correlationSQL(alias string, correlations []correlation) []string {
	if len(correlations) == 3 {
		sameScan := true
		for _, c := range correlations {
			if c.inner.column != c.outer.column || c.outer.alias != correlations[0].outer.alias {
				sameScan = false
			}
		}
		if sameScan {
			return []string{alias + ".triple_hash = " + correlations[0].outer.alias + ".triple_hash"}
		}
	}
	var clauses []string
	for _, c := range correlations {
		if c.inner.column != "object" || c.outer.column != "object" {
			clauses = append(clauses, c.inner.expr()+" = "+c.outer.expr())
			continue
		}
		inner, outer := c.inner.alias, c.outer.alias
		clauses = append(clauses,
			objectTextExpr(inner)+" IS NOT DISTINCT FROM "+objectTextExpr(outer),
			inner+".object_language IS NOT DISTINCT FROM "+outer+".object_language",
			inner+".object_type IS NOT DISTINCT FROM "+outer+".object_type")
	}
	return clauses
}

// clauseSQL translates a group's FILTERs and MINUSes against the variables the
// group binds. A MINUS is subtracted only through the variables its own left
// side binds, and one sharing no variable with it subtracts nothing at all, as
// SPARQL specifies; NOT EXISTS, by contrast, is written by the query as a
// filter and is correlated on everything in scope.
func (t *translator) clauseSQL(parts groupParts, bindings map[string]sqlBinding) ([]string, error) {
	var where []string
	for _, filter := range parts.filters {
		clause, err := t.filterSQL(filter, bindings)
		if err != nil {
			return nil, err
		}
		where = append(where, clause)
	}
	for _, minus := range parts.minuses {
		left := make(map[string]sqlBinding, len(minus.leftVars))
		for name := range minus.leftVars {
			if binding, ok := bindings[name]; ok {
				left[name] = binding
			}
		}
		sql, correlated, err := t.subquerySQL(minus.pattern, left, false)
		if err != nil {
			return nil, err
		}
		if correlated {
			where = append(where, "NOT "+sql)
		}
	}
	return where, nil
}

// subquerySQL translates a MINUS or EXISTS group into an EXISTS subquery
// correlated to the enclosing query through outer, the bindings the group is
// evaluated against. Filters inside the group see the enclosing query's
// variables only when seesOuter is set: an EXISTS is evaluated with the
// enclosing solution substituted in, a MINUS group is evaluated on its own.
// correlated reports whether the group shares a variable with outer at all.
func (t *translator) subquerySQL(pattern rdflibsparql.Pattern, outer map[string]sqlBinding, seesOuter bool) (sql string, correlated bool, err error) {
	parts, err := t.graphPatternParts(pattern)
	if err != nil {
		return "", false, err
	}
	if len(parts.triples) == 0 {
		return "", false, fmt.Errorf("a MINUS or EXISTS group must include at least one triple pattern")
	}
	prefix := fmt.Sprintf("x%d_", t.subqueries)
	t.subqueries++
	patterns, err := t.patternSQL(parts.triples, prefix, outer)
	if err != nil {
		return "", false, err
	}
	scope := patterns.bindings
	if seesOuter {
		scope = make(map[string]sqlBinding, len(outer)+len(patterns.bindings))
		for name, binding := range outer {
			scope[name] = binding
		}
		for name, binding := range patterns.bindings {
			scope[name] = binding
		}
	}
	clauses, err := t.clauseSQL(parts, scope)
	if err != nil {
		return "", false, err
	}
	where := append(patterns.where, clauses...)
	sql = "EXISTS (SELECT 1\n  FROM " + strings.Join(patterns.from, "\n  CROSS JOIN ")
	if len(where) > 0 {
		sql += "\n  WHERE " + strings.Join(where, "\n    AND ")
	}
	return sql + ")", patterns.correlated, nil
}

func (b sqlBinding) expr() string {
	return bindingExpr(b.alias, b.column)
}

// projection is the expression a bound variable is read back through. It
// differs from expr only for an object, where a geometry is rendered as WKT so
// that a geometry-valued object is not projected as empty. Joins and filters
// keep using expr, so a query that never projects an object never pulls in the
// spatial extension.
func (b sqlBinding) projection() string {
	if b.column != "object" {
		return b.expr()
	}
	return objectTextExpr(b.alias)
}

// objectTextExpr renders the object union, geometry included, as text.
func objectTextExpr(alias string) string {
	return "COALESCE(" + objectScalarTextExprs(alias) + ", ST_AsText(" + alias + ".object_geometry))"
}

// bindingExpr is the column a subject or predicate is read from, or for an
// object, its union of typed columns rendered as text, geometry left out since
// comparing one as text is never what a query means.
func bindingExpr(alias string, column string) string {
	if column != "object" {
		return alias + "." + column
	}
	return "COALESCE(" + objectScalarTextExprs(alias) + ")"
}

// objectScalarTextExprs lists every non-geometry object column rendered as
// text, for the COALESCE that reads an object back whichever column holds it.
func objectScalarTextExprs(alias string) string {
	return alias + ".object_iri, CAST(" + alias + ".object_float AS VARCHAR), CAST(" + alias + ".object_integer AS VARCHAR), CAST(" + alias + ".object_byte AS VARCHAR), " + timeTextExpr(alias) + ", " + alias + ".object_string"
}

// timeTextExpr renders object_time back as the xsd:dateTime lexical form it
// was built from: DuckDB casts a timestamp to "YYYY-MM-DD HH:MM:SS[.ffffff]",
// and build stores every value normalized to UTC, so swapping the space for a
// 'T' and appending 'Z' restores the ISO form.
func timeTextExpr(alias string) string {
	return "replace(CAST(" + alias + ".object_time AS VARCHAR), ' ', 'T') || 'Z'"
}

// objectNumericExpr reads whichever numeric column holds an object, so a
// numeric comparison matches a value whether it was typed as a float, an
// integer, or a byte.
func objectNumericExpr(alias string) string {
	return "COALESCE(" + alias + ".object_float, CAST(" + alias + ".object_integer AS DOUBLE), CAST(" + alias + ".object_byte AS DOUBLE))"
}

// graphPatternParts flattens a group graph pattern into groupParts. A triple
// pattern written with a property path is replaced by the patterns pathSteps
// reduces it to.
func (t *translator) graphPatternParts(pattern rdflibsparql.Pattern) (groupParts, error) {
	switch p := pattern.(type) {
	case *rdflibsparql.BGP:
		parts := groupParts{triples: make([]patternTriple, 0, len(p.Triples))}
		for _, triple := range p.Triples {
			if triple.PredicatePath == nil {
				parts.triples = append(parts.triples, patternTriple{Triple: triple})
				continue
			}
			steps, err := t.pathSteps(triple.Subject, triple.PredicatePath, triple.Object, false)
			if err != nil {
				return groupParts{}, err
			}
			parts.triples = append(parts.triples, steps...)
		}
		return parts, nil
	case *rdflibsparql.GraphPattern:
		service, ok := strings.CutPrefix(strings.Trim(p.Name, "<>"), serviceMarker)
		if !ok {
			return groupParts{}, fmt.Errorf("only basic SPARQL triple patterns and FILTER expressions are supported yet")
		}
		parts, err := t.graphPatternParts(p.Pattern)
		if err != nil {
			return groupParts{}, err
		}
		for i := range parts.triples {
			// A SERVICE nested in another keeps its own endpoint.
			if parts.triples[i].service == "" {
				parts.triples[i].service = service
			}
		}
		// A MINUS or EXISTS group written inside the SERVICE is read from the
		// same endpoint. Its patterns are translated later, so the endpoint is
		// carried by wrapping the group in the GRAPH it was found under.
		for i := range parts.minuses {
			parts.minuses[i].pattern = &rdflibsparql.GraphPattern{Name: p.Name, Pattern: parts.minuses[i].pattern}
		}
		for i := range parts.filters {
			parts.filters[i] = scopeExists(parts.filters[i], p.Name)
		}
		return parts, nil
	case *rdflibsparql.JoinPattern:
		left, err := t.graphPatternParts(p.Left)
		if err != nil {
			return groupParts{}, err
		}
		right, err := t.graphPatternParts(p.Right)
		if err != nil {
			return groupParts{}, err
		}
		return groupParts{
			triples: append(left.triples, right.triples...),
			filters: append(left.filters, right.filters...),
			minuses: append(left.minuses, right.minuses...),
		}, nil
	case *rdflibsparql.FilterPattern:
		parts, err := t.graphPatternParts(p.Pattern)
		if err != nil {
			return groupParts{}, err
		}
		parts.filters = append(parts.filters, p.Expr)
		return parts, nil
	case *rdflibsparql.MinusPattern:
		left, err := t.graphPatternParts(p.Left)
		if err != nil {
			return groupParts{}, err
		}
		leftVars := make(map[string]bool)
		for _, triple := range left.triples {
			for _, term := range []string{triple.Subject, triple.Predicate, triple.Object} {
				if name := variableName(term); name != "" {
					leftVars[name] = true
				}
			}
		}
		left.minuses = append(left.minuses, minusGroup{pattern: p.Right, leftVars: leftVars})
		return left, nil
	default:
		return groupParts{}, fmt.Errorf("only basic SPARQL triple patterns and FILTER expressions are supported yet")
	}
}

// scopeExists rewrites every EXISTS in a filter expression so that its group
// is read from the named GRAPH, which is how a SERVICE carries its endpoint to
// a group nested inside it.
func scopeExists(expr rdflibsparql.Expr, graph string) rdflibsparql.Expr {
	switch e := expr.(type) {
	case *rdflibsparql.ExistsExpr:
		return &rdflibsparql.ExistsExpr{Pattern: &rdflibsparql.GraphPattern{Name: graph, Pattern: e.Pattern}, Not: e.Not}
	case *rdflibsparql.BinaryExpr:
		return &rdflibsparql.BinaryExpr{Op: e.Op, Left: scopeExists(e.Left, graph), Right: scopeExists(e.Right, graph)}
	case *rdflibsparql.UnaryExpr:
		return &rdflibsparql.UnaryExpr{Op: e.Op, Arg: scopeExists(e.Arg, graph)}
	default:
		return expr
	}
}

func (t *translator) filterSQL(expr rdflibsparql.Expr, bindings map[string]sqlBinding) (string, error) {
	switch e := expr.(type) {
	case *rdflibsparql.BinaryExpr:
		if e.Op == "&&" || e.Op == "||" {
			left, err := t.filterSQL(e.Left, bindings)
			if err != nil {
				return "", err
			}
			right, err := t.filterSQL(e.Right, bindings)
			if err != nil {
				return "", err
			}
			op := "AND"
			if e.Op == "||" {
				op = "OR"
			}
			return "(" + left + " " + op + " " + right + ")", nil
		}
		if !supportedFilterComparison(e.Op) {
			return "", fmt.Errorf("SPARQL FILTER operator %q is not supported yet", e.Op)
		}
		left, err := filterOperandSQL(e.Left, e.Right, bindings)
		if err != nil {
			return "", err
		}
		right, err := filterOperandSQL(e.Right, e.Left, bindings)
		if err != nil {
			return "", err
		}
		return left + " " + e.Op + " " + right, nil
	case *rdflibsparql.UnaryExpr:
		if e.Op != "!" {
			return "", fmt.Errorf("SPARQL FILTER operator %q is not supported yet", e.Op)
		}
		inner, err := t.filterSQL(e.Arg, bindings)
		if err != nil {
			return "", err
		}
		return "NOT (" + inner + ")", nil
	case *rdflibsparql.ExistsExpr:
		sql, _, err := t.subquerySQL(e.Pattern, bindings, true)
		if err != nil {
			return "", err
		}
		if e.Not {
			return "NOT " + sql, nil
		}
		return sql, nil
	case *rdflibsparql.FuncExpr:
		sql, boolean, err := functionSQL(e, bindings)
		if err != nil {
			return "", err
		}
		if !boolean {
			return "", fmt.Errorf("a FILTER on %s must compare it against something", sql)
		}
		return sql, nil
	default:
		return "", fmt.Errorf("only binary SPARQL FILTER expressions are supported yet")
	}
}

func supportedFilterComparison(op string) bool {
	switch op {
	case "=", "!=", "<", ">", "<=", ">=":
		return true
	default:
		return false
	}
}

func filterOperandSQL(expr rdflibsparql.Expr, other rdflibsparql.Expr, bindings map[string]sqlBinding) (string, error) {
	switch e := expr.(type) {
	case *rdflibsparql.VarExpr:
		binding, ok := bindings[e.Name]
		if !ok {
			return "", fmt.Errorf("FILTER variable ?%s is not bound by a supported triple pattern", e.Name)
		}
		// An object is compared in the column the other side's kind names, so a
		// number compares as a number rather than as the text it renders to.
		if binding.column == "object" {
			if literal, ok := other.(*rdflibsparql.LiteralExpr); ok {
				if _, ok := timestampSQL(literal.Value); ok {
					return binding.alias + ".object_time", nil
				}
				if literalIsNumeric(literal.Value) {
					return objectNumericExpr(binding.alias), nil
				}
				return binding.alias + ".object_string", nil
			}
			if _, ok := other.(*rdflibsparql.IRIExpr); ok {
				return binding.alias + ".object_iri", nil
			}
			// Two variables are compared as the text each renders to, so that
			// an IRI, a number, or a date compares as well as a string does.
			if _, ok := other.(*rdflibsparql.VarExpr); ok {
				return binding.expr(), nil
			}
			// The only function a variable can be compared against is geof:distance.
			if _, ok := other.(*rdflibsparql.FuncExpr); ok {
				return objectNumericExpr(binding.alias), nil
			}
			return binding.alias + ".object_string", nil
		}
		return binding.expr(), nil
	case *rdflibsparql.LiteralExpr:
		return termSQL(e.Value), nil
	case *rdflibsparql.IRIExpr:
		return sqlString(e.Value), nil
	case *rdflibsparql.FuncExpr:
		sql, boolean, err := functionSQL(e, bindings)
		if err != nil {
			return "", err
		}
		if boolean {
			return "", fmt.Errorf("%s is a boolean; use it in the FILTER directly rather than comparing it", sql)
		}
		return sql, nil
	default:
		return "", fmt.Errorf("unsupported SPARQL FILTER operand %T", expr)
	}
}

// literalIsNumeric reports whether a literal compares against the numeric
// object columns: its lexical form parses as a number and its datatype, when
// it carries one, is a numeric XSD type rather than, say, xsd:string.
func literalIsNumeric(term rdflibgo.Term) bool {
	if _, err := strconv.ParseFloat(term.String(), 64); err != nil {
		return false
	}
	if literal, ok := term.(rdflibgo.Literal); ok {
		datatype := literal.Datatype().Value()
		return datatype == "" || pkg.IsXSDNumericType(datatype)
	}
	return true
}

// timestampSQL renders an xsd:dateTime literal as the TIMESTAMP its value is
// stored as: parsed with its explicit timezone and normalized to UTC, matching
// how build writes object_time. A dateTime build would have stored as a string
// (no timezone, or beyond microsecond precision) is not rendered as one.
func timestampSQL(term rdflibgo.Term) (string, bool) {
	literal, ok := term.(rdflibgo.Literal)
	if !ok || literal.Datatype().Value() != pkg.XSDDateTime {
		return "", false
	}
	parsed, err := time.Parse(time.RFC3339Nano, literal.Lexical())
	if err != nil || parsed.Nanosecond()%1000 != 0 {
		return "", false
	}
	return "TIMESTAMP '" + parsed.UTC().Format("2006-01-02 15:04:05.999999") + "'", true
}

func termSQL(term rdflibgo.Term) string {
	if sql, ok := timestampSQL(term); ok {
		return sql
	}
	if literalIsNumeric(term) {
		return term.String()
	}
	return sqlString(term.String())
}

func constantClauses(alias string, column string, raw string, prefixes map[string]string) ([]string, error) {
	term, err := parseSPARQLTerm(raw, prefixes)
	if err != nil {
		return nil, err
	}
	if column != "object" {
		return []string{alias + "." + column + " = " + sqlString(term.value)}, nil
	}
	switch term.kind {
	case "iri":
		return []string{alias + ".object_iri = " + sqlString(term.value)}, nil
	case "literal":
		literal := rdflibgo.NewLiteral(term.value, rdflibgo.WithDatatype(rdflibgo.NewURIRefUnsafe(term.datatype)))
		if timestamp, ok := timestampSQL(literal); ok {
			return []string{alias + ".object_time = " + timestamp}, nil
		}
		if literalIsNumeric(literal) {
			return []string{objectNumericExpr(alias) + " = " + term.value}, nil
		}
		clauses := []string{alias + ".object_string = " + sqlString(term.value)}
		if term.language != "" {
			// tags are stored lower case, see the object_language column
			clauses = append(clauses, alias+".object_language = "+sqlString(strings.ToLower(term.language)))
		}
		return clauses, nil
	default:
		return nil, fmt.Errorf("unsupported object term %q", raw)
	}
}

type sparqlTerm struct {
	kind     string
	value    string
	datatype string
	language string
}

func parseSPARQLTerm(raw string, prefixes map[string]string) (sparqlTerm, error) {
	if strings.HasPrefix(raw, "<") && strings.HasSuffix(raw, ">") {
		return sparqlTerm{kind: "iri", value: raw[1 : len(raw)-1]}, nil
	}
	if strings.HasPrefix(raw, "\"") || strings.HasPrefix(raw, "'") {
		literal, datatype, language, err := parseLiteral(raw, prefixes)
		return sparqlTerm{kind: "literal", value: literal, datatype: datatype, language: language}, err
	}
	if raw == "true" || raw == "false" {
		return sparqlTerm{kind: "literal", value: raw, datatype: rdflibgo.XSDBoolean.Value()}, nil
	}
	if _, err := strconv.ParseFloat(raw, 64); err == nil {
		return sparqlTerm{kind: "literal", value: raw, datatype: rdflibgo.XSDDouble.Value()}, nil
	}
	if idx := strings.Index(raw, ":"); idx >= 0 {
		prefix, local := raw[:idx], raw[idx+1:]
		namespace, ok := prefixes[prefix]
		if !ok {
			return sparqlTerm{}, fmt.Errorf("unknown SPARQL prefix %q", prefix)
		}
		return sparqlTerm{kind: "iri", value: namespace + local}, nil
	}
	return sparqlTerm{}, fmt.Errorf("unsupported SPARQL term %q", raw)
}

// parseLiteral splits a quoted literal into its value and either the datatype
// it is typed with or the language tag it carries. An untagged, untyped
// literal is an xsd:string; a tagged one is an rdf:langString.
func parseLiteral(raw string, prefixes map[string]string) (value string, datatype string, language string, err error) {
	quote := raw[0]
	end := 1
	escaped := false
	for end < len(raw) {
		if escaped {
			escaped = false
			end++
			continue
		}
		if raw[end] == '\\' {
			escaped = true
			end++
			continue
		}
		if raw[end] == quote {
			break
		}
		end++
	}
	if end >= len(raw) {
		return "", "", "", fmt.Errorf("invalid SPARQL literal %q", raw)
	}
	value, err = strconv.Unquote(raw[:end+1])
	if err != nil {
		return "", "", "", fmt.Errorf("invalid SPARQL literal %q: %w", raw, err)
	}
	datatype = rdflibgo.XSDString.Value()
	rest := raw[end+1:]
	if strings.HasPrefix(rest, "@") {
		return value, rdflibgo.RDFLangString.Value(), rest[1:], nil
	}
	if strings.HasPrefix(rest, "^^<") && strings.HasSuffix(rest, ">") {
		datatype = rest[3 : len(rest)-1]
	} else if strings.HasPrefix(rest, "^^") {
		prefixed := rest[2:]
		idx := strings.Index(prefixed, ":")
		if idx < 0 {
			return "", "", "", fmt.Errorf("invalid SPARQL datatype %q", rest)
		}
		namespace, ok := prefixes[prefixed[:idx]]
		if !ok {
			return "", "", "", fmt.Errorf("unknown SPARQL prefix %q", prefixed[:idx])
		}
		datatype = namespace + prefixed[idx+1:]
	}
	return value, datatype, "", nil
}

func variableName(raw string) string {
	if strings.HasPrefix(raw, "?") || strings.HasPrefix(raw, "$") {
		return raw[1:]
	}
	return ""
}

func sqlString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func quoteIdent(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
