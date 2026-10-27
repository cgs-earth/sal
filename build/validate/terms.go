package validate

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/salmodule"
	rdflibgo "github.com/tggo/goRDFlib"
)

// VocabularyTerms is what a pinned vocabulary document defines under its
// namespace, read back out of the exact version the project pins. It is what
// the SPARQL editor of `sal serve --with-ui` completes `prefix:` names from,
// so the suggestions are the terms a build validated against rather than
// whatever a public registry says the vocabulary holds today.
type VocabularyTerms struct {
	// ID is what the pin was recorded under: a prefix namespace, or the IRI
	// of an imported ontology.
	ID string
	// Namespace is the IRI the vocabulary's terms are named under. It is ID
	// when that ends in a slash or a hash; a module's namespace carries the
	// trailing slash its import IRI drops; and an ontology imported by an
	// IRI ending in neither names its terms under the IRI plus "#" or "/",
	// whichever the document uses.
	Namespace string
	// Prefix is the name the vocabulary is conventionally declared under: the
	// prefix the document itself binds Namespace to, else a well known one,
	// else the last path segment of Namespace.
	Prefix string
	// Version is the owl:versionIRI the vocabulary is pinned at.
	Version string
	// Format is the media type the document was pinned with.
	Format string
	// Classes, Properties, and Others are the local names of the terms under
	// Namespace, each sorted and in exactly one of the three: a class is typed
	// rdfs:Class, owl:Class, or rdfs:Datatype, a property rdf:Property or one
	// of OWL's property classes, and the rest is everything else the document
	// names there, such as an enumeration member.
	Classes    []string
	Properties []string
	Others     []string
	// Statements is what the document says about each term, by local name:
	// the statements with the term as subject and one of descriptivePredicates
	// as predicate, in that order, which the SPARQL editor shows for the term
	// under the caret. A term the document only names has none.
	Statements map[string][]TermStatement
}

// TermStatement is one statement a vocabulary document makes about a term,
// rendered for reading: Predicate and an IRI Value are written as prefixed
// names when a prefix is known for the namespace (the vocabulary's own, one
// the document binds, or a conventional one) and as the full IRI otherwise.
type TermStatement struct {
	Predicate string `json:"predicate"`
	Value     string `json:"value"`
	// IRI reports that Value names a resource rather than being a literal.
	IRI bool `json:"iri,omitempty"`
	// Language is the language tag of a literal Value, when it has one.
	Language string `json:"language,omitempty"`
}

const rdfTypeIRI = "http://www.w3.org/1999/02/22-rdf-syntax-ns#type"

var classTypeIRIs = map[string]bool{
	"http://www.w3.org/2000/01/rdf-schema#Class":    true,
	"http://www.w3.org/2002/07/owl#Class":           true,
	"http://www.w3.org/2000/01/rdf-schema#Datatype": true,
}

var propertyTypeIRIs = map[string]bool{
	"http://www.w3.org/1999/02/22-rdf-syntax-ns#Property": true,
	"http://www.w3.org/2002/07/owl#ObjectProperty":        true,
	"http://www.w3.org/2002/07/owl#DatatypeProperty":      true,
	"http://www.w3.org/2002/07/owl#AnnotationProperty":    true,
	"http://www.w3.org/2002/07/owl#FunctionalProperty":    true,
}

