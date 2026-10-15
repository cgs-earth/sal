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

	pkg.AddTriple(g, thing, rdflibgo.RDF.Type, thingType)
	pkg.AddTriple(g, thingType, rdflibgo.RDF.Type, rdflibgo.RDFS.Class)
	pkg.AddTriple(g, thingType, rdflibgo.RDFS.SubClassOf, rdflibgo.RDFS.Class)

	require.NoError(t, NewTermsHaveClassDefinitions(g, base))
}

func TestAllNewTypesHaveRdfClassTypesRequiresTypeForLocalSubjects(t *testing.T) {
	base, err := pkg.DefaultSalBase()
	require.NoError(t, err)

	g := rdflibgo.NewGraph(rdflibgo.WithBase(base))
	thing := rdflibgo.NewURIRefUnsafe(base + "Thing")

	pkg.AddTriple(g, thing, rdflibgo.RDFS.Label, rdflibgo.NewLiteral("Thing"))

	err = NewTermsHaveClassDefinitions(g, base)
	require.Error(t, err)
	var typeDefinitionErr TermLacksTypeDefinitionErr
	require.ErrorAs(t, err, &typeDefinitionErr)
	require.Equal(t, base+"Thing", typeDefinitionErr.iri)
}

func TestAllNewTypesHaveRdfClassTypesRequiresLocalTypesToSubclassRDFSClass(t *testing.T) {
	base, err := pkg.DefaultSalBase()
	require.NoError(t, err)

	g := rdflibgo.NewGraph(rdflibgo.WithBase(base))
	thing := rdflibgo.NewURIRefUnsafe(base + "Thing")
	thingType := rdflibgo.NewURIRefUnsafe(base + "ThingType")

	pkg.AddTriple(g, thing, rdflibgo.RDF.Type, thingType)
	pkg.AddTriple(g, thingType, rdflibgo.RDF.Type, rdflibgo.RDFS.Class)

	err = NewTermsHaveClassDefinitions(g, base)
	require.Error(t, err)
	var subclassErr TermIsNotASubClassOfRDFSClassErr
	require.ErrorAs(t, err, &subclassErr)
	require.Equal(t, base+"ThingType", subclassErr.iri)
}

func TestAllNewTypesHaveRdfClassTypesAllowsTransitiveSubclass(t *testing.T) {
	base, err := pkg.DefaultSalBase()
	require.NoError(t, err)

	g := rdflibgo.NewGraph(rdflibgo.WithBase(base))
	thing := rdflibgo.NewURIRefUnsafe(base + "Thing")
	thingType := rdflibgo.NewURIRefUnsafe(base + "ThingType")
	parentType := rdflibgo.NewURIRefUnsafe(base + "ParentType")

	pkg.AddTriple(g, thing, rdflibgo.RDF.Type, thingType)
	pkg.AddTriple(g, thingType, rdflibgo.RDF.Type, rdflibgo.RDFS.Class)
	pkg.AddTriple(g, thingType, rdflibgo.RDFS.SubClassOf, parentType)
	pkg.AddTriple(g, parentType, rdflibgo.RDF.Type, rdflibgo.RDFS.Class)
	pkg.AddTriple(g, parentType, rdflibgo.RDFS.SubClassOf, rdflibgo.RDFS.Class)

	require.NoError(t, NewTermsHaveClassDefinitions(g, base))
}
