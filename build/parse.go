package build

import (
	"bytes"
	"fmt"

	"github.com/cgs-earth/json-gold/ld"
	pipld "github.com/piprate/json-gold/ld"
	rdflibgo "github.com/tggo/goRDFlib"
	"github.com/tggo/goRDFlib/jsonld"
	"github.com/tggo/goRDFlib/rdfxml"
	"github.com/tggo/goRDFlib/turtle"
)

type RDFFormat string

const (
	RDFXML RDFFormat = "application/rdf+xml"
	TURTLE RDFFormat = "text/turtle"
	JSONLD RDFFormat = "application/ld+json"
)

type jsonGoldLoaderAdapter struct {
	loader ld.DocumentLoader
}

// LoadDocument adapts SAL's JSON-LD fork loader to goRDFlib's upstream loader type.
func (a jsonGoldLoaderAdapter) LoadDocument(u string) (*pipld.RemoteDocument, error) {
	doc, err := a.loader.LoadDocument(u)
	if err != nil {
		return nil, err
	}
	return &pipld.RemoteDocument{
		DocumentURL: doc.DocumentURL,
		Document:    doc.Document,
		ContextURL:  doc.ContextURL,
	}, nil
}

func parseRdf(body []byte, base string, format RDFFormat, loaders ...ld.DocumentLoader) (*rdflibgo.Graph, error) {
	switch format {
	case RDFXML:
		g := rdflibgo.NewGraph(rdflibgo.WithBase(base))
		if err := rdfxml.Parse(g, bytes.NewReader(body), rdfxml.WithBase(base)); err != nil {
			return nil, err
		}
		return g, nil
	case TURTLE:
		g := rdflibgo.NewGraph(rdflibgo.WithBase(base))
		if err := turtle.Parse(g, bytes.NewReader(body), turtle.WithBase(base)); err != nil {
			return nil, err
		}
		return g, nil
	case JSONLD:
		g := rdflibgo.NewGraph(rdflibgo.WithBase(base))
		options := []jsonld.Option{jsonld.WithBase(base), jsonld.WithUnboundedLines()}
		if len(loaders) > 0 && loaders[0] != nil {
			options = append(options, jsonld.WithDocumentLoader(jsonGoldLoaderAdapter{loader: loaders[0]}))
		}
		if err := jsonld.Parse(g, bytes.NewReader(body), options...); err != nil {
			return nil, err
		}
		return g, nil
	default:
		return nil, fmt.Errorf("unknown format: %s", format)
	}
}
