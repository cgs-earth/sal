package build

import (
	"testing"

	"github.com/cgs-earth/sal/pkg"
	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
)

func TestAllNewTypesHaveRdfClassTypes(t *testing.T) {
	base, err := pkg.DefaultSalBase()
	require.NoError(t, err)

	g := rdflibgo.NewGraph(rdflibgo.WithBase(base))
	thing := rdflibgo.NewURIRefUnsafe(base + "Thing")
	thingType := rdflibgo.NewURIRefUnsafe(base + "ThingType")

	g.Add(thing, rdflibgo.RDF.Type, thingType)
	g.Add(thingType, rdflibgo.RDF.Type, rdflibgo.RDFS.Class)
	g.Add(thingType, rdflibgo.RDFS.SubClassOf, rdflibgo.RDFS.Class)

	require.NoError(t, NewTermsHaveClassDefinitions(g))
}

func TestAllNewTypesHaveRdfClassTypesRequiresTypeForLocalSubjects(t *testing.T) {
	base, err := pkg.DefaultSalBase()
	require.NoError(t, err)

	g := rdflibgo.NewGraph(rdflibgo.WithBase(base))
	thing := rdflibgo.NewURIRefUnsafe(base + "Thing")

	g.Add(thing, rdflibgo.RDFS.Label, rdflibgo.NewLiteral("Thing"))

	err = NewTermsHaveClassDefinitions(g)
	require.Error(t, err)
	require.Contains(t, err.Error(), base+"Thing must have an rdf:type definition")
}

func TestAllNewTypesHaveRdfClassTypesRequiresLocalTypesToSubclassRDFSClass(t *testing.T) {
	base, err := pkg.DefaultSalBase()
	require.NoError(t, err)

	g := rdflibgo.NewGraph(rdflibgo.WithBase(base))
	thing := rdflibgo.NewURIRefUnsafe(base + "Thing")
	thingType := rdflibgo.NewURIRefUnsafe(base + "ThingType")

	g.Add(thing, rdflibgo.RDF.Type, thingType)
	g.Add(thingType, rdflibgo.RDF.Type, rdflibgo.RDFS.Class)

	err = NewTermsHaveClassDefinitions(g)
	require.Error(t, err)
	require.Contains(t, err.Error(), base+"ThingType must be defined as a subclass of rdfs:Class")
}

func TestAllNewTypesHaveRdfClassTypesAllowsTransitiveSubclass(t *testing.T) {
	base, err := pkg.DefaultSalBase()
	require.NoError(t, err)

	g := rdflibgo.NewGraph(rdflibgo.WithBase(base))
	thing := rdflibgo.NewURIRefUnsafe(base + "Thing")
	thingType := rdflibgo.NewURIRefUnsafe(base + "ThingType")
	parentType := rdflibgo.NewURIRefUnsafe(base + "ParentType")

	g.Add(thing, rdflibgo.RDF.Type, thingType)
	g.Add(thingType, rdflibgo.RDF.Type, rdflibgo.RDFS.Class)
	g.Add(thingType, rdflibgo.RDFS.SubClassOf, parentType)
	g.Add(parentType, rdflibgo.RDF.Type, rdflibgo.RDFS.Class)
	g.Add(parentType, rdflibgo.RDFS.SubClassOf, rdflibgo.RDFS.Class)

	require.NoError(t, NewTermsHaveClassDefinitions(g))
}
