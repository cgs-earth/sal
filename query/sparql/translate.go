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

// sqlBinding is where a variable's value is read from. A variable a triple
// pattern binds reads a column of its scan: alias is the scan and column is
// subject, predicate, or object, whose value is spread over the typed object
// columns. A variable a UNION or a subquery binds reads the derived table
// those project, under columns named after the variable: alias is the derived
// table, name the variable, and column what it was projected as, a subject or
// predicate, an object with all of its typed columns, or valueColumn, the
// term rendered to text when the arms of a UNION disagree on its position or
// a subquery or VALUES computed it. A variable a BIND binds is its expression,
// in sql, and reads no table. nullable marks a variable a solution can leave
// unbound: one bound inside an OPTIONAL, in some arms of a UNION but not all,
// or by a VALUES with an UNDEF.
type sqlBinding struct {
	alias    string
	column   string
	name     string
	sql      string
	kind     exprKind
	nullable bool
}

// valueColumn is the column kind of a variable held as text.
const valueColumn = "value"

// typedObject reports whether the binding has the typed object columns.
func (b sqlBinding) typedObject() bool {
	return b.column == "object" && b.sql == ""
}

// scan reports whether the binding reads a scan of the table directly.
func (b sqlBinding) scan() bool {
	return b.name == "" && b.sql == ""
}

// col is one of the typed object columns of the binding: the scan's own, or
// the one a derived table projected it under.
func (b sqlBinding) col(column string) string {
	if b.name != "" {
		return b.alias + "." + quoteIdent(b.name+"."+column)
	}
	return b.alias + "." + column
}

// expr is the value the variable is compared and joined by: the column a
// subject or predicate is read from, or for an object, its union of typed
// columns rendered as text, geometry left out since comparing one as text is
// never what a query means.
func (b sqlBinding) expr() string {
	switch {
	case b.sql != "":
		return b.sql
	case b.column == "object":
		return "COALESCE(" + objectScalarTextExprs(b) + ")"
	case b.name != "":
		return b.alias + "." + quoteIdent(b.name)
	default:
		return b.alias + "." + b.column
	}
}

// projection is the expression a bound variable is read back through. It
// differs from expr only for an object, where a geometry is rendered as WKT so
// that a geometry-valued object is not projected as empty. Joins and filters
// keep using expr, so a query that never projects an object never pulls in the
// spatial extension.
func (b sqlBinding) projection() string {
	if !b.typedObject() {
		return b.expr()
	}
	return "COALESCE(" + objectScalarTextExprs(b) + ", ST_AsText(" + b.col("object_geometry") + "))"
}

// present is an expression that is NULL exactly when the variable is unbound,
// which BOUND and COUNT ask. Every stored object fills object_iri or carries
// an object_type, a blank node excepted, which fills object_string.
func (b sqlBinding) present() string {
	if !b.typedObject() {
		return b.expr()
	}
	return "COALESCE(" + b.col("object_iri") + ", " + b.col("object_type") + ", " + b.col("object_string") + ")"
}

// numeric reads whichever numeric column holds an object, so a numeric
// comparison matches a value whether it was typed as a float, an integer, or
// a byte; a value held as text is cast, NULL when it is not a number.
func (b sqlBinding) numeric() string {
	if !b.typedObject() {
		return "TRY_CAST(" + b.expr() + " AS DOUBLE)"
	}
	return "COALESCE(" + b.col("object_float") + ", CAST(" + b.col("object_integer") + " AS DOUBLE), CAST(" + b.col("object_byte") + " AS DOUBLE))"
}

// objectBinding is the binding of the object column of a scan, for the SQL
// elsewhere in this package that reads an object from a named scan.
func objectBinding(alias string) sqlBinding {
	return sqlBinding{alias: alias, column: "object"}
}

// bindingExpr is sqlBinding.expr for a column of a named scan.
func bindingExpr(alias string, column string) string {
	return sqlBinding{alias: alias, column: column}.expr()
}

// objectTextExpr renders the object union of a named scan, geometry
// included, as text.
func objectTextExpr(alias string) string {
	return objectBinding(alias).projection()
}

// objectNumericExpr is sqlBinding.numeric for the object of a named scan.
func objectNumericExpr(alias string) string {
	return objectBinding(alias).numeric()
}

