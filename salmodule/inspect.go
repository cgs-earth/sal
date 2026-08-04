package salmodule

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type inspectCmd struct {
	Module string `arg:"positional,required" placeholder:"MODULE" help:"The salmodule:// IRI, or the [HOST/]OWNER/REPO, of the module to inspect"`
}

func (cmd *inspectCmd) Run() error {
	ontology, err := Inspect(context.Background(), cmd.Module)
	if err != nil {
		return err
	}
	// the document a module prints is not necessarily formatted for a human
	var indented bytes.Buffer
	if err := json.Indent(&indented, ontology.Document, "", "  "); err != nil {
		fmt.Println(string(ontology.Document))
		return nil
	}
	fmt.Println(indented.String())
	return nil
}

// Inspect clones the module's repository, builds its Dockerfile, and runs its
// ontology command, returning the vocabulary the module published. Docker's own
// layer cache makes a repeated inspection of an unchanged module cheap, and the
// shared resolver keeps a module referenced twice in one invocation to a single
// clone and build.
func Inspect(ctx context.Context, reference string) (*ModuleOntology, error) {
	ref, err := ParseModuleIRI(moduleIRI(reference))
	if err != nil {
		return nil, err
	}
	return Default().Ontology(ctx, ref)
}

// moduleIRI makes the salmodule:// scheme optional, so that a module can be
// inspected by the repository reference a user is more likely to have at hand.
func moduleIRI(reference string) string {
	reference = strings.TrimSpace(reference)
	if IsModuleIRI(reference) {
		return reference
	}
	reference = strings.TrimPrefix(strings.TrimPrefix(reference, "https://"), "http://")
	return ProtocolScheme + "://" + strings.TrimSuffix(strings.TrimSuffix(reference, "/"), ".git")
}
