package validate

import (
	"fmt"
	"strconv"
	"strings"
)

type validationError struct {
	Path string
	Line int
	Term string
}

func (e validationError) Error() string {
	return fmt.Sprintf("%s:%d: undefined term %s", e.Path, e.Line, e.Term)
}

type undefinedPrefixError struct {
	Path   string
	Line   int
	Term   string
	Prefix string
}

func (e undefinedPrefixError) Error() string {
	return fmt.Sprintf("%s:%d: undefined term %s: prefix %s is not defined", e.Path, e.Line, e.Term, e.Prefix)
}

// vocabularyLookupError is a term that could not be checked because its
// vocabulary could not be, reported at the first line the vocabulary is used
// on. OtherLines are the later lines the same failure affects, so that one
// broken vocabulary reads as one problem rather than one per use.
type vocabularyLookupError struct {
	Path       string
	Line       int
	Term       string
	Err        error
	OtherLines []int
}

func (e *vocabularyLookupError) Error() string {
	message := fmt.Sprintf("%s:%d: failed to check vocabulary for %s: %v", e.Path, e.Line, e.Term, e.Err)
	if len(e.OtherLines) == 0 {
		return message
	}
	lines := make([]string, len(e.OtherLines))
	for i, line := range e.OtherLines {
		lines[i] = strconv.Itoa(line)
	}
	if len(lines) == 1 {
		return fmt.Sprintf("%s (also affects line %s)", message, lines[0])
	}
	return fmt.Sprintf("%s (also affects lines %s)", message, strings.Join(lines, ", "))
}

func (e *vocabularyLookupError) Unwrap() error { return e.Err }

type missingTypeError struct {
	Path string
	Line int
	IRI  string
}

func (e missingTypeError) Error() string {
	return fmt.Sprintf("%s:%d: %s must have an rdf:type definition", e.Path, e.Line, e.IRI)
}

type MultiError []error

func (e MultiError) Error() string {
	var sb strings.Builder
	for i, err := range e {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(err.Error())
	}
	return sb.String()
}

func (e MultiError) Unwrap() []error {
	return []error(e)
}
