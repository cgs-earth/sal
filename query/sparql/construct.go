package sparql

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/cgs-earth/sal/pkg/telemetry"
	rdflibgo "github.com/tggo/goRDFlib"
	rdflibsparql "github.com/tggo/goRDFlib/sparql"
	"go.opentelemetry.io/otel/attribute"
)

// constructObjectColumns are the columns a constructed triple's object is
// projected in, after subject and predicate. They are the object columns
// ExportSQL selects, in the same order, so a constructed row is read back into
// an RDF term exactly the way an exported row of the table is.
var constructObjectColumns = []string{"object_iri", "object_float", "object_integer", "object_byte", "object_time", "object_wkt", "object_string", "object_type", "object_language"}

// ConstructColumns are the columns the SQL of a CONSTRUCT query projects.
var ConstructColumns = append([]string{"subject", "predicate"}, constructObjectColumns...)

// solutionsAlias names the CTE holding a CONSTRUCT's solutions, and
// solutionKey the column identifying one of them, which is what makes a blank
// node in the template a different node per solution.
const (
	solutionsAlias = "solutions"
	solutionKey    = "__sal_solution"
)

// ConstructPrefixes reports whether a SPARQL query is a CONSTRUCT, and the
// prefixes it declares. A query that does not parse is an error.
func ConstructPrefixes(query string) (prefixes map[string]string, isConstruct bool, err error) {
	parsed, err := rdflibsparql.Parse(rewriteService(query))
	if err != nil {
		return nil, false, fmt.Errorf("parse SPARQL query: %w", err)
	}
	return parsed.Prefixes, parsed.Type == "CONSTRUCT", nil
}

// constructSQL translates a CONSTRUCT whose WHERE clause has already been
// translated into from, where, and bindings. The solutions are a CTE
// projecting every variable the template uses, and each triple of the
// template is one SELECT over it, instantiating the triple per solution; the
// SELECTs are combined with UNION, since a CONSTRUCT builds a graph and a graph
// holds a triple once. A triple the solution makes invalid, a literal as its
// subject or anything but an IRI as its predicate, is left out, as SPARQL
// specifies. LIMIT bounds the solutions, not the triples built from them.
func constructSQL(parsed *rdflibsparql.ParsedQuery, parts groupParts, from []string, where []string, bindings map[string]sqlBinding) (string, error) {
	template := parsed.Construct
	// CONSTRUCT WHERE { ... } is its own template
	if len(template) == 0 {
		for _, triple := range parts.triples {
			if triple.step != nil || triple.service != "" {
				return "", fmt.Errorf("CONSTRUCT WHERE supports only plain triple patterns; write the template out to use a property path or SERVICE")
			}
			template = append(template, rdflibsparql.TripleTemplate{Subject: triple.Subject, Predicate: triple.Predicate, Object: triple.Object})
		}
	}

	// every variable the template uses is projected once, in the order the
	// template first names it
	var columns, keyed []string
	projected := map[string]bool{}
	blankNodes := false
	for _, triple := range template {
		for _, term := range []string{triple.Subject, triple.Predicate, triple.Object} {
			if templateBlankNode(term) != "" {
				blankNodes = true
				continue
			}
			name := variableName(term)
			if name == "" || projected[name] {
				continue
			}
			binding, ok := bindings[name]
			if !ok {
				return "", fmt.Errorf("CONSTRUCT template variable ?%s is not bound by a supported triple pattern", name)
			}
			projected[name] = true
			if binding.column != "object" {
				columns = append(columns, binding.expr()+" AS "+quoteIdent(name))
				keyed = append(keyed, binding.expr())
				continue
			}
			alias := binding.alias
			exprs := []string{
				alias + ".object_iri",
				"CAST(" + alias + ".object_float AS VARCHAR)",
				"CAST(" + alias + ".object_integer AS VARCHAR)",
				"CAST(" + alias + ".object_byte AS VARCHAR)",
				timeTextExpr(alias),
				"ST_AsText(" + alias + ".object_geometry)",
				alias + ".object_string",
				alias + ".object_type",
				alias + ".object_language",
			}
			for i, column := range constructObjectColumns {
				columns = append(columns, exprs[i]+" AS "+quoteIdent(name+"."+column))
				keyed = append(keyed, exprs[i])
			}
		}
	}
	if blankNodes {
		// a solution is identified by the values it binds, not by the order it
		// was read in, so that the same data always builds the same blank nodes
		key := "'solution'"
		for _, expr := range keyed {
			key += ", COALESCE(" + expr + ", '')"
		}
		columns = append(columns, "md5(concat_ws(chr(31), "+key+")) AS "+quoteIdent(solutionKey))
	}
	if len(columns) == 0 {
		columns = []string{"1 AS " + quoteIdent(solutionKey)}
	}

	solutions := "SELECT DISTINCT " + strings.Join(columns, ", ") + "\n  FROM " + strings.Join(from, "\n  CROSS JOIN ")
	if len(where) > 0 {
		solutions += "\n  WHERE " + strings.Join(where, "\n    AND ")
	}
	if parsed.Limit >= 0 {
		solutions += "\n  LIMIT " + strconv.Itoa(parsed.Limit)
	}

	selects := make([]string, 0, len(template))
	for _, triple := range template {
		sql, err := templateTripleSQL(triple, bindings, parsed.Prefixes)
		if err != nil {
			return "", err
		}
		selects = append(selects, sql)
	}
	body := strings.Join(selects, "\nUNION\n")
	if len(selects) == 1 {
		body = strings.Replace(body, "SELECT ", "SELECT DISTINCT ", 1)
	}
	return "WITH " + solutionsAlias + " AS (\n  " + solutions + ")\n" + body, nil
}

