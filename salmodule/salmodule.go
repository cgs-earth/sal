package salmodule

import "fmt"

type SalModuleCmd struct {
	// Ontology is needed so that the sal cli itself is a sal module
	Ontology *ontologyCmd `arg:"subcommand:ontology" help:"Print the ontology of the sal cli itself"`
	Run      *runCmd      `arg:"subcommand:run" help:"Run a sal project"`
}

func Run(cmd *SalModuleCmd) error {
	switch {
	case cmd.Ontology != nil:
		return cmd.Ontology.Run()
	case cmd.Run != nil:
		return cmd.Run.Run()
	default:
		return fmt.Errorf("salmodule must be ran with a subcommand")
	}
}
