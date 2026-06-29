package build

import (
	"fmt"
	"strings"

	"github.com/cgs-earth/sal/pkg"
	rdflibgo "github.com/tggo/goRDFlib"
)

// NewTermsHaveClassDefinitions verifies local resources have rdf:type triples and
// local rdf:type values are declared as subclasses of rdfs:Class.
func NewTermsHaveClassDefinitions(g *rdflibgo.Graph) error {
	baseForRelativePaths, err := pkg.DefaultSalBase()
	if err != nil {
		return err
	}

	localSubjects := map[string]bool{}
	typedSubjects := map[string]bool{}
	localTypes := map[string]bool{}
	subClasses := map[string][]rdflibgo.URIRef{}

	g.Triples(nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		if subj, ok := triple.Subject.(rdflibgo.URIRef); ok && strings.HasPrefix(subj.Value(), baseForRelativePaths) {
			localSubjects[subj.Value()] = true
		}
		if triple.Predicate.Equal(rdflibgo.RDF.Type) {
			if subj, ok := triple.Subject.(rdflibgo.URIRef); ok {
				typedSubjects[subj.Value()] = true
			}
			if obj, ok := triple.Object.(rdflibgo.URIRef); ok && strings.HasPrefix(obj.Value(), baseForRelativePaths) {
				localTypes[obj.Value()] = true
			}
		}
		if triple.Predicate.Equal(rdflibgo.RDFS.SubClassOf) {
			subj, subjOK := triple.Subject.(rdflibgo.URIRef)
			obj, objOK := triple.Object.(rdflibgo.URIRef)
			if subjOK && objOK {
				subClasses[subj.Value()] = append(subClasses[subj.Value()], obj)
			}
		}
		return true
	})

	var errs multiError
	for iri := range localSubjects {
		if !typedSubjects[iri] {
			errs = append(errs, fmt.Errorf("%s must have an rdf:type definition", iri))
		}
	}
	for iri := range localTypes {
		if !isSubClassOfRDFSClass(iri, subClasses, map[string]bool{}) {
			errs = append(errs, fmt.Errorf("%s must be defined as a subclass of rdfs:Class", iri))
		}
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// isSubClassOfRDFSClass follows rdfs:subClassOf links until it reaches rdfs:Class.
func isSubClassOfRDFSClass(iri string, subClasses map[string][]rdflibgo.URIRef, seen map[string]bool) bool {
	if seen[iri] {
		return false
	}
	seen[iri] = true
	for _, parent := range subClasses[iri] {
		if parent.Equal(rdflibgo.RDFS.Class) || isSubClassOfRDFSClass(parent.Value(), subClasses, seen) {
			return true
		}
	}
	return false
}
