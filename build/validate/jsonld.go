package validate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/cgs-earth/json-gold/ld"
	rdflibgo "github.com/tggo/goRDFlib"
	"github.com/tggo/goRDFlib/jsonld"
)

func jsonErrorLine(content []byte, err error) int {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	var offset int64
	switch {
	case errors.As(err, &syntaxErr):
		offset = syntaxErr.Offset
	case errors.As(err, &typeErr):
		offset = typeErr.Offset
	default:
		return 1
	}
	if offset <= 0 {
		return 1
	}
	return 1 + bytes.Count(content[:min(int(offset), len(content))], []byte("\n"))
}

func validateJSONLDFile(path string, vocabsToReplace map[string]string, base string) (*rdflibgo.Graph, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("build: read %s: %w", path, err)
	}

	makeStringFromQuad := func(quad *ld.Quad) string {
		if quad.Graph != nil {
			return fmt.Sprintf("%s %s %s %s", quad.Subject.GetValue(), quad.Predicate.GetValue(), quad.Object.GetValue(), quad.Graph.GetValue())
		}
		return fmt.Sprintf("%s %s %s", quad.Subject.GetValue(), quad.Predicate.GetValue(), quad.Object.GetValue())
	}

	provenance := make(map[string]ld.RDFQuadProvenance)
	opts := ld.NewJsonLdOptions("")
	opts.RDFQuadProvenanceCallback = func(quad *ld.Quad, prov ld.RDFQuadProvenance) {
		provenance[makeStringFromQuad(quad)] = prov
	}
	proc := ld.NewJsonLdProcessor()
	_, err = proc.ToRDF(content, opts)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid JSON-LD: %w", path, err)
	}

	var terms []UsedTermsInFile
	g := rdflibgo.NewGraph(rdflibgo.WithBase(base))
	err = jsonld.Parse(g, bytes.NewReader(content), jsonld.WithBase(base))
	if err != nil {
		return nil, fmt.Errorf("%s: invalid Turtle: %w", path, err)
	}

	ctx := RdfContext{
		Prefixes: make(map[string]string),
		Vocab:    "",
	}

	if err := validateTerms(path, terms, ctx, vocabsToReplace, base); err != nil {
		return nil, err
	}
	return g, nil
}
