package sparql

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/tggo/goRDFlib/paths"
	rdflibsparql "github.com/tggo/goRDFlib/sparql"
)

// pathStep is a property path reduced to what one scan answers. A simple step
// is the predicates it matches and in which direction; a compound step is a
// sequence or an alternative of steps that one scan cannot answer, a mixed
// alternative such as p|^p or anything inside a repetition, whose edges are
// a derived table joined or united from its parts; and a repeated step, of
// either kind, scans the recursive closure of its edges rather than the
// table itself. A top-level sequence a/b is not a step; it is two patterns
// sharing a hidden variable, which pathSteps splits it into.
type pathStep struct {
	predicates []string // one for p, several for p1|p2 and !(p1|p2)
	negated    bool     // !p: the scan matches every predicate but these
	inverse    bool     // ^p: the pattern's subject reads the object column and vice versa
	zero, more bool     // p?, p+, p*; both false for a single step

	parts       []pathStep // a compound step: the steps its edges are made of
	alternative bool       // the parts are alternatives, united, rather than a sequence, joined
}

func (s pathStep) closure() bool {
	return s.zero || s.more
}

func (s pathStep) compound() bool {
	return len(s.parts) > 0
}

// derived reports that the step scans a derived table with start and finish
// columns rather than the source itself.
func (s pathStep) derived() bool {
	return s.closure() || s.compound()
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
			args = reversed(p.Args)
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
	default:
		step, err := stepOf(path, inverse)
		if err != nil {
			return nil, err
		}
		return []patternTriple{{Triple: rdflibsparql.Triple{Subject: subject, Object: object}, step: &step}}, nil
	}
}

func reversed(args []paths.Path) []paths.Path {
	result := make([]paths.Path, 0, len(args))
	for i := len(args) - 1; i >= 0; i-- {
		result = append(result, args[i])
	}
	return result
}

