package describe

import (
	"fmt"
	"strings"

	salsparql "github.com/cgs-earth/sal/query/sparql"
)

// DescribeCmd prints every statement a built data product makes about one
// subject, which is the `<subject> ?p ?o` pattern run as a filter on the
// subject column.
type DescribeCmd struct {
	Subject string `arg:"positional,required" help:"The IRI of the subject to describe"`
}

func (cmd *DescribeCmd) Run() error {
	subject := subjectIRI(cmd.Subject)
	if subject == "" {
		return fmt.Errorf("describe: a subject IRI is required")
	}

	result, err := salsparql.RunLookup(func(layout salsparql.ObjectLayout) string {
		return salsparql.DescribeSQL(subject, layout)
	})
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

// subjectIRI reads the IRI out of what the subject was given as. A subject is
// commonly copied out of Turtle or a SPARQL pattern, where an IRI is written
// inside angle brackets that are not part of the IRI itself.
func subjectIRI(raw string) string {
	subject := strings.TrimSpace(raw)
	if strings.HasPrefix(subject, "<") && strings.HasSuffix(subject, ">") {
		subject = strings.TrimSpace(subject[1 : len(subject)-1])
	}
	return subject
}
