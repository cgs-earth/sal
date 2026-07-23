package validate

import (
	"fmt"
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

type vocabularyLookupError struct {
	Path string
	Line int
	Term string
	Err  error
}

func (e vocabularyLookupError) Error() string {
	return fmt.Sprintf("%s:%d: failed to check vocabulary for %s: %v", e.Path, e.Line, e.Term, e.Err)
}

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
