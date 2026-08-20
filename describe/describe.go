package describe

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/cgs-earth/sal/pkg"
	salsparql "github.com/cgs-earth/sal/query/sparql"
)

// DescribeCmd prints every statement a built data product makes about one
// subject, which is the `<subject> ?p ?o` pattern run as a filter on the
// subject column.
type DescribeCmd struct {
	Subject string `arg:"positional,required" help:"The IRI of the subject to describe, or a name relative to the project base"`
}

func (cmd *DescribeCmd) Run() error {
	subject, err := subjectIRI(cmd.Subject, pkg.DefaultSalBase)
	if err != nil {
		return err
	}

	result, err := salsparql.RunLookup(salsparql.DescribeSQL(subject))
	if err != nil {
		return err
	}
	if len(result.Rows) == 0 {
		fmt.Printf("no statements found with %s as their subject\n", subject)
		return nil
	}
	fmt.Print(salsparql.FormatTable(result.Header, result.Rows))
	return nil
}

// absoluteIRI matches a leading RFC 3986 scheme, which is what separates an IRI
// that stands on its own from a name that is relative to the project's base.
var absoluteIRI = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+\-.]*:`)

// subjectIRI reads the IRI out of what the subject was given as. A subject is
// commonly copied out of Turtle or a SPARQL pattern, where an IRI is written
// inside angle brackets that are not part of the IRI itself. A subject with no
// scheme is a term the project defined itself, so it is resolved against the
// project base the same way build resolves a relative term in the project's RDF.
func subjectIRI(raw string, salBase func() (string, error)) (string, error) {
	subject := strings.TrimSpace(raw)
	if strings.HasPrefix(subject, "<") && strings.HasSuffix(subject, ">") {
		subject = strings.TrimSpace(subject[1 : len(subject)-1])
	}
	if subject == "" {
		return "", fmt.Errorf("describe: a subject IRI is required")
	}
	// A "_:" subject is a blank node label exactly as build stores it in the
	// subject column, so it is looked up as-is rather than resolved against
	// the project base.
	if strings.HasPrefix(subject, "_:") {
		return subject, nil
	}
	if absoluteIRI.MatchString(subject) {
		return subject, nil
	}

	base, err := salBase()
	if err != nil {
		return "", fmt.Errorf("describe: %q has no scheme, so it is relative to the project base, which could not be determined: %w", subject, err)
	}
	return base + strings.TrimPrefix(subject, "/"), nil
}
