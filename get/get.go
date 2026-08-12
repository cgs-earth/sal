package get

import (
	"fmt"
)

// GetCmd groups the lookups that read the RDF resources inside a built data
// product, plus `imports`, which reads the project's ontology node in .sal/config.jsonld
// instead of the data product. Anything about the Iceberg table that holds a
// built data product belongs in `sal query` instead.
type GetCmd struct {
	Classes   *classesCmd   `arg:"subcommand:classes" help:"List the RDF classes that resources in the data product are typed with"`
	Datatypes *datatypesCmd `arg:"subcommand:datatypes" help:"List the RDF datatypes the data product declares"`
	Instances *instancesCmd `arg:"subcommand:instances" help:"List the resources in the data product with the class each one is typed with"`
	Shapes    *shapesCmd    `arg:"subcommand:shapes" help:"List the SHACL shapes the data product declares with the class each one targets"`
	Imports   *importsCmd   `arg:"subcommand:imports" help:"List the ontologies, SAL modules, and OCI artifacts the project imports"`
}

func (cmd *GetCmd) Run() error {
	switch {
	case cmd.Classes != nil:
		return cmd.Classes.Run()
	case cmd.Datatypes != nil:
		return cmd.Datatypes.Run()
	case cmd.Instances != nil:
		return cmd.Instances.Run()
	case cmd.Shapes != nil:
		return cmd.Shapes.Run()
	case cmd.Imports != nil:
		return cmd.Imports.Run()
	default:
		return fmt.Errorf("get must be ran with a subcommand")
	}
}
