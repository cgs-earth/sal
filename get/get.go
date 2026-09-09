package get

import (
	"fmt"

	"github.com/cgs-earth/sal/pkg"
)

// GetCmd groups the lookups that read the RDF resources inside a built data
// product, plus `vocabularies`, which reads the pinned vocabulary and project
// ontology nodes in .sal/config.jsonld instead of the data product. Anything
// about the Iceberg table that holds a built data product belongs in
// `sal query` instead.
//
// The resource lookups list only the resources the project itself defines by
// default, which are the ones named under the project base; an imported
// ontology or a module task contributes resources under its own namespace,
// and those are listed only with each lookup's own --all flag. The flag is
// declared on each lookup rather than here, since it has no meaning for
// `statements` or `vocabularies`.
type GetCmd struct {
	Classes      *classesCmd      `arg:"subcommand:classes" help:"List the RDF classes the project declares with the annotations each one carries"`
	Datatypes    *datatypesCmd    `arg:"subcommand:datatypes" help:"List the RDF datatypes the project declares"`
	Instances    *instancesCmd    `arg:"subcommand:instances" help:"List the resources the project instantiates with the class each one is typed with"`
	Properties   *propertiesCmd   `arg:"subcommand:properties" help:"List the RDF properties the project declares with the type each one is declared with"`
	Shapes       *shapesCmd       `arg:"subcommand:shapes" help:"List the SHACL shapes the project declares with the class each one targets"`
	Statements   *statementsCmd   `arg:"subcommand:statements" help:"List the statements of the data product, one row per triple with the object's datatype and language tag"`
	Vocabularies *vocabulariesCmd `arg:"subcommand:vocabularies" help:"List the vocabularies the project has pinned, and whether each one is imported"`
}

func (cmd *GetCmd) Run() error {
	switch {
	case cmd.Classes != nil:
		return cmd.Classes.Run()
	case cmd.Datatypes != nil:
		return cmd.Datatypes.Run()
	case cmd.Instances != nil:
		return cmd.Instances.Run()
	case cmd.Properties != nil:
		return cmd.Properties.Run()
	case cmd.Shapes != nil:
		return cmd.Shapes.Run()
	case cmd.Statements != nil:
		return cmd.Statements.Run()
	case cmd.Vocabularies != nil:
		return cmd.Vocabularies.Run()
	default:
		return fmt.Errorf("get must be ran with a subcommand")
	}
}

// subjectPrefix is the prefix a resource lookup restricts its subjects to:
// the project base, or nothing at all when the lookup's --all was given. The base comes
// from the Git remote, the same base `sal build` resolves the project's own
// relative terms against, so it is exactly the namespace the project's own
// resources are named under.
func subjectPrefix(all bool) (string, error) {
	if all {
		return "", nil
	}
	return pkg.DefaultSalBase()
}

// noneFound reports an empty listing. A listing restricted to the project
// base says so and that --all would widen it; one over every namespace says
// instead what the data product would have had to declare to fill it.
func noneFound(what, declaresNone, prefix string) {
	if prefix == "" {
		fmt.Printf("no %s found; %s\n", what, declaresNone)
		return
	}
	fmt.Printf("no %s found under the project base %s; run with --all to list every namespace\n", what, prefix)
}
