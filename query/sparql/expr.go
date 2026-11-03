package sparql

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/cgs-earth/sal/pkg"
	rdflibgo "github.com/tggo/goRDFlib"
	rdflibsparql "github.com/tggo/goRDFlib/sparql"
)

// exprKind is what an expression evaluates to. It decides which typed column
// an object variable is compared against, so that FILTER(?age > 21) reads the
// numeric columns while FILTER(?name = "bob") reads object_string, and how a
// projected value is rendered.
type exprKind int

const (
	kindTerm     exprKind = iota // any RDF term rendered as text
	kindIRI                      // an IRI
	kindString                   // a string
	kindNumeric                  // a number
	kindBoolean                  // a truth value
	kindDateTime                 // a timestamp
	kindGeometry                 // a geometry
)

// sqlExpr is a translated expression together with its kind.
type sqlExpr struct {
	sql  string
	kind exprKind
}

// exprSQL translates an expression wherever a query writes one: a FILTER, a
// BIND, a projection, ORDER BY, GROUP BY, or HAVING. A comparison involving
// an object variable is written against the typed column the other side's
// kind names (comparisonSQL); everything else translates to the DuckDB
// function or operator of the same meaning.
func (t *translator) exprSQL(expr rdflibsparql.Expr, bindings map[string]sqlBinding) (sqlExpr, error) {
	switch e := expr.(type) {
	case *rdflibsparql.VarExpr:
		binding, err := boundVariable(e.Name, bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		if binding.sql != "" {
			return sqlExpr{sql: binding.sql, kind: binding.kind}, nil
		}
		return sqlExpr{sql: binding.expr(), kind: kindTerm}, nil
	case *rdflibsparql.LiteralExpr:
		return literalSQL(e.Value), nil
	case *rdflibsparql.IRIExpr:
		return sqlExpr{sql: sqlString(e.Value), kind: kindIRI}, nil
	case *rdflibsparql.BinaryExpr:
		switch e.Op {
		case "&&", "||":
			left, err := t.booleanSQL(e.Left, bindings)
			if err != nil {
				return sqlExpr{}, err
			}
			right, err := t.booleanSQL(e.Right, bindings)
			if err != nil {
				return sqlExpr{}, err
			}
			op := "AND"
			if e.Op == "||" {
				op = "OR"
			}
			return sqlExpr{sql: "(" + left + " " + op + " " + right + ")", kind: kindBoolean}, nil
		case "=", "!=", "<", ">", "<=", ">=":
			return t.comparisonSQL(e.Op, e.Left, e.Right, bindings)
		case "+", "-", "*", "/":
			left, err := t.numericSQL(e.Left, bindings)
			if err != nil {
				return sqlExpr{}, err
			}
			right, err := t.numericSQL(e.Right, bindings)
			if err != nil {
				return sqlExpr{}, err
			}
			return sqlExpr{sql: "(" + left + " " + e.Op + " " + right + ")", kind: kindNumeric}, nil
		default:
			return sqlExpr{}, fmt.Errorf("SPARQL operator %q is not supported yet", e.Op)
		}
	case *rdflibsparql.UnaryExpr:
		switch e.Op {
		case "!":
			inner, err := t.booleanSQL(e.Arg, bindings)
			if err != nil {
				return sqlExpr{}, err
			}
			return sqlExpr{sql: "NOT (" + inner + ")", kind: kindBoolean}, nil
		case "-":
			inner, err := t.numericSQL(e.Arg, bindings)
			if err != nil {
				return sqlExpr{}, err
			}
			return sqlExpr{sql: "-(" + inner + ")", kind: kindNumeric}, nil
		default:
			return sqlExpr{}, fmt.Errorf("SPARQL operator %q is not supported yet", e.Op)
		}
	case *rdflibsparql.ExistsExpr:
		sql, _, err := t.subquerySQL(e.Pattern, bindings, true)
		if err != nil {
			return sqlExpr{}, err
		}
		if e.Not {
			sql = "NOT " + sql
		}
		return sqlExpr{sql: sql, kind: kindBoolean}, nil
	case *rdflibsparql.FuncExpr:
		return t.functionSQL(e, bindings)
	default:
		return sqlExpr{}, fmt.Errorf("unsupported SPARQL expression %T", expr)
	}
}

func boundVariable(name string, bindings map[string]sqlBinding) (sqlBinding, error) {
	binding, ok := bindings[name]
	if !ok {
		return sqlBinding{}, fmt.Errorf("FILTER variable ?%s is not bound by a supported triple pattern", name)
	}
	return binding, nil
}

// literalSQL renders a literal as the SQL value it compares as: a timestamp,
// a number, a boolean, or otherwise its lexical form as a string.
func literalSQL(term rdflibgo.Term) sqlExpr {
	if sql, ok := timestampSQL(term); ok {
		return sqlExpr{sql: sql, kind: kindDateTime}
	}
	if literalIsNumeric(term) {
		return sqlExpr{sql: term.String(), kind: kindNumeric}
	}
	if literal, ok := term.(rdflibgo.Literal); ok && literal.Datatype().Value() == rdflibgo.XSDBoolean.Value() {
		return sqlExpr{sql: strings.ToUpper(literal.Lexical()), kind: kindBoolean}
	}
	return sqlExpr{sql: sqlString(term.String()), kind: kindString}
}

// booleanSQL translates an expression that has to be a truth value, which is
// what a FILTER, a logical operator, and an IF condition take.
func (t *translator) booleanSQL(expr rdflibsparql.Expr, bindings map[string]sqlBinding) (string, error) {
	e, err := t.exprSQL(expr, bindings)
	if err != nil {
		return "", err
	}
	if e.kind != kindBoolean {
		return "", fmt.Errorf("a FILTER on %s must compare it against something", e.sql)
	}
	return e.sql, nil
}

// numericSQL translates an expression read as a number: an object variable
// through its numeric columns, text through TRY_CAST so that a value that is
// not a number is NULL rather than an error.
func (t *translator) numericSQL(expr rdflibsparql.Expr, bindings map[string]sqlBinding) (string, error) {
	if v, ok := expr.(*rdflibsparql.VarExpr); ok {
		binding, err := boundVariable(v.Name, bindings)
		if err != nil {
			return "", err
		}
		if binding.sql == "" || binding.kind != kindNumeric {
			return binding.numeric(), nil
		}
		return binding.sql, nil
	}
	e, err := t.exprSQL(expr, bindings)
	if err != nil {
		return "", err
	}
	switch e.kind {
	case kindNumeric:
		return e.sql, nil
	case kindTerm, kindString:
		return "TRY_CAST(" + e.sql + " AS DOUBLE)", nil
	default:
		return "", fmt.Errorf("%s is not a number", e.sql)
	}
}

// integerSQL is numericSQL for an argument SQL needs as an integer, such as
// the position of a SUBSTR. An integer literal is written as it is.
func (t *translator) integerSQL(expr rdflibsparql.Expr, bindings map[string]sqlBinding) (string, error) {
	if literal, ok := expr.(*rdflibsparql.LiteralExpr); ok {
		if _, err := strconv.ParseInt(literal.Value.String(), 10, 64); err == nil {
			return literal.Value.String(), nil
		}
	}
	sql, err := t.numericSQL(expr, bindings)
	if err != nil {
		return "", err
	}
	return "CAST(" + sql + " AS BIGINT)", nil
}

// stringSQL translates an expression read as a string. An object variable
// reads object_string, so that a string function on an IRI or a number
// yields NULL as SPARQL specifies; STR, which accepts any term, is the one
// function that reads the whole object instead.
func (t *translator) stringSQL(expr rdflibsparql.Expr, bindings map[string]sqlBinding) (string, error) {
	if v, ok := expr.(*rdflibsparql.VarExpr); ok {
		binding, err := boundVariable(v.Name, bindings)
		if err != nil {
			return "", err
		}
		if binding.typedObject() {
			return binding.col("object_string"), nil
		}
	}
	e, err := t.exprSQL(expr, bindings)
	if err != nil {
		return "", err
	}
	return textSQL(e), nil
}

// textSQL renders a translated expression as text, the way a bound variable
// renders.
func textSQL(e sqlExpr) string {
	switch e.kind {
	case kindNumeric, kindDateTime:
		return "CAST(" + e.sql + " AS VARCHAR)"
	case kindBoolean:
		return "CASE WHEN " + e.sql + " THEN 'true' ELSE 'false' END"
	case kindGeometry:
		return "ST_AsText(" + e.sql + ")"
	default:
		return e.sql
	}
}

// termSQL translates an expression read as any term rendered to text, which
// is what STR, GROUP_CONCAT, and a cast take: an object renders as it is
// projected, a geometry as its WKT.
func (t *translator) termSQL(expr rdflibsparql.Expr, bindings map[string]sqlBinding) (string, error) {
	if v, ok := expr.(*rdflibsparql.VarExpr); ok {
		binding, err := boundVariable(v.Name, bindings)
		if err != nil {
			return "", err
		}
		if binding.sql == "" {
			return binding.projection(), nil
		}
	}
	return t.stringSQL(expr, bindings)
}

// dateTimeSQL translates an expression read as a timestamp.
func (t *translator) dateTimeSQL(expr rdflibsparql.Expr, bindings map[string]sqlBinding) (string, error) {
	if v, ok := expr.(*rdflibsparql.VarExpr); ok {
		binding, err := boundVariable(v.Name, bindings)
		if err != nil {
			return "", err
		}
		if binding.typedObject() {
			return binding.col("object_time"), nil
		}
	}
	e, err := t.exprSQL(expr, bindings)
	if err != nil {
		return "", err
	}
	switch e.kind {
	case kindDateTime:
		return e.sql, nil
	case kindTerm, kindString:
		return "TRY_CAST(" + e.sql + " AS TIMESTAMP)", nil
	default:
		return "", fmt.Errorf("%s is not a dateTime", e.sql)
	}
}

// operand is one side of a comparison: a variable read from a table, kept as
// its binding so the column it is compared in can be chosen by the other
// side, or any other expression.
type operand struct {
	binding *sqlBinding
	expr    sqlExpr
}

func (t *translator) operandSQL(expr rdflibsparql.Expr, bindings map[string]sqlBinding) (operand, error) {
	if v, ok := expr.(*rdflibsparql.VarExpr); ok {
		binding, err := boundVariable(v.Name, bindings)
		if err != nil {
			return operand{}, err
		}
		if binding.sql == "" {
			return operand{binding: &binding, expr: sqlExpr{sql: binding.expr(), kind: kindTerm}}, nil
		}
	}
	e, err := t.exprSQL(expr, bindings)
	return operand{expr: e}, err
}

// comparisonSQL translates a comparison. An object variable is compared in
// the column the other side's kind names, so a number compares as a number
// rather than as the text it renders to; two variables are compared as the
// text each renders to, so that an IRI, a number, or a date compares as well
// as a string does.
func (t *translator) comparisonSQL(op string, leftExpr, rightExpr rdflibsparql.Expr, bindings map[string]sqlBinding) (sqlExpr, error) {
	left, err := t.operandSQL(leftExpr, bindings)
	if err != nil {
		return sqlExpr{}, err
	}
	right, err := t.operandSQL(rightExpr, bindings)
	if err != nil {
		return sqlExpr{}, err
	}
	l, r := left.expr.sql, right.expr.sql
	switch {
	case left.typedObject() && !right.typedObject():
		l, r = objectComparison(*left.binding, right.expr)
	case right.typedObject() && !left.typedObject():
		r, l = objectComparison(*right.binding, left.expr)
	case left.expr.kind == kindNumeric && right.expr.kind != kindNumeric:
		r = "TRY_CAST(" + r + " AS DOUBLE)"
	case right.expr.kind == kindNumeric && left.expr.kind != kindNumeric:
		l = "TRY_CAST(" + l + " AS DOUBLE)"
	}
	return sqlExpr{sql: l + " " + op + " " + r, kind: kindBoolean}, nil
}

func (o operand) typedObject() bool {
	return o.binding != nil && o.binding.typedObject()
}

// objectComparison is the column an object variable is compared in against
// the other side of a comparison, and that side rewritten to compare against
// it: a boolean is stored as its lexical form in object_string.
func objectComparison(binding sqlBinding, other sqlExpr) (column string, otherSQL string) {
	switch other.kind {
	case kindNumeric:
		return binding.numeric(), other.sql
	case kindDateTime:
		return binding.col("object_time"), other.sql
	case kindString:
		return binding.col("object_string"), other.sql
	case kindIRI:
		return binding.col("object_iri"), other.sql
	case kindBoolean:
		if other.sql == "TRUE" || other.sql == "FALSE" {
			return binding.col("object_string"), sqlString(strings.ToLower(other.sql))
		}
		return binding.col("object_string"), "CASE WHEN " + other.sql + " THEN 'true' ELSE 'false' END"
	case kindGeometry:
		return binding.col("object_geometry"), other.sql
	default:
		return binding.expr(), other.sql
	}
}

// regexFlags maps the SPARQL regex flags to DuckDB's RE2 options: i for
// case insensitivity, m for ^ and $ matching at line breaks, q for a literal
// pattern. s is DuckDB's default and x has no counterpart.
var regexFlags = map[rune]string{'i': "i", 'm': "m", 'q': "l", 's': ""}

// groupReference is the $n that a SPARQL REPLACE names a capture group by,
// which DuckDB writes as \n.
var groupReference = regexp.MustCompile(`\$(\d)`)

// functionSQL translates a function call.
func (t *translator) functionSQL(call *rdflibsparql.FuncExpr, bindings map[string]sqlBinding) (sqlExpr, error) {
	args := func(n int) error {
		if len(call.Args) != n {
			return fmt.Errorf("%s takes %d arguments, got %d", call.Name, n, len(call.Args))
		}
		return nil
	}
	switch call.Name {
	case "STR":
		if err := args(1); err != nil {
			return sqlExpr{}, err
		}
		sql, err := t.termSQL(call.Args[0], bindings)
		return sqlExpr{sql: sql, kind: kindString}, err
	case "LANG":
		binding, err := literalArgument(call, bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		return sqlExpr{sql: langExpr(binding), kind: kindString}, nil
	case "DATATYPE":
		binding, err := literalArgument(call, bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		return sqlExpr{sql: binding.col("object_type"), kind: kindIRI}, nil
	case "LANGMATCHES":
		sql, err := langMatchesSQL(call, bindings)
		return sqlExpr{sql: sql, kind: kindBoolean}, err
	case "BOUND":
		if err := args(1); err != nil {
			return sqlExpr{}, err
		}
		variable, ok := call.Args[0].(*rdflibsparql.VarExpr)
		if !ok {
			return sqlExpr{}, fmt.Errorf("BOUND takes a variable")
		}
		binding, err := boundVariable(variable.Name, bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		return sqlExpr{sql: binding.present() + " IS NOT NULL", kind: kindBoolean}, nil
	case "COALESCE":
		if len(call.Args) == 0 {
			return sqlExpr{}, fmt.Errorf("COALESCE takes at least one argument")
		}
		exprs := make([]sqlExpr, 0, len(call.Args))
		for _, arg := range call.Args {
			e, err := t.exprSQL(arg, bindings)
			if err != nil {
				return sqlExpr{}, err
			}
			exprs = append(exprs, e)
		}
		kind, sqls := sharedKind(exprs)
		return sqlExpr{sql: "COALESCE(" + strings.Join(sqls, ", ") + ")", kind: kind}, nil
	case "IF":
		if err := args(3); err != nil {
			return sqlExpr{}, err
		}
		condition, err := t.booleanSQL(call.Args[0], bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		then, err := t.exprSQL(call.Args[1], bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		otherwise, err := t.exprSQL(call.Args[2], bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		kind, branches := sharedKind([]sqlExpr{then, otherwise})
		return sqlExpr{sql: "CASE WHEN " + condition + " THEN " + branches[0] + " ELSE " + branches[1] + " END", kind: kind}, nil
	case "ISIRI", "ISURI", "ISBLANK", "ISLITERAL", "ISNUMERIC":
		if err := args(1); err != nil {
			return sqlExpr{}, err
		}
		return t.termTestSQL(call.Name, call.Args[0], bindings)
	case "SAMETERM":
		if err := args(2); err != nil {
			return sqlExpr{}, err
		}
		return t.comparisonSQL("=", call.Args[0], call.Args[1], bindings)
	case "STRLEN", "UCASE", "LCASE", "MD5", "SHA1", "SHA256":
		if err := args(1); err != nil {
			return sqlExpr{}, err
		}
		s, err := t.stringSQL(call.Args[0], bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		function := map[string]string{"STRLEN": "length", "UCASE": "upper", "LCASE": "lower", "MD5": "md5", "SHA1": "sha1", "SHA256": "sha256"}[call.Name]
		kind := kindString
		if call.Name == "STRLEN" {
			kind = kindNumeric
		}
		return sqlExpr{sql: function + "(" + s + ")", kind: kind}, nil
	case "STRSTARTS", "STRENDS", "CONTAINS", "STRBEFORE", "STRAFTER":
		if err := args(2); err != nil {
			return sqlExpr{}, err
		}
		s, err := t.stringSQL(call.Args[0], bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		part, err := t.stringSQL(call.Args[1], bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		switch call.Name {
		case "STRSTARTS":
			return sqlExpr{sql: "starts_with(" + s + ", " + part + ")", kind: kindBoolean}, nil
		case "STRENDS":
			return sqlExpr{sql: "ends_with(" + s + ", " + part + ")", kind: kindBoolean}, nil
		case "CONTAINS":
			return sqlExpr{sql: "contains(" + s + ", " + part + ")", kind: kindBoolean}, nil
		case "STRBEFORE":
			return sqlExpr{sql: "CASE WHEN contains(" + s + ", " + part + ") THEN substr(" + s + ", 1, instr(" + s + ", " + part + ") - 1) ELSE '' END", kind: kindString}, nil
		default:
			return sqlExpr{sql: "CASE WHEN contains(" + s + ", " + part + ") THEN substr(" + s + ", instr(" + s + ", " + part + ") + length(" + part + ")) ELSE '' END", kind: kindString}, nil
		}
	case "SUBSTR":
		if len(call.Args) != 2 && len(call.Args) != 3 {
			return sqlExpr{}, fmt.Errorf("SUBSTR takes a string, a start, and an optional length, got %d arguments", len(call.Args))
		}
		s, err := t.stringSQL(call.Args[0], bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		positions := make([]string, 0, 2)
		for _, arg := range call.Args[1:] {
			position, err := t.integerSQL(arg, bindings)
			if err != nil {
				return sqlExpr{}, err
			}
			positions = append(positions, position)
		}
		return sqlExpr{sql: "substr(" + s + ", " + strings.Join(positions, ", ") + ")", kind: kindString}, nil
	case "CONCAT":
		parts := make([]string, 0, len(call.Args))
		for _, arg := range call.Args {
			s, err := t.stringSQL(arg, bindings)
			if err != nil {
				return sqlExpr{}, err
			}
			parts = append(parts, s)
		}
		return sqlExpr{sql: "concat(" + strings.Join(parts, ", ") + ")", kind: kindString}, nil
	case "REGEX", "REPLACE":
		return t.regexSQL(call, bindings)
	case "ABS", "ROUND", "CEIL", "FLOOR":
		if err := args(1); err != nil {
			return sqlExpr{}, err
		}
		n, err := t.numericSQL(call.Args[0], bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		return sqlExpr{sql: strings.ToLower(call.Name) + "(" + n + ")", kind: kindNumeric}, nil
	case "RAND":
		return sqlExpr{sql: "random()", kind: kindNumeric}, nil
	case "NOW":
		return sqlExpr{sql: "now()", kind: kindDateTime}, nil
	case "YEAR", "MONTH", "DAY", "HOURS", "MINUTES", "SECONDS":
		if err := args(1); err != nil {
			return sqlExpr{}, err
		}
		timestamp, err := t.dateTimeSQL(call.Args[0], bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		function := map[string]string{"YEAR": "year", "MONTH": "month", "DAY": "day", "HOURS": "hour", "MINUTES": "minute", "SECONDS": "second"}[call.Name]
		return sqlExpr{sql: function + "(" + timestamp + ")", kind: kindNumeric}, nil
	case "IRI", "URI":
		if err := args(1); err != nil {
			return sqlExpr{}, err
		}
		s, err := t.termSQL(call.Args[0], bindings)
		return sqlExpr{sql: s, kind: kindIRI}, err
	case "COUNT", "SUM", "AVG", "MIN", "MAX", "GROUP_CONCAT", "SAMPLE":
		return t.aggregateSQL(call, bindings)
	}
	if local, ok := strings.CutPrefix(call.IRI, pkg.XSDNamespace); ok {
		return t.castSQL(local, call, bindings)
	}
	return geoFunctionSQL(call, bindings)
}

// sharedKind is the kind of a COALESCE or an IF and the SQL of its branches:
// the kind they agree on, or otherwise any term, with every branch rendered
// as text since SQL will not mix a number with a string.
func sharedKind(exprs []sqlExpr) (exprKind, []string) {
	sqls := make([]string, 0, len(exprs))
	agree := true
	for _, e := range exprs {
		sqls = append(sqls, e.sql)
		agree = agree && e.kind == exprs[0].kind
	}
	if agree {
		return exprs[0].kind, sqls
	}
	for i, e := range exprs {
		sqls[i] = textSQL(e)
	}
	return kindTerm, sqls
}

// termTestSQL translates isIRI, isBlank, isLiteral, and isNumeric. An object
// variable answers from its typed columns: an IRI fills object_iri, a literal
// carries an object_type, and a blank node has neither. A subject or
// predicate is an IRI unless it carries the "_:" prefix the table stores
// blank nodes under. A value a BIND, VALUES, UNION, or subquery bound is
// text, which is told apart by its shape.
func (t *translator) termTestSQL(name string, arg rdflibsparql.Expr, bindings map[string]sqlBinding) (sqlExpr, error) {
	variable, ok := arg.(*rdflibsparql.VarExpr)
	if !ok {
		e, err := t.exprSQL(arg, bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		truth := map[string]bool{
			"ISIRI": e.kind == kindIRI, "ISURI": e.kind == kindIRI, "ISBLANK": false,
			"ISLITERAL": e.kind != kindIRI && e.kind != kindTerm, "ISNUMERIC": e.kind == kindNumeric,
		}[name]
		return sqlExpr{sql: strings.ToUpper(strconv.FormatBool(truth)), kind: kindBoolean}, nil
	}
	binding, err := boundVariable(variable.Name, bindings)
	if err != nil {
		return sqlExpr{}, err
	}
	var sql string
	switch {
	case binding.typedObject():
		sql = map[string]string{
			"ISIRI":     binding.col("object_iri") + " IS NOT NULL",
			"ISURI":     binding.col("object_iri") + " IS NOT NULL",
			"ISBLANK":   "(" + binding.col("object_iri") + " IS NULL AND " + binding.col("object_type") + " IS NULL AND " + binding.col("object_string") + " IS NOT NULL)",
			"ISLITERAL": binding.col("object_type") + " IS NOT NULL",
			"ISNUMERIC": binding.numeric() + " IS NOT NULL",
		}[name]
	case binding.column == "subject" || binding.column == "predicate":
		sql = map[string]string{
			"ISIRI":     "NOT starts_with(" + binding.expr() + ", '_:')",
			"ISURI":     "NOT starts_with(" + binding.expr() + ", '_:')",
			"ISBLANK":   "starts_with(" + binding.expr() + ", '_:')",
			"ISLITERAL": "FALSE",
			"ISNUMERIC": "FALSE",
		}[name]
	default:
		value := binding.expr()
		iri := "(regexp_matches(" + value + ", '^[A-Za-z][A-Za-z0-9+.-]*:') AND NOT starts_with(" + value + ", '_:'))"
		sql = map[string]string{
			"ISIRI":     iri,
			"ISURI":     iri,
			"ISBLANK":   "starts_with(" + value + ", '_:')",
			"ISLITERAL": "(" + value + " IS NOT NULL AND NOT " + iri + " AND NOT starts_with(" + value + ", '_:'))",
			"ISNUMERIC": "TRY_CAST(" + value + " AS DOUBLE) IS NOT NULL",
		}[name]
	}
	return sqlExpr{sql: sql, kind: kindBoolean}, nil
}

// regexSQL translates REGEX and REPLACE to DuckDB's regexp_matches and
// regexp_replace, which take RE2 syntax, close enough to XPath's for the
// patterns a query writes.
func (t *translator) regexSQL(call *rdflibsparql.FuncExpr, bindings map[string]sqlBinding) (sqlExpr, error) {
	fixed := 2
	if call.Name == "REPLACE" {
		fixed = 3
	}
	if len(call.Args) != fixed && len(call.Args) != fixed+1 {
		return sqlExpr{}, fmt.Errorf("%s takes %d arguments and optional flags, got %d", call.Name, fixed, len(call.Args))
	}
	args := make([]string, 0, fixed)
	for _, arg := range call.Args[:fixed] {
		s, err := t.stringSQL(arg, bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		args = append(args, s)
	}
	options := ""
	if len(call.Args) > fixed {
		literal, ok := call.Args[fixed].(*rdflibsparql.LiteralExpr)
		if !ok {
			return sqlExpr{}, fmt.Errorf("the flags of %s must be a string literal", call.Name)
		}
		for _, flag := range literal.Value.String() {
			option, ok := regexFlags[flag]
			if !ok {
				return sqlExpr{}, fmt.Errorf("the %s flag %q is not supported; use i, m, q, or s", call.Name, string(flag))
			}
			options += option
		}
	}
	if call.Name == "REGEX" {
		if options != "" {
			args = append(args, sqlString(options))
		}
		return sqlExpr{sql: "regexp_matches(" + strings.Join(args, ", ") + ")", kind: kindBoolean}, nil
	}
	// a replacement names a group as $1, which RE2 writes \1
	args[2] = groupReference.ReplaceAllString(args[2], `\$1`)
	args = append(args, sqlString("g"+options))
	return sqlExpr{sql: "regexp_replace(" + strings.Join(args, ", ") + ")", kind: kindString}, nil
}

// aggregateSQL translates an aggregate call to DuckDB's. The minimum and
// maximum of an object variable are taken over its numeric columns first, so
// that 9 sorts before 10, and over the text it renders to otherwise.
func (t *translator) aggregateSQL(call *rdflibsparql.FuncExpr, bindings map[string]sqlBinding) (sqlExpr, error) {
	distinct := ""
	if call.Distinct {
		distinct = "DISTINCT "
	}
	if call.Name == "COUNT" && call.Star {
		return sqlExpr{sql: "count(*)", kind: kindNumeric}, nil
	}
	if len(call.Args) != 1 {
		return sqlExpr{}, fmt.Errorf("%s takes one argument, got %d", call.Name, len(call.Args))
	}
	arg := call.Args[0]
	switch call.Name {
	case "COUNT":
		// a plain count asks only whether the variable is bound
		if variable, ok := arg.(*rdflibsparql.VarExpr); ok && !call.Distinct {
			binding, err := boundVariable(variable.Name, bindings)
			if err != nil {
				return sqlExpr{}, err
			}
			return sqlExpr{sql: "count(" + binding.present() + ")", kind: kindNumeric}, nil
		}
		e, err := t.exprSQL(arg, bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		return sqlExpr{sql: "count(" + distinct + e.sql + ")", kind: kindNumeric}, nil
	case "SUM", "AVG":
		n, err := t.numericSQL(arg, bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		return sqlExpr{sql: strings.ToLower(call.Name) + "(" + distinct + n + ")", kind: kindNumeric}, nil
	case "MIN", "MAX":
		function := strings.ToLower(call.Name)
		if variable, ok := arg.(*rdflibsparql.VarExpr); ok {
			binding, err := boundVariable(variable.Name, bindings)
			if err != nil {
				return sqlExpr{}, err
			}
			if binding.typedObject() {
				return sqlExpr{sql: "COALESCE(CAST(" + function + "(" + binding.numeric() + ") AS VARCHAR), " + function + "(" + binding.expr() + "))", kind: kindTerm}, nil
			}
		}
		e, err := t.exprSQL(arg, bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		return sqlExpr{sql: function + "(" + e.sql + ")", kind: e.kind}, nil
	case "GROUP_CONCAT":
		s, err := t.termSQL(arg, bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		separator := call.Separator
		if separator == "" {
			separator = " "
		}
		return sqlExpr{sql: "string_agg(" + distinct + s + ", " + sqlString(separator) + ")", kind: kindString}, nil
	default:
		e, err := t.exprSQL(arg, bindings)
		if err != nil {
			return sqlExpr{}, err
		}
		return sqlExpr{sql: "any_value(" + e.sql + ")", kind: e.kind}, nil
	}
}

// castSQL translates an XSD constructor such as xsd:integer(?x) to a
// TRY_CAST, so a value that cannot be converted is NULL rather than an error.
func (t *translator) castSQL(local string, call *rdflibsparql.FuncExpr, bindings map[string]sqlBinding) (sqlExpr, error) {
	if len(call.Args) != 1 {
		return sqlExpr{}, fmt.Errorf("xsd:%s takes one argument, got %d", local, len(call.Args))
	}
	s, err := t.termSQL(call.Args[0], bindings)
	if err != nil {
		return sqlExpr{}, err
	}
	switch local {
	case "string":
		return sqlExpr{sql: s, kind: kindString}, nil
	case "integer", "int", "long", "short", "byte", "nonNegativeInteger", "positiveInteger", "nonPositiveInteger", "negativeInteger", "unsignedLong", "unsignedInt", "unsignedShort", "unsignedByte":
		return sqlExpr{sql: "TRY_CAST(" + s + " AS BIGINT)", kind: kindNumeric}, nil
	case "decimal", "double", "float":
		return sqlExpr{sql: "TRY_CAST(" + s + " AS DOUBLE)", kind: kindNumeric}, nil
	case "boolean":
		return sqlExpr{sql: "TRY_CAST(" + s + " AS BOOLEAN)", kind: kindBoolean}, nil
	case "dateTime", "date":
		return sqlExpr{sql: "TRY_CAST(" + s + " AS TIMESTAMP)", kind: kindDateTime}, nil
	default:
		return sqlExpr{}, fmt.Errorf("SPARQL function xsd:%s is not supported yet", local)
	}
}
