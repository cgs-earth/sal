package build

import (
	"bytes"
	"encoding/xml"
	"io"
	"net/url"
)

func extractRDFXMLVocabularyTerms(base string, body []byte) (map[string]bool, error) {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	base = vocabularyDocumentURL(base)
	terms := map[string]bool{}

	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			return terms, nil
		}
		if err != nil {
			return nil, err
		}

		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		collectRDFXMLElementTerms(base, start, terms)
	}
}

func collectRDFXMLElementTerms(base string, elem xml.StartElement, terms map[string]bool) {
	if elem.Name.Space != "" && elem.Name.Space != rdfNamespaceIRI {
		terms[elem.Name.Space+elem.Name.Local] = true
	}

	for _, attr := range elem.Attr {
		if attr.Name.Space != rdfNamespaceIRI {
			continue
		}
		switch attr.Name.Local {
		case "about":
			if iri := resolveRDFXMLIRI(base, attr.Value); iri != "" {
				terms[iri] = true
			}
		case "ID":
			if iri := resolveRDFXMLIRI(base, "#"+attr.Value); iri != "" {
				terms[iri] = true
			}
		}
	}
}

func resolveRDFXMLIRI(base, value string) string {
	iri, err := url.Parse(value)
	if err != nil {
		return ""
	}
	if iri.IsAbs() {
		return iri.String()
	}
	baseIRI, err := url.Parse(base)
	if err != nil {
		return value
	}
	return baseIRI.ResolveReference(iri).String()
}
