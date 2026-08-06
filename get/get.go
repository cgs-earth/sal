package get

import (
	"fmt"
)

// GetCmd groups the lookups that read the RDF resources inside a built data
// product. Anything about the Iceberg table that holds them belongs in
// `sal query` instead.
type GetCmd struct {
	Classes   *classesCmd   `arg:"subcommand:classes" help:"List the RDF classes that resources in the data product are typed with"`
	Datatypes *datatypesCmd `arg:"subcommand:datatypes" help:"List the RDF datatypes the data product declares"`
	Instances *instancesCmd `arg:"subcommand:instances" help:"List the resources in the data product with the class each one is typed with"`
}

func (cmd *GetCmd) Run() error {
	switch {
	case cmd.Classes != nil:
		return cmd.Classes.Run()
	case cmd.Datatypes != nil:
		return cmd.Datatypes.Run()
	case cmd.Instances != nil:
		return cmd.Instances.Run()
	default:
		return fmt.Errorf("get must be ran with a subcommand")
	}
}
