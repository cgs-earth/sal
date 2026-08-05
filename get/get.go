package get

import "fmt"

// GetCmd groups the lookups that read the RDF resources inside a built data
// product. Anything about the Iceberg table that holds them belongs in
// `sal query` instead.
type GetCmd struct {
	Classes *classesCmd `arg:"subcommand:classes" help:"List the RDF classes that resources in the data product are typed with"`
}

func (cmd *GetCmd) Run() error {
	switch {
	case cmd.Classes != nil:
		return cmd.Classes.Run()
	default:
		return fmt.Errorf("get must be ran with a subcommand")
	}
}