// descriptivePredicates are the properties that say what a term is, in the
// order they are worth reading: what it is called and what it means first,
// then where it sits in the vocabulary and what it relates, then whether it
// is still in use. Anything else the document states about a term, its
// rdf:type above all, is left to the term lists.
var descriptivePredicates = []string{
	"http://www.w3.org/2000/01/rdf-schema#label",
	"http://www.w3.org/2004/02/skos/core#prefLabel",
	"http://www.w3.org/2000/01/rdf-schema#comment",
	"http://www.w3.org/2004/02/skos/core#definition",
	"https://schema.org/description",
	"http://schema.org/description",
	"http://purl.org/dc/terms/description",
	"http://purl.org/dc/elements/1.1/description",
	"http://www.w3.org/2004/02/skos/core#scopeNote",
	"http://www.w3.org/2004/02/skos/core#note",
	"http://www.w3.org/2004/02/skos/core#example",
	"http://www.w3.org/2000/01/rdf-schema#subClassOf",
	"http://www.w3.org/2000/01/rdf-schema#subPropertyOf",
	"http://www.w3.org/2000/01/rdf-schema#domain",
	"http://www.w3.org/2000/01/rdf-schema#range",
	"https://schema.org/domainIncludes",
	"http://schema.org/domainIncludes",
	"https://schema.org/rangeIncludes",
	"http://schema.org/rangeIncludes",
	"http://www.w3.org/2002/07/owl#deprecated",
	"https://schema.org/supersededBy",
	"http://schema.org/supersededBy",
	"http://www.w3.org/2000/01/rdf-schema#seeAlso",
}

// statementValuesLimit bounds how many values of one predicate a term's
// statements carry, since a class with a hundred domainIncludes is not
// something to read in an editor popup.
const statementValuesLimit = 5

// renderIRI writes iri as a prefixed name when prefixes binds its namespace,
// and as the IRI itself otherwise.
func renderIRI(iri string, prefixes map[string]string) string {
	cut := strings.LastIndexAny(iri, "/#")
	if cut < 0 {
		return iri
	}
	if prefix, ok := prefixes[iri[:cut+1]]; ok {
		name := iri[cut+1:]
		if name != "" && !strings.ContainsAny(name, "/#") {
			return prefix + ":" + name
		}
	}
	return iri
}

// conventionalPrefixes are the prefixes vocabularies are declared under by
// convention, for a document that does not bind one to its own namespace.
var conventionalPrefixes = map[string]string{
	"http://www.w3.org/1999/02/22-rdf-syntax-ns#":         "rdf",
	"http://www.w3.org/2000/01/rdf-schema#":               "rdfs",
	"http://www.w3.org/2002/07/owl#":                      "owl",
	"http://www.w3.org/2001/XMLSchema#":                   "xsd",
	"https://schema.org/":                                 "schema",
	"http://schema.org/":                                  "schema",
	"http://www.opengis.net/ont/geosparql#":               "geo",
	"http://www.opengis.net/ont/sf#":                      "sf",
	"http://www.opengis.net/def/function/geosparql/":      "geof",
	"http://purl.org/dc/terms/":                           "dcterms",
	"http://purl.org/dc/elements/1.1/":                    "dc",
	"http://www.w3.org/2004/02/skos/core#":                "skos",
	"http://xmlns.com/foaf/0.1/":                          "foaf",
	"http://www.w3.org/ns/prov#":                          "prov",
	"http://www.w3.org/ns/shacl#":                         "sh",
	"http://www.w3.org/2006/time#":                        "time",
	"http://www.w3.org/ns/sosa/":                          "sosa",
	"http://www.w3.org/ns/ssn/":                           "ssn",
	"http://www.w3.org/ns/dcat#":                          "dcat",
	"http://rdfs.org/ns/void#":                            "void",
	"http://www.w3.org/2003/01/geo/wgs84_pos#":            "wgs84",
	"http://qudt.org/schema/qudt/":                        "qudt",
	"http://qudt.org/vocab/unit/":                         "unit",
	"https://www.opengis.net/def/schema/hy_features/hyf/": "hyf",
	"http://www.opengis.net/def/schema/hy_features/hyf/":  "hyf",
	"https://w3id.org/sal/salmodule#":                     "salmodule",
	"http://purl.org/vocab/vann/":                         "vann",
}

// termsCache memoizes Terms by the version a document is pinned at. A pinned
// version names exact bytes, so what it defines never changes, and the server
// answers the terms of a vocabulary on every open of the SPARQL tab without
// parsing schema.org again each time.
var termsCache sync.Map

