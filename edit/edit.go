package edit

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/cgs-earth/sal/salmodule"
)

// EditCmd opens the browser form that edits a SAL module's ontology.jsonld,
// the same form `sal init --salmodule` creates it with, in the module the
// current directory belongs to.
type EditCmd struct {
	Prompt bool `arg:"--prompt" help:"print the prompt that asks a coding agent to implement the module from ontology.jsonld, instead of opening the form"`
	Agents bool `arg:"--agents" help:"write that prompt as AGENTS.md in the module root, instead of opening the form; an AGENTS.md sal did not write is left alone"`
	Port   int  `arg:"--port" help:"serve the form on this localhost port instead of an ephemeral one, for forwarding it over SSH"`
	UI     bool `arg:"--ui" help:"open the browser even where sal would not on its own (CI, an SSH session, no terminal or display); the form is served either way"`
}

// ErrNotAModule is returned when the current directory holds no
// ontology.jsonld, since there is nothing to edit.
var ErrNotAModule = errors.New("the current directory is not a SAL module: it has no " + salmodule.OntologyFile + "; run `sal init --salmodule` to create one")

func (cmd *EditCmd) Run() error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, salmodule.OntologyFile)
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return ErrNotAModule
	}
	form, err := NewForm(dir, "edit")
	if err != nil {
		return err
	}
	if cmd.Prompt || cmd.Agents {
		document, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if cmd.Agents {
			written, err := WriteAgentsFile(dir, form.Namespace, document)
			if err != nil {
				return err
			}
			slog.Info("wrote the agent prompt to " + written)
		}
		if cmd.Prompt {
			prompt, err := AgentPrompt(dir, form.Namespace, document)
			if err != nil {
				return err
			}
			fmt.Print(prompt)
		}
		return nil
	}
	return form.Serve(cmd.Port, cmd.UI)
}