// stepOf reduces a path to one step: a single scan where the path is one
// predicate set in one direction, and a compound step otherwise.
func stepOf(path paths.Path, inverse bool) (pathStep, error) {
	switch p := path.(type) {
	case paths.URIRefPath:
		return pathStep{predicates: []string{p.URI.Value()}, inverse: inverse}, nil
	case *paths.InvPath:
		return stepOf(p.Arg, !inverse)
	case *paths.NegatedPath:
		excluded := make([]string, 0, len(p.Excluded))
		for _, iri := range p.Excluded {
			excluded = append(excluded, iri.Value())
		}
		return pathStep{predicates: excluded, negated: true, inverse: inverse}, nil
	case *paths.AlternativePath:
		steps := make([]pathStep, 0, len(p.Args))
		for _, arg := range p.Args {
			step, err := stepOf(arg, inverse)
			if err != nil {
				return pathStep{}, err
			}
			steps = append(steps, step)
		}
		// an alternative of plain predicates in one direction is one scan
		// with predicate IN (...)
		merged := steps[0]
		for i, step := range steps {
			if step.derived() || step.negated || step.inverse != merged.inverse {
				return pathStep{parts: steps, alternative: true}, nil
			}
			if i > 0 {
				merged.predicates = append(merged.predicates, step.predicates...)
			}
		}
		return merged, nil
	case *paths.SequencePath:
		args := p.Args
		if inverse {
			args = reversed(p.Args)
		}
		parts := make([]pathStep, 0, len(args))
		for _, arg := range args {
			step, err := stepOf(arg, inverse)
			if err != nil {
				return pathStep{}, err
			}
			parts = append(parts, step)
		}
		return pathStep{parts: parts}, nil
	case *paths.MulPath:
		inner, err := stepOf(p.Path, inverse)
		if err != nil {
			return pathStep{}, err
		}
		if inner.closure() {
			// a repetition of a repetition closes over the inner closure
			inner = pathStep{parts: []pathStep{inner}, alternative: true}
		}
		inner.zero, inner.more = p.Zero, p.More
		return inner, nil
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

// scanSQL is the derived table a step that one scan cannot answer reads,
// with columns start and finish: the closure of its edges for a repeated
// step, else the edges themselves. identity is what a zero-length match is
// the identity of, see closureSQL; depth numbers the CTEs of a closure so
// that one nested in another's edges does not shadow its names.
func (s pathStep) scanSQL(source string, identity string, depth int) string {
	if s.closure() {
		return closureSQL(source, s, identity, depth)
	}
	return "(" + s.edgesSQL(source, depth) + ")"
}

// edgesSQL is the SELECT yielding one row per pair of nodes the step connects,
// as start and finish: one scan of the source for a simple step, the join of
// its parts' edges for a sequence, and their union for an alternative. A part
// of a sequence that matches a path of length zero is expanded away rather
// than given an identity, since inside a sequence its identity is whatever
// the neighbouring part touches: a/b? is a together with a/b.
func (s pathStep) edgesSQL(source string, depth int) string {
	if !s.compound() {
		from, to := "edge.subject", bindingExpr("edge", "object")
		if s.inverse {
			from, to = to, from
		}
		return "SELECT " + from + " AS start, " + to + " AS finish\n    FROM " + source + " AS edge\n    WHERE " + predicateClause("edge", s)
	}
	if s.alternative {
		arms := make([]string, 0, len(s.parts))
		for i, part := range s.parts {
			arms = append(arms, "SELECT start, finish FROM "+part.scanSQL(source, "", depth)+" AS a"+strconv.Itoa(i))
		}
		return strings.Join(arms, "\n    UNION ALL ")
	}
	for i, part := range s.parts {
		if !part.zero {
			continue
		}
		without := append(append([]pathStep{}, s.parts[:i]...), s.parts[i+1:]...)
		present := append([]pathStep{}, s.parts...)
		present[i].zero = false
		return "SELECT start, finish FROM (" + pathStep{parts: without}.edgesSQL(source, depth) + ") AS a0\n    UNION ALL SELECT start, finish FROM (" + pathStep{parts: present}.edgesSQL(source, depth) + ") AS a1"
	}
	if len(s.parts) == 1 {
		return "SELECT start, finish FROM " + s.parts[0].scanSQL(source, "", depth) + " AS p0"
	}
	last := "p" + strconv.Itoa(len(s.parts)-1)
	sql := "SELECT p0.start, " + last + ".finish\n    FROM " + s.parts[0].scanSQL(source, "", depth) + " AS p0"
	for i, part := range s.parts[1:] {
		alias := "p" + strconv.Itoa(i+1)
		sql += "\n    JOIN " + part.scanSQL(source, "", depth) + " AS " + alias + " ON " + alias + ".start = p" + strconv.Itoa(i) + ".finish"
	}
	return sql
}

// closureSQL is the scan a repeated path step reads: the transitive closure of
// the step's edges, computed whole by a recursive CTE, with columns start and
// finish. A zero-length match adds the identity of identity, the endpoint the
// pattern already binds; when nothing binds either endpoint, of every node an
// edge touches, which is where this translation narrows what SPARQL defines
// as every term of the graph. UNION rather than UNION ALL in the recursion is
// what stops it on a cycle, and for the identity is what keeps a node a cycle
// leads back to from matching twice, since a repeated path matches each pair
// of nodes once. The edges of a compound step may hold a closure
// of their own, whose CTEs are named by depth so they never shadow these.
func closureSQL(source string, step pathStep, identity string, depth int) string {
	edges, closure := "edges", "closure"
	if depth > 0 {
		edges += strconv.Itoa(depth)
		closure += strconv.Itoa(depth)
	}
	edgeRows := step.edgesSQL(source, depth+1)

	var body string
	if step.more {
		body = "WITH RECURSIVE " + edges + " AS (\n    " + edgeRows + "),\n  " + closure + "(start, finish) AS (\n    SELECT start, finish FROM " + edges + "\n    UNION\n    SELECT " + closure + ".start, " + edges + ".finish FROM " + closure + " JOIN " + edges + " ON " + edges + ".start = " + closure + ".finish)\n  SELECT start, finish FROM " + closure
	} else {
		body = "WITH " + edges + " AS (\n    " + edgeRows + ")\n  SELECT start, finish FROM " + edges
	}
	if step.zero {
		if identity != "" {
			body += "\n  UNION SELECT " + identity + ", " + identity
		} else {
			body += "\n  UNION SELECT node, node FROM (SELECT start AS node FROM " + edges + " UNION SELECT finish FROM " + edges + ")"
		}
	}
	return "(" + body + ")"
}
