package salmodule

import "fmt"

type SalModuleCmd struct {
	// Ontology is needed so that the sal cli itself is a sal module
	Ontology *ontologyCmd `arg:"subcommand:ontology" help:"Print the ontology of the sal cli itself"`
	SalRun   *runCmd      `arg:"subcommand:run" help:"Run a sal project"`
	Inspect  *inspectCmd  `arg:"subcommand:inspect" help:"Print the ontology that a remote SAL module publishes"`
}

func (cmd *SalModuleCmd) Run() error {
	switch {
	case cmd.Ontology != nil:
		return cmd.Ontology.Run()
	case cmd.SalRun != nil:
		return cmd.SalRun.Run()
	case cmd.Inspect != nil:
		return cmd.Inspect.Run()
	default:
		return fmt.Errorf("salmodule must be ran with a subcommand")
	}
}