// Terms reads the terms the vocabulary pinned under id defines out of the
// pinned document on disk. Unlike Document, it never fetches: a pin whose
// document is missing is an error, since the server has no business
// dereferencing anything.
func (p *PinnedVocabularies) Terms(id string) (VocabularyTerms, error) {
	entry, ok := p.entries[id]
	if !ok {
		return VocabularyTerms{}, fmt.Errorf("%s is not a pinned vocabulary", id)
	}
	key := entry.Scheme + entry.Version + "\x00" + id
	if cached, ok := termsCache.Load(key); ok {
		return cached.(VocabularyTerms), nil
	}
	body, err := p.readDocument(entry)
	if err != nil {
		return VocabularyTerms{}, err
	}
	base := id
	if salmodule.IsModuleIRI(id) {
		ref, err := salmodule.ParseModuleIRI(id)
		if err != nil {
			return VocabularyTerms{}, err
		}
		base = ref.Namespace
	}
	_, graph, err := serializeRdfDataAndGetVocab(entry.MediaType, body, base)
	if err != nil {
		return VocabularyTerms{}, err
	}
	terms := termsOf(graph, id, base)
	terms.Version = entry.Scheme + entry.Version
	terms.Format = entry.MediaType
	termsCache.Store(key, terms)
	return terms, nil
}

// termsOf collects the terms graph names under the vocabulary's namespace,
// classified by what the document types them as, and the prefix to declare
// the namespace under. base is the namespace a module's terms resolve against,
// and id otherwise.
func termsOf(graph *rdflibgo.Graph, id string, base string) VocabularyTerms {
	iris := map[string]bool{}
	kinds := map[string]string{}
	pkg.Triples(graph, nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		subject, isIRI := triple.Subject.(rdflibgo.URIRef)
		if isIRI {
			iris[subject.Value()] = true
		}
		iris[triple.Predicate.Value()] = true
		switch object := triple.Object.(type) {
		case rdflibgo.URIRef:
			iris[object.Value()] = true
			if isIRI && triple.Predicate.Value() == rdfTypeIRI {
				switch {
				case classTypeIRIs[object.Value()]:
					kinds[subject.Value()] = "class"
				case propertyTypeIRIs[object.Value()] && kinds[subject.Value()] != "class":
					kinds[subject.Value()] = "property"
				}
			}
		case rdflibgo.Literal:
			iris[object.Datatype().Value()] = true
		}
		return true
	})

	namespace := vocabularyNamespace(id, base, iris)
	terms := VocabularyTerms{ID: id, Namespace: namespace, Prefix: vocabularyPrefix(graph, namespace), Statements: map[string][]TermStatement{}}
	prefixes := statementPrefixes(graph, namespace, terms.Prefix)
	for iri := range iris {
		name, ok := strings.CutPrefix(iri, namespace)
		if !ok || name == "" || strings.ContainsAny(name, "/#") {
			continue
		}
		switch kinds[iri] {
		case "class":
			terms.Classes = append(terms.Classes, name)
		case "property":
			terms.Properties = append(terms.Properties, name)
		default:
			terms.Others = append(terms.Others, name)
		}
		if statements := termStatements(graph, iri, prefixes); len(statements) > 0 {
			terms.Statements[name] = statements
		}
	}
	sort.Strings(terms.Classes)
	sort.Strings(terms.Properties)
	sort.Strings(terms.Others)
	return terms
}

// statementPrefixes is what IRIs in a term's statements are shortened with:
// the conventional prefixes, then whatever the document binds itself, then
// the vocabulary's own prefix for its namespace, each overriding the last.
func statementPrefixes(graph *rdflibgo.Graph, namespace string, prefix string) map[string]string {
	prefixes := maps.Clone(conventionalPrefixes)
	pkg.Namespaces(graph)(func(prefix string, iri rdflibgo.URIRef) bool {
		if prefix != "" && !strings.ContainsAny(prefix, ":/#") {
			prefixes[iri.Value()] = prefix
		}
		return true
	})
	prefixes[namespace] = prefix
	return prefixes
}

