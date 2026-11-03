package salmodule

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/cgs-earth/sal/specs"
)

// WriteModuleFiles scaffolds the files a SAL module repository needs: the
// ontology.jsonld document plus a Dockerfile and .dockerignore holding only a
// comment saying what belongs in each. A Dockerfile or .dockerignore that
// already exists is left alone so that re-running init never clobbers a
// module's own work; ontology.jsonld is written when overwrite is set, which
// is how `sal edit` saves the document the form edited, and left alone with a
// warning otherwise.
func WriteModuleFiles(dir string, ontology string, overwrite bool) error {
	files := []struct {
		name, content string
		overwrite     bool
	}{
		{OntologyFile, ontology, overwrite},
		{DockerfileName, "# When invoked this Dockerfile should stream JSON-LD to standard out in accordance with the salmodule specification.\n", false},
		{".dockerignore", "# Ignore any files that should not be packaged into the salmodule container ran by the SAL cli.\n", false},
	}
	for _, file := range files {
		path := filepath.Join(dir, file.name)
		if _, err := os.Stat(path); err == nil && !file.overwrite {
			slog.Warn(file.name + " already exists; leaving it unchanged")
			continue
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.WriteFile(path, []byte(file.content), 0644); err != nil {
			return err
		}
		slog.Info("wrote " + file.name)
	}
	return nil
}

// EditorSchemaURL is where .vscode/settings.json points VS Code for the
// schema, relative to the workspace root the settings file sits under.
const EditorSchemaURL = "./.vscode/" + specs.SalModuleOntologySchemaFile

// WriteEditorSchema copies the SAL module ontology JSON Schema into .vscode
// and registers it in .vscode/settings.json for ontology.jsonld, so that an
// editor checks the ontology against the SAL Module specification as it is
// written. The schema copy is sal's, not the module's, so a stale copy is
// rewritten; the settings file is the module's, so an existing one gains
// only the json.schemas entry, and one sal cannot parse (settings.json may
// hold comments) is left alone with a warning saying what to add by hand.
func WriteEditorSchema(dir string) error {
	vscodeDir := filepath.Join(dir, ".vscode")
	if err := os.MkdirAll(vscodeDir, 0755); err != nil {
		return err
	}
	schemaPath := filepath.Join(vscodeDir, specs.SalModuleOntologySchemaFile)
	if existing, err := os.ReadFile(schemaPath); err != nil && !os.IsNotExist(err) {
		return err
	} else if !bytes.Equal(existing, specs.SalModuleOntologySchema) {
		if err := os.WriteFile(schemaPath, specs.SalModuleOntologySchema, 0644); err != nil {
			return err
		}
		slog.Info("created .vscode/" + specs.SalModuleOntologySchemaFile)
	}

	entry := map[string]any{
		"fileMatch": []any{"/" + OntologyFile},
		"url":       EditorSchemaURL,
	}
	settingsPath := filepath.Join(vscodeDir, "settings.json")
	settings := map[string]any{}
	content, err := os.ReadFile(settingsPath)
	switch {
	case err != nil && !os.IsNotExist(err):
		return err
	case err == nil:
		if strings.Contains(string(content), EditorSchemaURL) {
			return nil
		}
		if err := json.Unmarshal(content, &settings); err != nil {
			slog.Warn(".vscode/settings.json could not be parsed as JSON; add the schema to it yourself", "json.schemas", entry)
			return nil
		}
	}
	schemas, _ := settings["json.schemas"].([]any)
	settings["json.schemas"] = append(schemas, entry)
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(settingsPath, append(out, '\n'), 0644); err != nil {
		return err
	}
	slog.Info("registered the schema for " + OntologyFile + " in .vscode/settings.json")
	return nil
}
