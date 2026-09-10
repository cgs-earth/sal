package sparql

import (
	"fmt"
	"strings"

	"github.com/tggo/goRDFlib/paths"
	rdflibsparql "github.com/tggo/goRDFlib/sparql"
)

// pathStep is a property path reduced to what one scan answers: the
// predicates it matches and in which direction, and whether it is repeated,
// in which case the scan is the recursive closure of those edges rather than
// the table itself. A sequence a/b is not a step; it is two patterns sharing a
// hidden variable, which pathSteps splits it into.
type pathStep struct {
	predicates []string // one for p, several for p1|p2 and !(p1|p2)
	negated    bool     // !p: the scan matches every predicate but these
	inverse    bool     // ^p: the pattern's subject reads the object column and vice versa
	zero, more bool     // p?, p+, p*; both false for a single step
}

func (s pathStep) closure() bool {
	return s.zero || s.more
}

// hiddenVariablePrefix names the variables a sequence path is chained
// through. They join like any other variable and are never projected.
const hiddenVariablePrefix = "__sal_path"

func isHiddenVariable(name string) bool {
	return strings.HasPrefix(name, hiddenVariablePrefix)
}

// pathSteps rewrites `subject path object` into the triple patterns it is
// answered by. A sequence a/b becomes two patterns sharing a hidden variable,
// which is what lets every existing join rule apply to it; an inverse is
// pushed down to the steps it applies to, reversing a sequence on the way;
// everything else is one step.
func (t *translator) pathSteps(subject string, path paths.Path, object string, inverse bool) ([]patternTriple, error) {
	switch p := path.(type) {
	case *paths.InvPath:
		return t.pathSteps(subject, p.Arg, object, !inverse)
	case *paths.SequencePath:
		args := p.Args
		if inverse {
			args = make([]paths.Path, 0, len(p.Args))
			for i := len(p.Args) - 1; i >= 0; i-- {
				args = append(args, p.Args[i])
			}
		}
		var triples []patternTriple
		from := subject
		for i, arg := range args {
			to := object
			if i < len(args)-1 {
				to = fmt.Sprintf("?%s%d", hiddenVariablePrefix, t.hidden)
				t.hidden++
			}
			steps, err := t.pathSteps(from, arg, to, inverse)
			if err != nil {
				return nil, err
			}
			triples = append(triples, steps...)
			from = to
		}
		return triples, nil
	case *paths.MulPath:
		step, err := stepOf(p.Path, inverse)
		if err != nil {
			return nil, fmt.Errorf("only a single IRI, its inverse, or an alternative of IRIs can be repeated with *, + or ?: %w", err)
		}
		step.zero, step.more = p.Zero, p.More
		return []patternTriple{{Triple: triple(subject, object), step: &step}}, nil
	default:
		step, err := stepOf(path, inverse)
		if err != nil {
			return nil, err
		}
		return []patternTriple{{Triple: triple(subject, object), step: &step}}, nil
	}
}

func triple(subject string, object string) rdflibsparql.Triple {
	return rdflibsparql.Triple{Subject: subject, Object: object}
}

// stepOf reduces a path with no sequence or repetition in it to the one step
// a single scan answers.
func stepOf(path paths.Path, inverse bool) (pathStep, error) {
	switch p := path.(type) {
	case paths.URIRefPath:
		return pathStep{predicates: []string{p.URI.Value()}, inverse: inverse}, nil
	case *paths.InvPath:
		return stepOf(p.Arg, !inverse)
	case *paths.NegatedPath:
		if inverse {
			return pathStep{}, fmt.Errorf("a negated inverse path, !^p, is not supported yet")
		}
		excluded := make([]string, 0, len(p.Excluded))
		for _, iri := range p.Excluded {
			excluded = append(excluded, iri.Value())
		}
		return pathStep{predicates: excluded, negated: true}, nil
	case *paths.AlternativePath:
		var merged pathStep
		for i, arg := range p.Args {
			step, err := stepOf(arg, inverse)
			if err != nil {
				return pathStep{}, err
			}
			if step.negated || (i > 0 && step.inverse != merged.inverse) {
				return pathStep{}, fmt.Errorf("only an alternative of plain IRIs, such as schema:name|rdfs:label, or of their inverses, is supported yet")
			}
			if i == 0 {
				merged = step
				continue
			}
			merged.predicates = append(merged.predicates, step.predicates...)
		}
		return merged, nil
	case *paths.SequencePath:
		return pathStep{}, fmt.Errorf("a sequence path cannot be repeated or alternated with another path yet")
	case *paths.MulPath:
		return pathStep{}, fmt.Errorf("a repeated path cannot be repeated again or alternated with another path yet")
	default:
		return pathStep{}, fmt.Errorf("SPARQL property path %T is not supported yet", path)
	}
}

// predicateClause is the predicate test a path step adds to its scan.
func predicateClause(alias string, step pathStep) string {
	if len(step.predicates) == 1 && !step.negated {
		return alias + ".predicate = " + sqlString(step.predicates[0])
	}
	quoted := make([]string, 0, len(step.predicates))
	for _, predicate := range step.predicates {
		quoted = append(quoted, sqlString(predicate))
	}
	operator := " IN ("
	if step.negated {
		operator = " NOT IN ("
	}
	return alias + ".predicate" + operator + strings.Join(quoted, ", ") + ")"
}

// closureSQL is the scan a repeated path step reads: the transitive closure of
// the step's edges, computed whole by a recursive CTE, with columns start and
// finish. A zero-length match adds the identity of identity, the endpoint the
// pattern already binds; when nothing binds either endpoint, of every node an
// edge touches, which is where this translation narrows what SPARQL defines
// as every term of the graph. UNION rather than UNION ALL in the recursion is
// what stops it on a cycle.
func closureSQL(source string, step pathStep, identity string) string {
	from, to := "edge.subject", bindingExpr("edge", "object")
	if step.inverse {
		from, to = to, from
	}
	edges := "SELECT " + from + " AS start, " + to + " AS finish\n    FROM " + source + " AS edge\n    WHERE " + predicateClause("edge", step)

	var body string
	if step.more {
		body = "WITH RECURSIVE edges AS (\n    " + edges + "),\n  closure(start, finish) AS (\n    SELECT start, finish FROM edges\n    UNION\n    SELECT closure.start, edges.finish FROM closure JOIN edges ON edges.start = closure.finish)\n  SELECT start, finish FROM closure"
	} else {
		body = "WITH edges AS (\n    " + edges + ")\n  SELECT start, finish FROM edges"
	}
	if step.zero {
		if identity != "" {
			body += "\n  UNION ALL SELECT " + identity + ", " + identity
		} else {
			body += "\n  UNION ALL SELECT node, node FROM (SELECT start AS node FROM edges UNION SELECT finish FROM edges)"
		}
	}
	return "(" + body + ")"
}