// termStatements is what graph says about the term iri with the descriptive
// predicates, in descriptivePredicates order and at most statementValuesLimit
// values per predicate. Values are sorted so the result does not depend on
// the order of the document, with an English or untagged literal ahead of
// one in another language, which is what the limit then keeps.
func termStatements(graph *rdflibgo.Graph, iri string, prefixes map[string]string) []TermStatement {
	var statements []TermStatement
	for _, predicate := range descriptivePredicates {
		var values []TermStatement
		predicateIRI := rdflibgo.NewURIRefUnsafe(predicate)
		pkg.Triples(graph, rdflibgo.NewURIRefUnsafe(iri), &predicateIRI, nil)(func(triple rdflibgo.Triple) bool {
			switch object := triple.Object.(type) {
			case rdflibgo.URIRef:
				values = append(values, TermStatement{Predicate: renderIRI(predicate, prefixes), Value: renderIRI(object.Value(), prefixes), IRI: true})
			case rdflibgo.Literal:
				if text := strings.TrimSpace(object.Lexical()); text != "" {
					values = append(values, TermStatement{Predicate: renderIRI(predicate, prefixes), Value: text, Language: object.Language()})
				}
			}
			return true
		})
		slices.SortFunc(values, func(a, b TermStatement) int {
			if preferred := preferredLanguageOf(a) != preferredLanguageOf(b); preferred {
				if preferredLanguageOf(a) {
					return -1
				}
				return 1
			}
			return strings.Compare(a.Value, b.Value)
		})
		statements = append(statements, values[:min(len(values), statementValuesLimit)]...)
	}
	return statements
}

// preferredLanguageOf reports whether a statement's value is an IRI or a
// literal an English speaking reader can be expected to read: untagged or
// tagged English.
func preferredLanguageOf(statement TermStatement) bool {
	language := strings.ToLower(statement.Language)
	return language == "" || language == "en" || strings.HasPrefix(language, "en-")
}

// vocabularyNamespace is the IRI the terms of the vocabulary pinned under id
// are named under. A namespace, and a module's base, already end in the
// separator; an ontology imported by a bare IRI is read for whether its terms
// hang off it with "#" or "/".
func vocabularyNamespace(id string, base string, iris map[string]bool) string {
	if strings.HasSuffix(base, "/") || strings.HasSuffix(base, "#") {
		return base
	}
	hashes, slashes := 0, 0
	for iri := range iris {
		switch {
		case strings.HasPrefix(iri, id+"#"):
			hashes++
		case strings.HasPrefix(iri, id+"/"):
			slashes++
		}
	}
	switch {
	case hashes == 0 && slashes == 0:
		return id
	case slashes > hashes:
		return id + "/"
	default:
		return id + "#"
	}
}

var prefixCharacters = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// vocabularyPrefix is the prefix to declare namespace under: what the
// document binds it to itself, else what PrefixFor says.
func vocabularyPrefix(graph *rdflibgo.Graph, namespace string) string {
	bound := ""
	pkg.Namespaces(graph)(func(prefix string, iri rdflibgo.URIRef) bool {
		if prefix != "" && iri.Value() == namespace && !strings.ContainsAny(prefix, ":/#") {
			bound = prefix
			return false
		}
		return true
	})
	if bound != "" {
		return bound
	}
	return PrefixFor(namespace)
}

// PrefixFor is the prefix a namespace is conventionally declared under, or
// for one nobody has a convention for the last segment of its path, which is
// what a project usually calls such a vocabulary.
func PrefixFor(namespace string) string {
	if prefix, ok := conventionalPrefixes[namespace]; ok {
		return prefix
	}
	trimmed := strings.TrimRight(namespace, "/#")
	segment := trimmed[strings.LastIndex(trimmed, "/")+1:]
	segment = strings.ToLower(prefixCharacters.ReplaceAllString(segment, ""))
	if segment == "" || (segment[0] >= '0' && segment[0] <= '9') {
		return "ns"
	}
	return segment
}