// templateBlankNode is the label of a blank node a template names, either
// written as _:label or generated by the parser for [ ] and ( ), or "" for
// any other term.
func templateBlankNode(term string) string {
	if label, ok := strings.CutPrefix(term, "_:"); ok {
		return label
	}
	name := variableName(term)
	for _, prefix := range []string{"_bnode", "_coll", "_reifier"} {
		if strings.HasPrefix(name, prefix) {
			return strings.TrimPrefix(name, "_")
		}
	}
	return ""
}

// templateTripleSQL is the SELECT instantiating one triple of a CONSTRUCT
// template over the solutions. nullable collects the expressions a solution
// can leave NULL because the term it binds is not valid in that position; a
// row where one is NULL is not a triple and is filtered out.
func templateTripleSQL(triple rdflibsparql.TripleTemplate, bindings map[string]sqlBinding, prefixes map[string]string) (string, error) {
	var nullable []string
	solution := func(name string) string { return solutionsAlias + "." + quoteIdent(name) }
	blankNode := func(label string) string {
		return "'_:construct_" + strings.ReplaceAll(label, "'", "") + "_' || " + solution(solutionKey)
	}
	// resource is a term in subject or predicate position: an IRI, or for a
	// subject a blank node, which the table stores under a "_:" prefix.
	resource := func(term string, position string) (string, error) {
		if label := templateBlankNode(term); label != "" {
			if position == "predicate" {
				return "", fmt.Errorf("a blank node cannot be the predicate of a CONSTRUCT template triple")
			}
			return blankNode(label), nil
		}
		if name := variableName(term); name != "" {
			var expr string
			switch {
			case bindings[name].column != "object" && position == "subject":
				return solution(name), nil
			case bindings[name].column != "object":
				expr = "CASE WHEN NOT starts_with(" + solution(name) + ", '_:') THEN " + solution(name) + " END"
			case position == "subject":
				blank := solution(name + ".object_string")
				expr = "COALESCE(" + solution(name+".object_iri") + ", CASE WHEN starts_with(" + blank + ", '_:') AND " + solution(name+".object_type") + " IS NULL THEN " + blank + " END)"
			default:
				expr = solution(name + ".object_iri")
			}
			nullable = append(nullable, expr)
			return expr, nil
		}
		parsed, err := parseSPARQLTerm(term, prefixes)
		if err != nil {
			return "", err
		}
		if parsed.kind != "iri" {
			return "", fmt.Errorf("the %s of a CONSTRUCT template triple must be an IRI, not %s", position, term)
		}
		return sqlString(parsed.value), nil
	}

	subject, err := resource(triple.Subject, "subject")
	if err != nil {
		return "", err
	}
	predicate, err := resource(triple.Predicate, "predicate")
	if err != nil {
		return "", err
	}

	// the object is projected column by column; whichever the term does not
	// fill stays NULL
	object := map[string]string{}
	if label := templateBlankNode(triple.Object); label != "" {
		object["object_string"] = blankNode(label)
	} else if name := variableName(triple.Object); name != "" && bindings[name].column == "object" {
		for _, column := range constructObjectColumns {
			object[column] = solution(name + "." + column)
		}
	} else if name != "" {
		// a variable bound as a subject or predicate holds an IRI or a blank node
		object["object_iri"] = "CASE WHEN NOT starts_with(" + solution(name) + ", '_:') THEN " + solution(name) + " END"
		object["object_string"] = "CASE WHEN starts_with(" + solution(name) + ", '_:') THEN " + solution(name) + " END"
	} else {
		parsed, err := parseSPARQLTerm(triple.Object, prefixes)
		if err != nil {
			return "", err
		}
		if parsed.kind == "iri" {
			object["object_iri"] = sqlString(parsed.value)
		} else {
			// a bare number is typed by its form, as SPARQL reads it, rather
			// than as the double parseSPARQLTerm compares every number as
			if _, err := strconv.ParseInt(triple.Object, 10, 64); err == nil {
				parsed.datatype = rdflibgo.XSDInteger.Value()
			} else if _, err := strconv.ParseFloat(triple.Object, 64); err == nil && !strings.ContainsAny(triple.Object, "eE") {
				parsed.datatype = rdflibgo.XSDDecimal.Value()
			}
			object["object_string"] = sqlString(parsed.value)
			object["object_type"] = sqlString(parsed.datatype)
			if parsed.language != "" {
				object["object_language"] = sqlString(strings.ToLower(parsed.language))
			}
		}
	}

	selects := []string{subject + " AS subject", predicate + " AS predicate"}
	for _, column := range constructObjectColumns {
		expr, ok := object[column]
		if !ok {
			expr = "CAST(NULL AS VARCHAR)"
		}
		selects = append(selects, expr+" AS "+column)
	}
	sql := "SELECT " + strings.Join(selects, ", ") + "\nFROM " + solutionsAlias
	if len(nullable) > 0 {
		sql += "\nWHERE " + strings.Join(nullable, " IS NOT NULL\n  AND ") + " IS NOT NULL"
	}
	return sql, nil
}

// Construct runs a SPARQL CONSTRUCT query and calls rowFn with every triple it
// builds, as one row in the columns ConstructColumns names. The rows are
// streamed rather than buffered, and the slice passed to rowFn is reused.
func (r DuckDBRunner) Construct(ctx context.Context, query string, rowFn func([]sql.NullString) error) (err error) {
	ctx, span := telemetry.Start(ctx, "sparql.construct", attribute.String("sal.sparql.query", queryTextAttribute(query)))
	defer func() { telemetry.End(span, err) }()
	_, isConstruct, err := ConstructPrefixes(query)
	if err != nil {
		return err
	}
	if !isConstruct {
		return fmt.Errorf("not a SPARQL CONSTRUCT query")
	}
	// the runner's row limit is for a shell's page of results; a graph is
	// built whole
	r.Limit = 0
	statement, err := r.Translate(query)
	if err != nil {
		return err
	}
	return r.StreamSQL(ctx, statement, needsSpatial(statement), rowFn)
}
