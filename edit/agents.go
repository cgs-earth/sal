package edit

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
)

// AgentsFile is the file in a module root that coding agents read first,
// which `sal edit --agents` writes the agent prompt into so that it never
// has to be pasted by hand. The form does not offer it: a module with the
// salmodule skill installed has the contract there already, and the prompt
// is for pasting into an agent once.
const AgentsFile = "AGENTS.md"

// agentsMarker heads an AGENTS.md sal wrote, so that sal knows it may write
// it again and never writes over one someone wrote by hand.
const agentsMarker = "<!-- Written by `sal edit --agents` from ontology.jsonld; run it again after changing the ontology, and edit ontology.jsonld rather than this file. -->\n"

// ErrForeignAgentsFile is returned when AGENTS.md exists and sal did not
// write it, since what it holds is someone's own instructions.
var ErrForeignAgentsFile = errors.New(AgentsFile + " exists and was not written by sal; remove it or write the prompt elsewhere")

// AgentsFileState is what the module root holds under AgentsFile: nothing,
// a file sal wrote, or a file someone else wrote.
type AgentsFileState string

const (
	AgentsFileNone  AgentsFileState = "none"
	AgentsFileSal   AgentsFileState = "sal"
	AgentsFileOther AgentsFileState = "other"
)

func agentsFileState(dir string) (AgentsFileState, error) {
	content, err := os.ReadFile(filepath.Join(dir, AgentsFile))
	if os.IsNotExist(err) {
		return AgentsFileNone, nil
	}
	if err != nil {
		return "", err
	}
	if bytes.HasPrefix(content, []byte(agentsMarker)) {
		return AgentsFileSal, nil
	}
	return AgentsFileOther, nil
}

// WriteAgentsFile renders the agent prompt for the document and writes it as
// AGENTS.md in the module root, headed by the marker that lets sal write it
// again, refusing an AGENTS.md sal did not write. It returns the path.
func WriteAgentsFile(dir string, namespace string, document []byte) (string, error) {
	state, err := agentsFileState(dir)
	if err != nil {
		return "", err
	}
	if state == AgentsFileOther {
		return "", ErrForeignAgentsFile
	}
	prompt, err := AgentPrompt(dir, namespace, document)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, AgentsFile)
	return path, os.WriteFile(path, []byte(agentsMarker+"\n# "+filepath.Base(dir)+"\n\n"+prompt), 0644)
}