// objectScalarTextExprs lists every non-geometry object column rendered as
// text, for the COALESCE that reads an object back whichever column holds it.
func objectScalarTextExprs(b sqlBinding) string {
	return b.col("object_iri") + ", CAST(" + b.col("object_float") + " AS VARCHAR), CAST(" + b.col("object_integer") + " AS VARCHAR), CAST(" + b.col("object_byte") + " AS VARCHAR), " + timeTextExpr(b.col("object_time")) + ", " + b.col("object_string")
}

// timeTextExpr renders an object_time column back as the xsd:dateTime lexical
// form it was built from: DuckDB casts a timestamp to
// "YYYY-MM-DD HH:MM:SS[.ffffff]", and build stores every value normalized to
// UTC, so swapping the space for a 'T' and appending 'Z' restores the ISO form.
func timeTextExpr(column string) string {
	return "replace(CAST(" + column + " AS VARCHAR), ' ', 'T') || 'Z'"
}

// ToSQL converts a read-only SPARQL SELECT, CONSTRUCT, or ASK over supported
// graph patterns into SQL that runs against the DuckDB triples view. A SELECT
// projects its variables; a CONSTRUCT projects the triples its template
// builds, in the columns ConstructColumns names; an ASK projects one boolean
// column named ask.
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
// from: its triple patterns, its FILTER expressions, the groups a MINUS
// subtracts from it, the groups an OPTIONAL joins to it, its UNIONs, BINDs,
// VALUES, and subqueries. The parser nests every construct over the pattern
// before it, so the group is flattened and each construct carries what it
// needs to know about its place in the group.
type groupParts struct {
	triples    []patternTriple
	filters    []rdflibsparql.Expr
	minuses    []minusGroup
	optionals  []optionalGroup
	unions     [][]rdflibsparql.Pattern
	binds      []bindGroup
	values     []*rdflibsparql.ValuesPattern
	subqueries []*rdflibsparql.ParsedQuery
}

