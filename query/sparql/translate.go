package sparql

import (
	"fmt"
	"strconv"
	"strings"

	rdflibgo "github.com/tggo/goRDFlib"
	rdflibsparql "github.com/tggo/goRDFlib/sparql"
)

type ObjectLayout int

const (
	SimpleObjects ObjectLayout = iota
	TypedObjects
)

type sqlBinding struct {
	alias  string
	column string
}

// ToSQL converts a read-only SPARQL SELECT over supported triple patterns
// into SQL that runs against the DuckDB triples view.
func ToSQL(input string, layout ObjectLayout) (string, error) {
	parsed, err := rdflibsparql.Parse(input)
	if err != nil {
		return "", fmt.Errorf("parse SPARQL query: %w", err)
	}
	if parsed.Type != "SELECT" {
		return "", fmt.Errorf("only read-only SPARQL SELECT queries are supported")
	}
	if len(parsed.ProjectExprs) > 0 || len(parsed.GroupBy) > 0 || parsed.Having != nil || len(parsed.OrderBy) > 0 || parsed.Offset > 0 {
		return "", fmt.Errorf("SPARQL projection expressions and solution modifiers are not supported yet")
	}

	triples, filters, err := basicGraphPatternParts(parsed.Where)
	if err != nil {
		return "", err
	}
	if len(triples) == 0 {
		return "", fmt.Errorf("SPARQL query must include at least one triple pattern")
	}

	var from []string
	var where []string
	bindings := make(map[string]sqlBinding)
	var discovered []string
	for i, triple := range triples {
		alias := fmt.Sprintf("t%d", i)
		from = append(from, "triples AS "+alias)
		for _, part := range []struct {
			term   string
			column string
		}{
			{term: triple.Subject, column: "subject"},
			{term: triple.Predicate, column: "predicate"},
			{term: triple.Object, column: "object"},
		} {
			if variableName(part.term) != "" {
				name := variableName(part.term)
				expr := bindingExpr(alias, part.column, layout)
				if previous, ok := bindings[name]; ok {
					where = append(where, previous.expr(layout)+" = "+expr)
				} else {
					bindings[name] = sqlBinding{alias: alias, column: part.column}
					discovered = append(discovered, name)
				}
				continue
			}
			clauses, err := constantClauses(alias, part.column, part.term, parsed.Prefixes, layout)
			if err != nil {
				return "", err
			}
			where = append(where, clauses...)
		}
	}
	for _, filter := range filters {
		clause, err := filterSQL(filter, bindings, layout)
		if err != nil {
			return "", err
		}
		where = append(where, clause)
	}

	projected := parsed.Variables
	if len(projected) == 0 {
		projected = discovered
	}
	if len(projected) == 0 {
		return "", fmt.Errorf("SPARQL SELECT must project at least one variable")
	}
	selects := make([]string, 0, len(projected))
	for _, name := range projected {
		binding, ok := bindings[name]
		if !ok {
			return "", fmt.Errorf("projected variable ?%s is not bound by a supported triple pattern", name)
		}
		selects = append(selects, binding.expr(layout)+" AS "+quoteIdent(name))
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

func (b sqlBinding) expr(layout ObjectLayout) string {
	return bindingExpr(b.alias, b.column, layout)
}

func bindingExpr(alias string, column string, layout ObjectLayout) string {
	if column != "object" || layout == SimpleObjects {
		return alias + "." + column
	}
	return "COALESCE(" + alias + ".object_iri, CAST(" + alias + ".object_float AS VARCHAR), " + alias + ".object_string)"
}

func basicGraphPatternParts(pattern rdflibsparql.Pattern) ([]rdflibsparql.Triple, []rdflibsparql.Expr, error) {
	switch p := pattern.(type) {
	case *rdflibsparql.BGP:
		for _, triple := range p.Triples {
			if triple.PredicatePath != nil {
				return nil, nil, fmt.Errorf("SPARQL property paths are not supported yet")
			}
		}
		return p.Triples, nil, nil
	case *rdflibsparql.JoinPattern:
		leftTriples, leftFilters, err := basicGraphPatternParts(p.Left)
		if err != nil {
			return nil, nil, err
		}
		rightTriples, rightFilters, err := basicGraphPatternParts(p.Right)
		if err != nil {
			return nil, nil, err
		}
		return append(leftTriples, rightTriples...), append(leftFilters, rightFilters...), nil
	case *rdflibsparql.FilterPattern:
		triples, filters, err := basicGraphPatternParts(p.Pattern)
		if err != nil {
			return nil, nil, err
		}
		return triples, append(filters, p.Expr), nil
	default:
		return nil, nil, fmt.Errorf("only basic SPARQL triple patterns and FILTER expressions are supported yet")
	}
}

func filterSQL(expr rdflibsparql.Expr, bindings map[string]sqlBinding, layout ObjectLayout) (string, error) {
	switch e := expr.(type) {
	case *rdflibsparql.BinaryExpr:
		if e.Op == "&&" || e.Op == "||" {
			left, err := filterSQL(e.Left, bindings, layout)
			if err != nil {
				return "", err
			}
			right, err := filterSQL(e.Right, bindings, layout)
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
		left, err := filterOperandSQL(e.Left, e.Right, bindings, layout)
		if err != nil {
			return "", err
		}
		right, err := filterOperandSQL(e.Right, e.Left, bindings, layout)
		if err != nil {
			return "", err
		}
		return left + " " + e.Op + " " + right, nil
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

func filterOperandSQL(expr rdflibsparql.Expr, other rdflibsparql.Expr, bindings map[string]sqlBinding, layout ObjectLayout) (string, error) {
	switch e := expr.(type) {
	case *rdflibsparql.VarExpr:
		binding, ok := bindings[e.Name]
		if !ok {
			return "", fmt.Errorf("FILTER variable ?%s is not bound by a supported triple pattern", e.Name)
		}
		if binding.column == "object" && layout == TypedObjects {
			if literal, ok := other.(*rdflibsparql.LiteralExpr); ok && literalIsFloat(literal.Value) {
				return binding.alias + ".object_float", nil
			}
			if _, ok := other.(*rdflibsparql.IRIExpr); ok {
				return binding.alias + ".object_iri", nil
			}
			return binding.alias + ".object_string", nil
		}
		return binding.expr(layout), nil
	case *rdflibsparql.LiteralExpr:
		return termSQL(e.Value), nil
	case *rdflibsparql.IRIExpr:
		return sqlString(e.Value), nil
	default:
		return "", fmt.Errorf("unsupported SPARQL FILTER operand %T", expr)
	}
}

func literalIsFloat(term rdflibgo.Term) bool {
	_, err := strconv.ParseFloat(term.String(), 64)
	return err == nil
}

func termSQL(term rdflibgo.Term) string {
	if _, err := strconv.ParseFloat(term.String(), 64); err == nil {
		return term.String()
	}
	return sqlString(term.String())
}

func constantClauses(alias string, column string, raw string, prefixes map[string]string, layout ObjectLayout) ([]string, error) {
	term, err := parseSPARQLTerm(raw, prefixes)
	if err != nil {
		return nil, err
	}
	if column != "object" || layout == SimpleObjects {
		return []string{alias + "." + column + " = " + sqlString(term.value)}, nil
	}
	switch term.kind {
	case "iri":
		return []string{alias + ".object_iri = " + sqlString(term.value)}, nil
	case "literal":
		if _, err := strconv.ParseFloat(term.value, 64); err == nil {
			return []string{alias + ".object_float = " + term.value}, nil
		}
		return []string{alias + ".object_string = " + sqlString(term.value)}, nil
	default:
		return nil, fmt.Errorf("unsupported object term %q", raw)
	}
}

type sparqlTerm struct {
	kind     string
	value    string
	datatype string
}

func parseSPARQLTerm(raw string, prefixes map[string]string) (sparqlTerm, error) {
	if strings.HasPrefix(raw, "<") && strings.HasSuffix(raw, ">") {
		return sparqlTerm{kind: "iri", value: raw[1 : len(raw)-1]}, nil
	}
	if strings.HasPrefix(raw, "\"") || strings.HasPrefix(raw, "'") {
		literal, datatype, err := parseLiteral(raw, prefixes)
		return sparqlTerm{kind: "literal", value: literal, datatype: datatype}, err
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

func parseLiteral(raw string, prefixes map[string]string) (string, string, error) {
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
		return "", "", fmt.Errorf("invalid SPARQL literal %q", raw)
	}
	value, err := strconv.Unquote(raw[:end+1])
	if err != nil {
		return "", "", fmt.Errorf("invalid SPARQL literal %q: %w", raw, err)
	}
	datatype := rdflibgo.XSDString.Value()
	rest := raw[end+1:]
	if strings.HasPrefix(rest, "^^<") && strings.HasSuffix(rest, ">") {
		datatype = rest[3 : len(rest)-1]
	} else if strings.HasPrefix(rest, "^^") {
		prefixed := rest[2:]
		idx := strings.Index(prefixed, ":")
		if idx < 0 {
			return "", "", fmt.Errorf("invalid SPARQL datatype %q", rest)
		}
		namespace, ok := prefixes[prefixed[:idx]]
		if !ok {
			return "", "", fmt.Errorf("unknown SPARQL prefix %q", prefixed[:idx])
		}
		datatype = namespace + prefixed[idx+1:]
	}
	return value, datatype, nil
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
