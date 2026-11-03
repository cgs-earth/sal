package sparql

import (
	"fmt"
	"strings"

	rdflibgo "github.com/tggo/goRDFlib"
	rdflibsparql "github.com/tggo/goRDFlib/sparql"
)

// The SPARQL term accessors are answered from the columns that accompany an
// object value: LANG reads object_language and DATATYPE reads object_type, so
// both apply only to a variable bound in object position. LANGMATCHES is the
// one function that takes them, and it is the standard way to filter on a
// language tag.

// literalArgument checks that a term accessor was called with one object
// variable and returns its binding, whose typed columns hold the language tag
// and datatype. A subject or predicate is never a literal, and a value a
// BIND, VALUES, UNION, or subquery bound is text that carries neither, so
// calling LANG or DATATYPE on one is reported rather than answered with NULL.
func literalArgument(call *rdflibsparql.FuncExpr, bindings map[string]sqlBinding) (sqlBinding, error) {
	if len(call.Args) != 1 {
		return sqlBinding{}, fmt.Errorf("%s takes one variable, got %d arguments", call.Name, len(call.Args))
	}
	variable, ok := call.Args[0].(*rdflibsparql.VarExpr)
	if !ok {
		return sqlBinding{}, fmt.Errorf("%s takes a variable bound by a triple pattern", call.Name)
	}
	binding, ok := bindings[variable.Name]
	if !ok {
		return sqlBinding{}, fmt.Errorf("%s variable ?%s is not bound by a supported triple pattern", call.Name, variable.Name)
	}
	if !binding.typedObject() {
		return sqlBinding{}, fmt.Errorf("%s(?%s) is not defined: ?%s is bound as a %s, and only an object can be a literal", call.Name, variable.Name, variable.Name, binding.column)
	}
	return binding, nil
}

// langExpr is what LANG reads: the stored tag, the empty string for a literal
// without one, and NULL for an IRI or blank node, which have no object_type.
// The NULL is what SPARQL specifies, an error that leaves the variable unbound
// in a projection and drops the row from a FILTER, rather than an empty tag
// that FILTER(LANG(?o) = "") would match.
func langExpr(binding sqlBinding) string {
	return "CASE WHEN " + binding.col("object_type") + " IS NULL THEN NULL ELSE COALESCE(" + binding.col("object_language") + ", '') END"
}

// langMatchesSQL translates LANGMATCHES(LANG(?x), "range") with the basic
// filtering of RFC 4647 that SPARQL specifies: "*" matches any tagged literal,
// and any other range matches a tag equal to it or extending it with a
// subtag, so "en" matches "en-us". Tags are stored lower case (see the
// object_language column), so the range is lowered to compare against them.
// Only a LANG call is accepted as the tag, since the tag of a variable is the
// only tag the table holds.
func langMatchesSQL(call *rdflibsparql.FuncExpr, bindings map[string]sqlBinding) (string, error) {
	if len(call.Args) != 2 {
		return "", fmt.Errorf("LANGMATCHES takes a language tag and a language range, got %d arguments", len(call.Args))
	}
	lang, ok := call.Args[0].(*rdflibsparql.FuncExpr)
	if !ok || lang.Name != "LANG" {
		return "", fmt.Errorf("the first argument of LANGMATCHES must be LANG(?variable)")
	}
	binding, err := literalArgument(lang, bindings)
	if err != nil {
		return "", err
	}
	rangeLiteral, ok := call.Args[1].(*rdflibsparql.LiteralExpr)
	if !ok {
		return "", fmt.Errorf("the language range of LANGMATCHES must be a string literal")
	}
	literal, ok := rangeLiteral.Value.(rdflibgo.Literal)
	if !ok {
		return "", fmt.Errorf("the language range of LANGMATCHES must be a string literal")
	}
	column := binding.col("object_language")
	languageRange := strings.ToLower(literal.Lexical())
	if languageRange == "*" {
		return column + " IS NOT NULL", nil
	}
	return "(" + column + " = " + sqlString(languageRange) + " OR starts_with(" + column + ", " + sqlString(languageRange+"-") + "))", nil
}