// merge appends another group's parts to this one's, which is what a join of
// two patterns in the same group is.
func (p groupParts) merge(other groupParts) groupParts {
	return groupParts{
		triples:    append(p.triples, other.triples...),
		filters:    append(p.filters, other.filters...),
		minuses:    append(p.minuses, other.minuses...),
		optionals:  append(p.optionals, other.optionals...),
		unions:     append(p.unions, other.unions...),
		binds:      append(p.binds, other.binds...),
		values:     append(p.values, other.values...),
		subqueries: append(p.subqueries, other.subqueries...),
	}
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

// optionalGroup is an OPTIONAL group together with the variables of the
// pattern before it, which are the ones its LEFT JOIN is conditioned on. A
// variable it shares with a pattern after it in the group joins through the
// WHERE clause instead, as an unbound value or an equal one, since SPARQL
// joins that pattern to the solutions the OPTIONAL has already extended.
type optionalGroup struct {
	pattern  rdflibsparql.Pattern
	leftVars map[string]bool
}

// bindGroup is a BIND: the variable and the expression it is bound to.
type bindGroup struct {
	name string
	expr rdflibsparql.Expr
}

// translator carries what every part of one query's translation needs: the
// query's prefixes, the tables its patterns scan, counts of the subqueries,
// OPTIONAL groups, and derived tables written so far so that each one aliases
// its scans apart from the others and from the enclosing query, and a count
// of the hidden variables sequence paths have been chained through.
type translator struct {
	prefixes   map[string]string
	sources    tableSources
	subqueries int
	optionals  int
	derived    int
	hidden     int
}

// groupSQL is what a group graph pattern translates to: one aliased scan per
// triple pattern plus one derived table per UNION, VALUES, and subquery in
// from, the OPTIONAL groups LEFT JOINed to them in joins, their constants,
// joins, and filters in where, the binding of each variable, and the
// variables in the order they were first seen, which is what SELECT *
// projects. correlated reports whether any variable was already bound by an
// enclosing query, which a MINUS needs to know.
type groupSQL struct {
	from       []string
	joins      []leftJoin
	where      []string
	bindings   map[string]sqlBinding
	discovered []string
	correlated bool
}

// leftJoin is an OPTIONAL group as a LEFT JOIN: the group's own scans and
// joins, and the conditions it is joined on, which are the group's own
// constants, joins, and filters together with the equalities tying it to the
// pattern before it.
type leftJoin struct {
	inner groupSQL
	on    []string
}

// bind records a variable the group binds, joining it to the binding the
// group already has for it through join when there is one.
func (g *groupSQL) bind(name string, binding sqlBinding, join func(existing sqlBinding) string) {
	if existing, ok := g.bindings[name]; ok {
		g.where = append(g.where, join(existing))
		return
	}
	g.bindings[name] = binding
	if !isHiddenVariable(name) {
		g.discovered = append(g.discovered, name)
	}
}

// fromSQL renders the group's FROM clause, each scan and derived table on a
// line of its own under indent and each OPTIONAL group as a LEFT JOIN.
func (g groupSQL) fromSQL(indent string) string {
	from := strings.Join(g.from, "\n"+indent+"CROSS JOIN ")
	for _, join := range g.joins {
		inner := join.inner.fromSQL(indent + "  ")
		if len(join.inner.from)+len(join.inner.joins) > 1 {
			inner = "(" + inner + ")"
		}
		on := "TRUE"
		if len(join.on) > 0 {
			on = strings.Join(join.on, "\n"+indent+"  AND ")
		}
		from += "\n" + indent + "LEFT JOIN " + inner + " ON " + on
	}
	return from
}

// whereSQL renders the group's WHERE clause under indent, or nothing when
// there is none.
func (g groupSQL) whereSQL(indent string) string {
	if len(g.where) == 0 {
		return ""
	}
	return "\n" + indent + "WHERE " + strings.Join(g.where, "\n"+indent+"  AND ")
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
	t := &translator{prefixes: parsed.Prefixes, sources: sources}
	sql, _, err := t.querySQL(parsed)
	return sql, err
}

// querySQL translates a whole query, the one ToSQL was given or a subquery
// of it, and names the columns a SELECT projects.
func (t *translator) querySQL(parsed *rdflibsparql.ParsedQuery) (sql string, columns []string, err error) {
	if parsed.Type != "SELECT" && parsed.Type != "CONSTRUCT" && parsed.Type != "ASK" {
		return "", nil, fmt.Errorf("only read-only SPARQL SELECT, CONSTRUCT, and ASK queries are supported")
	}
	parts, err := t.graphPatternParts(parsed.Where)
	if err != nil {
		return "", nil, err
	}
	group, err := t.groupSQL(parts, "t", nil, nil)
	if err != nil {
		return "", nil, err
	}
	if len(group.from) == 0 {
		return "", nil, fmt.Errorf("SPARQL query must include at least one triple pattern")
	}
	bindings := group.bindings

	switch parsed.Type {
	case "CONSTRUCT":
		sql, err := constructSQL(parsed, parts, group, limitsSQL(parsed, "  "))
		return sql, ConstructColumns, err
	case "ASK":
		return "SELECT EXISTS (SELECT 1\n  FROM " + group.fromSQL("  ") + group.whereSQL("  ") + ") AS " + quoteIdent("ask"), []string{"ask"}, nil
	}

	// a grouped variable is projected by the expression it is grouped by
	grouped := make(map[string]bool)
	groupBy := make([]string, 0, len(parsed.GroupBy))
	for i, expr := range parsed.GroupBy {
		if variable, ok := expr.(*rdflibsparql.VarExpr); ok {
			binding, err := boundVariable(variable.Name, bindings)
			if err != nil {
				return "", nil, err
			}
			grouped[variable.Name] = true
			groupBy = append(groupBy, binding.expr())
			continue
		}
		e, err := t.exprSQL(expr, bindings)
		if err != nil {
			return "", nil, err
		}
		groupBy = append(groupBy, e.sql)
		if alias := parsed.GroupByAliases[i]; alias != "" {
			bindings[alias] = sqlBinding{column: valueColumn, sql: e.sql, kind: e.kind}
			grouped[alias] = true
		}
	}

	projected := parsed.Variables
	if len(projected) == 0 {
		projected = group.discovered
	}
	if len(projected) == 0 {
		return "", nil, fmt.Errorf("SPARQL SELECT must project at least one variable")
	}
	// A projection expression, (expr AS ?name), is listed among the variables
	// under the name it binds; the expression itself is what is projected,
	// and the name is bound to it so ORDER BY and HAVING can refer to it.
	projectExprs := make(map[string]rdflibsparql.Expr, len(parsed.ProjectExprs))
	for _, projectExpr := range parsed.ProjectExprs {
		projectExprs[projectExpr.Var] = projectExpr.Expr
	}
	selects := make([]string, 0, len(projected))
	for _, name := range projected {
		if expr, ok := projectExprs[name]; ok {
			e, err := t.exprSQL(expr, bindings)
			if err != nil {
				return "", nil, err
			}
			sql := e.sql
			if e.kind == kindGeometry {
				sql = "ST_AsText(" + sql + ")"
			}
			selects = append(selects, sql+" AS "+quoteIdent(name))
			bindings[name] = sqlBinding{column: valueColumn, sql: e.sql, kind: e.kind}
			continue
		}
		binding, ok := bindings[name]
		if !ok {
			return "", nil, fmt.Errorf("projected variable ?%s is not bound by a supported triple pattern", name)
		}
		if grouped[name] {
			selects = append(selects, binding.expr()+" AS "+quoteIdent(name))
			continue
		}
		selects = append(selects, binding.projection()+" AS "+quoteIdent(name))
	}

	sql = "SELECT " + strings.Join(selects, ", ") + "\nFROM " + group.fromSQL("") + group.whereSQL("")
	if len(groupBy) > 0 {
		sql += "\nGROUP BY " + strings.Join(groupBy, ", ")
	}
	if parsed.Having != nil {
		having, err := t.booleanSQL(parsed.Having, bindings)
		if err != nil {
			return "", nil, err
		}
		sql += "\nHAVING " + having
	}
	if parsed.Distinct {
		sql = strings.Replace(sql, "SELECT ", "SELECT DISTINCT ", 1)
	}
	orderBy, err := t.orderBySQL(parsed, bindings)
	if err != nil {
		return "", nil, err
	}
	return sql + orderBy + limitsSQL(parsed, ""), projected, nil
}

// orderBySQL renders the ORDER BY clause. An object variable is ordered by
// its numeric columns, then its timestamps, then the text it renders to, so
// that numbers order as numbers and dates as dates, each kind before the
// next; anything else is ordered by the expression itself.
func (t *translator) orderBySQL(parsed *rdflibsparql.ParsedQuery, bindings map[string]sqlBinding) (string, error) {
	if len(parsed.OrderBy) == 0 {
		return "", nil
	}
	keys := make([]string, 0, len(parsed.OrderBy))
	for _, order := range parsed.OrderBy {
		direction := ""
		if order.Desc {
			direction = " DESC"
		}
		if variable, ok := order.Expr.(*rdflibsparql.VarExpr); ok {
			binding, err := boundVariable(variable.Name, bindings)
			if err != nil {
				return "", err
			}
			if binding.typedObject() {
				keys = append(keys, binding.numeric()+direction, binding.col("object_time")+direction, binding.expr()+direction)
				continue
			}
		}
		e, err := t.exprSQL(order.Expr, bindings)
		if err != nil {
			return "", err
		}
		keys = append(keys, e.sql+direction)
	}
	return "\nORDER BY " + strings.Join(keys, ", "), nil
}

// limitsSQL renders the LIMIT and OFFSET of a query under indent.
func limitsSQL(parsed *rdflibsparql.ParsedQuery, indent string) string {
	sql := ""
	if parsed.Limit >= 0 {
		sql += "\n" + indent + "LIMIT " + strconv.Itoa(parsed.Limit)
	}
	if parsed.Offset > 0 {
		sql += "\n" + indent + "OFFSET " + strconv.Itoa(parsed.Offset)
	}
	return sql
}

// groupSQL translates a flattened group under aliasPrefix. outer holds the
// bindings of an enclosing query the group's patterns correlate to, for a
// MINUS or EXISTS group; scope holds further bindings the group's filters
// and BINDs may read without correlating to, the enclosing solution an
// EXISTS is evaluated under or the pattern an OPTIONAL extends. The parts
// are translated in a fixed order rather than the order the query wrote
// them: scans, then VALUES, subqueries, and UNIONs as derived tables, then
// OPTIONAL groups, then BINDs, which may read a variable an OPTIONAL bound,
// and last the filters, which see everything.
func (t *translator) groupSQL(parts groupParts, aliasPrefix string, outer map[string]sqlBinding, scope map[string]sqlBinding) (groupSQL, error) {
	group, err := t.patternSQL(parts.triples, aliasPrefix, outer)
	if err != nil {
		return groupSQL{}, err
	}
	visible := func() map[string]sqlBinding {
		all := make(map[string]sqlBinding, len(scope)+len(group.bindings))
		for name, binding := range scope {
			all[name] = binding
		}
		for name, binding := range group.bindings {
			all[name] = binding
		}
		return all
	}

	for _, values := range parts.values {
		if err := t.valuesSQL(&group, values); err != nil {
			return groupSQL{}, err
		}
	}
	for _, subquery := range parts.subqueries {
		sql, columns, err := t.querySQL(subquery)
		if err != nil {
			return groupSQL{}, err
		}
		alias := fmt.Sprintf("q%d", t.derived)
		t.derived++
		group.from = append(group.from, "(\n"+sql+"\n) AS "+alias)
		for _, name := range columns {
			binding := sqlBinding{alias: alias, column: valueColumn, name: name}
			group.bind(name, binding, func(existing sqlBinding) string { return joinClause(existing, binding) })
		}
	}
	for _, arms := range parts.unions {
		if err := t.unionSQL(&group, arms); err != nil {
			return groupSQL{}, err
		}
	}
	for _, optional := range parts.optionals {
		if err := t.optionalSQL(&group, optional, scope); err != nil {
			return groupSQL{}, err
		}
	}
	for _, bind := range parts.binds {
		e, err := t.exprSQL(bind.expr, visible())
		if err != nil {
			return groupSQL{}, err
		}
		binding := sqlBinding{column: valueColumn, sql: e.sql, kind: e.kind}
		group.bind(bind.name, binding, func(existing sqlBinding) string {
			column, value := existing.expr(), e.sql
			if existing.typedObject() {
				column, value = objectComparison(existing, e)
			}
			return nullableClause(existing, column+" = "+value)
		})
	}

	clauses, err := t.clauseSQL(parts, visible())
	if err != nil {
		return groupSQL{}, err
	}
	group.where = append(group.where, clauses...)
	return group, nil
}

// joinClause ties two bindings of one variable together. A binding a
// solution can leave unbound is compatible with anything, as SPARQL
// specifies, so the clause holds when it is NULL as well as when the values
// are equal.
func joinClause(a sqlBinding, b sqlBinding) string {
	return nullableClause(a, nullableClause(b, a.expr()+" = "+b.expr()))
}

func nullableClause(binding sqlBinding, clause string) string {
	if !binding.nullable {
		return clause
	}
	return "(" + binding.expr() + " IS NULL OR " + clause + ")"
}

// valuesSQL adds a VALUES block to the group as a derived table of its rows,
// its terms rendered the way a bound variable renders, so that it joins the
// group's bindings by text. An UNDEF is NULL, which makes the variable
// nullable, and a column of IRIs alone joins an object through object_iri.
func (t *translator) valuesSQL(group *groupSQL, values *rdflibsparql.ValuesPattern) error {
	alias := fmt.Sprintf("v%d", t.derived)
	t.derived++
	rows := make([]string, 0, len(values.Values))
	undefined := make([]bool, len(values.Vars))
	iris := make([]bool, len(values.Vars))
	for i := range iris {
		iris[i] = true
	}
	for _, row := range values.Values {
		if len(row) != len(values.Vars) {
			return fmt.Errorf("a VALUES row has %d values for %d variables", len(row), len(values.Vars))
		}
		cells := make([]string, 0, len(row))
		for i, term := range row {
			if term == nil {
				undefined[i] = true
				cells = append(cells, "NULL")
				continue
			}
			if _, ok := term.(rdflibgo.URIRef); !ok {
				iris[i] = false
			}
			cells = append(cells, sqlString(term.String()))
		}
		rows = append(rows, "("+strings.Join(cells, ", ")+")")
	}
	columns := make([]string, 0, len(values.Vars))
	for _, name := range values.Vars {
		columns = append(columns, quoteIdent(name))
	}
	group.from = append(group.from, "(VALUES "+strings.Join(rows, ", ")+") AS "+alias+"("+strings.Join(columns, ", ")+")")
	for i, name := range values.Vars {
		binding := sqlBinding{alias: alias, column: valueColumn, name: name, nullable: undefined[i]}
		group.bind(name, binding, func(existing sqlBinding) string {
			if existing.typedObject() && iris[i] {
				return nullableClause(binding, nullableClause(existing, existing.col("object_iri")+" = "+binding.expr()))
			}
			return joinClause(existing, binding)
		})
	}
	return nil
}

// unionSQL adds a UNION to the group as a derived table of its arms combined
// with UNION ALL. Each variable any arm binds is one column of every arm,
// NULL in an arm that does not bind it; a variable every arm binds in object
// position keeps its typed columns, so LANG, DATATYPE, and a numeric
// comparison still work on it, and one every arm binds as a subject or
// predicate stays one. Anything else is projected as the text it renders to.
func (t *translator) unionSQL(group *groupSQL, arms []rdflibsparql.Pattern) error {
	alias := fmt.Sprintf("u%d", t.derived)
	t.derived++
	armGroups := make([]groupSQL, 0, len(arms))
	var names []string
	positions := make(map[string]map[string]bool)
	for i, arm := range arms {
		parts, err := t.graphPatternParts(arm)
		if err != nil {
			return err
		}
		armGroup, err := t.groupSQL(parts, fmt.Sprintf("%s_%d_", alias, i), nil, nil)
		if err != nil {
			return err
		}
		if len(armGroup.from) == 0 {
			return fmt.Errorf("a UNION arm must include at least one triple pattern")
		}
		armGroups = append(armGroups, armGroup)
		for _, name := range armGroup.discovered {
			if positions[name] == nil {
				positions[name] = make(map[string]bool)
				names = append(names, name)
			}
			column := armGroup.bindings[name].column
			if armGroup.bindings[name].sql != "" {
				column = valueColumn
			}
			positions[name][column] = true
		}
	}

	// the column kind every arm agrees on, or valueColumn
	bindings := make(map[string]sqlBinding, len(names))
	for _, name := range names {
		column := valueColumn
		nullable := false
		for _, armGroup := range armGroups {
			if _, ok := armGroup.bindings[name]; !ok {
				nullable = true
			}
		}
		if !nullable && len(positions[name]) == 1 {
			for position := range positions[name] {
				column = position
			}
		}
		bindings[name] = sqlBinding{alias: alias, column: column, name: name, nullable: nullable}
	}

	selects := make([]string, 0, len(armGroups))
	for _, armGroup := range armGroups {
		columns := make([]string, 0, len(names))
		for _, name := range names {
			binding, bound := armGroup.bindings[name]
			switch {
			case bindings[name].typedObject():
				for _, column := range objectColumns {
					columns = append(columns, binding.col(column)+" AS "+quoteIdent(name+"."+column))
				}
			case bound:
				columns = append(columns, binding.expr()+" AS "+quoteIdent(name))
			default:
				columns = append(columns, "CAST(NULL AS VARCHAR) AS "+quoteIdent(name))
			}
		}
		selects = append(selects, "SELECT "+strings.Join(columns, ", ")+"\n  FROM "+armGroup.fromSQL("  ")+armGroup.whereSQL("  "))
	}
	group.from = append(group.from, "(\n"+strings.Join(selects, "\nUNION ALL\n")+"\n) AS "+alias)
	for _, name := range names {
		binding := bindings[name]
		group.bind(name, binding, func(existing sqlBinding) string { return joinClause(existing, binding) })
	}
	return nil
}

// objectColumns are the typed columns an object is spread over, which a
// derived table projects to keep a variable bound in object position typed.
var objectColumns = []string{"object_iri", "object_float", "object_integer", "object_byte", "object_time", "object_string", "object_type", "object_language", "object_geometry"}

// optionalSQL adds an OPTIONAL group to the group as a LEFT JOIN. The group
// is translated on its own, and everything it would put in a WHERE clause
// goes in the ON clause instead, together with an equality for each variable
// it shares with the pattern before it, so that the join either extends a
// solution with the whole group or leaves every variable of it NULL. A
// variable it shares with a pattern after it is joined through the WHERE
// clause as NULL or equal, and every variable it binds anew is nullable.
func (t *translator) optionalSQL(group *groupSQL, optional optionalGroup, scope map[string]sqlBinding) error {
	parts, err := t.graphPatternParts(optional.pattern)
	if err != nil {
		return err
	}
	left := make(map[string]sqlBinding, len(scope)+len(optional.leftVars))
	for name, binding := range scope {
		left[name] = binding
	}
	for name := range optional.leftVars {
		if binding, ok := group.bindings[name]; ok {
			left[name] = binding
		}
	}
	prefix := fmt.Sprintf("o%d_", t.optionals)
	t.optionals++
	inner, err := t.groupSQL(parts, prefix, nil, left)
	if err != nil {
		return err
	}
	if len(inner.from) == 0 {
		return fmt.Errorf("an OPTIONAL group must include at least one triple pattern")
	}
	join := leftJoin{inner: inner, on: inner.where}
	join.inner.where = nil
	for _, name := range inner.discovered {
		binding := inner.bindings[name]
		if existing, ok := left[name]; ok {
			join.on = append(join.on, joinClause(existing, binding))
			continue
		}
		binding.nullable = true
		group.bind(name, binding, func(existing sqlBinding) string { return joinClause(existing, binding) })
	}
	group.joins = append(group.joins, join)
	return nil
}

// patternSQL translates triple patterns into aliased scans, numbered from zero
// under aliasPrefix. A variable seen before in the same list becomes a join; one
// bound by an enclosing query, in outer, becomes a correlation instead, so that
// a MINUS or EXISTS group is evaluated per solution of the query it belongs to.
// A path step scans the same source with its subject and object columns
// swapped for an inverse, and a repeated step scans the closure of its edges
// instead, a derived table whose start and finish the pattern's ends bind.
func (t *translator) patternSQL(triples []patternTriple, aliasPrefix string, outer map[string]sqlBinding) (groupSQL, error) {
	result := groupSQL{bindings: make(map[string]sqlBinding)}
	for i, triple := range triples {
		alias := fmt.Sprintf("%s%d", aliasPrefix, i)
		source, err := t.sources.source(triple.service)
		if err != nil {
			return groupSQL{}, err
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
					return groupSQL{}, err
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
				return groupSQL{}, err
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
// An enclosing binding a solution can leave unbound is compatible with
// anything, so its correlation holds when it is NULL too.
func correlationSQL(alias string, correlations []correlation) []string {
	if len(correlations) == 3 {
		sameScan := true
		for _, c := range correlations {
			if c.inner.column != c.outer.column || !c.outer.scan() || c.outer.alias != correlations[0].outer.alias {
				sameScan = false
			}
		}
		if sameScan {
			return []string{alias + ".triple_hash = " + correlations[0].outer.alias + ".triple_hash"}
		}
	}
	var clauses []string
	for _, c := range correlations {
		if !c.inner.typedObject() || !c.outer.typedObject() {
			clauses = append(clauses, nullableClause(c.outer, c.inner.expr()+" = "+c.outer.expr()))
			continue
		}
		clauses = append(clauses,
			c.inner.projection()+" IS NOT DISTINCT FROM "+c.outer.projection(),
			c.inner.col("object_language")+" IS NOT DISTINCT FROM "+c.outer.col("object_language"),
			c.inner.col("object_type")+" IS NOT DISTINCT FROM "+c.outer.col("object_type"))
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
		clause, err := t.booleanSQL(filter, bindings)
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
	prefix := fmt.Sprintf("x%d_", t.subqueries)
	t.subqueries++
	var scope map[string]sqlBinding
	if seesOuter {
		scope = outer
	}
	group, err := t.groupSQL(parts, prefix, outer, scope)
	if err != nil {
		return "", false, err
	}
	if len(group.from) == 0 {
		return "", false, fmt.Errorf("a MINUS or EXISTS group must include at least one triple pattern")
	}
	return "EXISTS (SELECT 1\n  FROM " + group.fromSQL("  ") + group.whereSQL("  ") + ")", group.correlated, nil
}

// graphPatternParts flattens a group graph pattern into groupParts. A triple
// pattern written with a property path is replaced by the patterns pathSteps
// reduces it to. The parser nests each OPTIONAL, MINUS, BIND, and UNION over
// the pattern written before it in the group, and a join over the pattern
// after; flattening them into one groupParts keeps every scan of the group in
// one FROM clause, with each construct remembering the variables of the
// pattern it applied to where that matters.
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
			return groupParts{}, fmt.Errorf("a GRAPH clause is not supported; the table holds a single graph")
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
		// A group written inside the SERVICE is read from the same endpoint.
		// Its patterns are translated later, so the endpoint is carried by
		// wrapping the group in the GRAPH it was found under.
		inService := func(pattern rdflibsparql.Pattern) rdflibsparql.Pattern {
			return &rdflibsparql.GraphPattern{Name: p.Name, Pattern: pattern}
		}
		for i := range parts.minuses {
			parts.minuses[i].pattern = inService(parts.minuses[i].pattern)
		}
		for i := range parts.optionals {
			parts.optionals[i].pattern = inService(parts.optionals[i].pattern)
		}
		for i := range parts.unions {
			for j := range parts.unions[i] {
				parts.unions[i][j] = inService(parts.unions[i][j])
			}
		}
		for i, subquery := range parts.subqueries {
			scoped := *subquery
			scoped.Where = inService(subquery.Where)
			parts.subqueries[i] = &scoped
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
		return left.merge(right), nil
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
		left.minuses = append(left.minuses, minusGroup{pattern: p.Right, leftVars: patternVariables(p.Left)})
		return left, nil
	case *rdflibsparql.OptionalPattern:
		left, err := t.graphPatternParts(p.Main)
		if err != nil {
			return groupParts{}, err
		}
		left.optionals = append(left.optionals, optionalGroup{pattern: p.Optional, leftVars: patternVariables(p.Main)})
		return left, nil
	case *rdflibsparql.UnionPattern:
		return groupParts{unions: [][]rdflibsparql.Pattern{unionArms(p)}}, nil
	case *rdflibsparql.BindPattern:
		parts, err := t.graphPatternParts(p.Pattern)
		if err != nil {
			return groupParts{}, err
		}
		parts.binds = append(parts.binds, bindGroup{name: p.Var, expr: p.Expr})
		return parts, nil
	case *rdflibsparql.ValuesPattern:
		return groupParts{values: []*rdflibsparql.ValuesPattern{p}}, nil
	case *rdflibsparql.SubqueryPattern:
		return groupParts{subqueries: []*rdflibsparql.ParsedQuery{p.Query}}, nil
	default:
		return groupParts{}, fmt.Errorf("SPARQL pattern %T is not supported yet", pattern)
	}
}

// unionArms flattens a chain of UNIONs, which the parser nests to the left,
// into its arms in the order they were written.
func unionArms(pattern rdflibsparql.Pattern) []rdflibsparql.Pattern {
	union, ok := pattern.(*rdflibsparql.UnionPattern)
	if !ok {
		return []rdflibsparql.Pattern{pattern}
	}
	return append(unionArms(union.Left), unionArms(union.Right)...)
}

// patternVariables is the set of variables a pattern can bind: the ends of
// its triple patterns, the variables of its BINDs and VALUES, and what a
// subquery projects. The right side of a MINUS binds nothing outside it.
func patternVariables(pattern rdflibsparql.Pattern) map[string]bool {
	variables := make(map[string]bool)
	var collect func(pattern rdflibsparql.Pattern)
	collect = func(pattern rdflibsparql.Pattern) {
		switch p := pattern.(type) {
		case *rdflibsparql.BGP:
			for _, triple := range p.Triples {
				for _, term := range []string{triple.Subject, triple.Predicate, triple.Object} {
					if name := variableName(term); name != "" {
						variables[name] = true
					}
				}
			}
		case *rdflibsparql.GraphPattern:
			collect(p.Pattern)
		case *rdflibsparql.JoinPattern:
			collect(p.Left)
			collect(p.Right)
		case *rdflibsparql.FilterPattern:
			collect(p.Pattern)
		case *rdflibsparql.MinusPattern:
			collect(p.Left)
		case *rdflibsparql.OptionalPattern:
			collect(p.Main)
			collect(p.Optional)
		case *rdflibsparql.UnionPattern:
			collect(p.Left)
			collect(p.Right)
		case *rdflibsparql.BindPattern:
			collect(p.Pattern)
			variables[p.Var] = true
		case *rdflibsparql.ValuesPattern:
			for _, name := range p.Vars {
				variables[name] = true
			}
		case *rdflibsparql.SubqueryPattern:
			for _, name := range p.Query.Variables {
				variables[name] = true
			}
		}
	}
	collect(pattern)
	return variables
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
