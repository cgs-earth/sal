package edit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/salmodule"
)

// AgentPrompt renders the prompt a user hands a coding agent to implement
// the SAL module in dir from its ontology.jsonld: what the document declares,
// read out of the document itself, followed by the contract every module
// container keeps, where the SAL module agent skill is installed in the
// module (or how to install it), and how to test the result. The document
// is the source of truth and the prompt says so; what is listed here is so
// that the agent sees the tasks, their configuration, and their output
// shapes without reconstructing them from the JSON-LD.
func AgentPrompt(dir string, namespace string, document []byte) (string, error) {
	data, err := promptData(dir, namespace, document)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := promptTemplate.Execute(&out, data); err != nil {
		return "", err
	}
	return out.String(), nil
}

// promptModule is what the template renders.
type promptModule struct {
	Dir          string
	OntologyFile string
	Namespace    string
	Title        string
	Creator      string
	Version      string
	Comment      string
	// EnvVar is the environment variable the task instance arrives in.
	EnvVar string
	// Image is the docker tag the test commands build the module as.
	Image    string
	Prefixes []promptPrefix
	Tasks    []promptTask
	// Skills are the installed salmodule SKILL.md files, relative to Dir,
	// or empty when the skill is not installed.
	Skills []string
	// SkillsRepository is where the skills come from when none is installed.
	SkillsRepository string
	// Properties are the module's own properties declared as nodes of the
	// graph rather than inside a shape, which a task may be configured with.
	Properties []promptProperty
	// Other are the IDs of graph nodes the prompt does not describe, so the
	// agent knows to read them from the document.
	Other []string
}

type promptPrefix struct {
	Prefix    string
	Namespace string
}

type promptTask struct {
	ID      string
	IRI     string
	Label   string
	Comment string
	// Properties are the configuration properties the task shape constrains.
	Properties []promptProperty
	Stdout     *promptShape
	Stdin      *promptShape
	// Instance is a sample of the JSON-LD node object the run command is
	// handed for the task, built from the task shape.
	Instance string
}

// promptShape summarizes a SHACL node shape: what it targets and the
// property constraints it holds.
type promptShape struct {
	TargetClass string
	Closed      bool
	Properties  []promptProperty
}

// promptProperty summarizes one sh:property, or one property declared in
// the graph.
type promptProperty struct {
	Path     string
	Comment  string
	Datatype string
	NodeKind string
	Class    string
	Range    string
	MinCount string
	MaxCount string
	Message  string
	// Other lists the constraints the summary does not name, so none is
	// hidden from the agent.
	Other []string
}

// Cardinality words the constraint counts the way a contract reads.
func (p promptProperty) Cardinality() string {
	switch {
	case p.MinCount == "" && p.MaxCount == "":
		return "optional, repeatable"
	case p.MinCount == "" && p.MaxCount == "1":
		return "optional, at most one"
	case p.MinCount == "":
		return "optional, at most " + p.MaxCount
	case p.MaxCount == "" && p.MinCount == "0":
		return "optional, repeatable"
	case p.MaxCount == "":
		return "required, at least " + p.MinCount
	case p.MinCount == "1" && p.MaxCount == "1":
		return "required, exactly one"
	case p.MinCount == "0":
		return "optional, at most " + p.MaxCount
	case p.MinCount == p.MaxCount:
		return "required, exactly " + p.MinCount
	default:
		return "required, between " + p.MinCount + " and " + p.MaxCount
	}
}

// Kind is the value a property takes, from whichever constraint says.
func (p promptProperty) Kind() string {
	switch {
	case p.Datatype != "":
		return "a " + p.Datatype + " literal"
	case p.Class != "":
		return "an IRI of a " + p.Class
	case p.NodeKind != "":
		return strings.TrimPrefix(p.NodeKind, "sh:")
	case p.Range != "":
		return p.Range
	default:
		return "any value"
	}
}

// promptData reads the document into what the template renders. The
// document is JSON the SAL module JSON Schema has fixed the shape of, so it
// is walked as JSON rather than expanded to RDF: the prefixed names it is
// written with are the ones the agent will write too.
func promptData(dir string, namespace string, document []byte) (*promptModule, error) {
	var doc struct {
		Context map[string]any   `json:"@context"`
		Graph   []map[string]any `json:"@graph"`
	}
	if err := json.Unmarshal(document, &doc); err != nil {
		return nil, fmt.Errorf("%s is not a JSON-LD document with a @context and a @graph: %w", salmodule.OntologyFile, err)
	}
	data := &promptModule{
		Dir:              dir,
		OntologyFile:     salmodule.OntologyFile,
		Namespace:        namespace,
		EnvVar:           salmodule.DefaultTaskInstanceEnvVar,
		Image:            "sal-module-" + strings.ToLower(filepath.Base(dir)),
		Skills:           pkg.InstalledSkills(dir, pkg.SalModuleSkill),
		SkillsRepository: pkg.SkillsRepository,
	}
	for prefix, value := range doc.Context {
		if iri, ok := value.(string); ok {
			data.Prefixes = append(data.Prefixes, promptPrefix{Prefix: prefix, Namespace: iri})
		}
	}
	sort.Slice(data.Prefixes, func(i, j int) bool { return data.Prefixes[i].Prefix < data.Prefixes[j].Prefix })

	for _, node := range doc.Graph {
		id := stringOf(node["@id"])
		types := idsOf(node["@type"])
		switch {
		case contains(types, "owl:Ontology"):
			data.Title = stringOf(node["dcterms:title"])
			data.Creator = stringOf(node["dcterms:creator"])
			data.Version = stringOf(node["owl:versionInfo"])
			data.Comment = stringOf(node["rdfs:comment"])
			if envVar := stringOf(node["salmodule:taskInstanceEnvVar"]); envVar != "" {
				data.EnvVar = envVar
			}
		case isTaskClass(node):
			data.Tasks = append(data.Tasks, promptTaskOf(node, namespace))
		case contains(types, "owl:DatatypeProperty") || contains(types, "owl:ObjectProperty"):
			data.Properties = append(data.Properties, promptProperty{
				Path:    id,
				Comment: stringOf(node["rdfs:comment"]),
				Range:   idOf(node["rdfs:range"]),
			})
		default:
			if id == "" {
				id = "(blank node)"
			}
			data.Other = append(data.Other, id)
		}
	}
	if len(data.Tasks) == 0 {
		return nil, fmt.Errorf("%s declares no task class: an owl:Class whose rdfs:subClassOf is salmodule:Task", salmodule.OntologyFile)
	}
	return data, nil
}

// isTaskClass reports whether a graph node is a class subclassing one of the
// SAL Module ontology's task classes, written with the salmodule prefix.
func isTaskClass(node map[string]any) bool {
	if !contains(idsOf(node["@type"]), "owl:Class") {
		return false
	}
	for _, parent := range idsOf(node["rdfs:subClassOf"]) {
		if salmodule.IsTaskBaseClass(strings.Replace(parent, "salmodule:", salmodule.Namespace, 1)) {
			return true
		}
	}
	return false
}

func promptTaskOf(node map[string]any, namespace string) promptTask {
	task := promptTask{
		ID:      stringOf(node["@id"]),
		Label:   stringOf(node["rdfs:label"]),
		Comment: stringOf(node["rdfs:comment"]),
	}
	task.IRI = namespace + task.ID
	for _, shape := range shapesOf(node["salmodule:taskShape"]) {
		task.Properties = append(task.Properties, shape.Properties...)
	}
	if shapes := shapesOf(node["salmodule:stdoutShape"]); len(shapes) > 0 {
		task.Stdout = mergeShapes(shapes)
	}
	if shapes := shapesOf(node["salmodule:stdinShape"]); len(shapes) > 0 {
		task.Stdin = mergeShapes(shapes)
	}
	task.Instance = sampleInstance(task)
	return task
}

// mergeShapes folds the shapes a class declares under one annotation into
// one summary, since the agent needs the union of their constraints.
func mergeShapes(shapes []promptShape) *promptShape {
	merged := &promptShape{}
	for _, shape := range shapes {
		if merged.TargetClass == "" {
			merged.TargetClass = shape.TargetClass
		}
		merged.Closed = merged.Closed || shape.Closed
		merged.Properties = append(merged.Properties, shape.Properties...)
	}
	return merged
}

// summarized are the keys of a property shape the summary reads; any other
// key is listed under Other so it is not lost.
var summarized = map[string]bool{
	"sh:path": true, "sh:datatype": true, "sh:nodeKind": true, "sh:class": true,
	"sh:minCount": true, "sh:maxCount": true, "sh:message": true, "sh:name": true, "sh:description": true,
}

// shapesOf reads the node shapes an annotation holds, one or several, and
// leaves anything that is not an object out.
func shapesOf(value any) []promptShape {
	var shapes []promptShape
	for _, item := range listOf(value) {
		node, ok := item.(map[string]any)
		if !ok || len(node) == 0 {
			continue
		}
		shape := promptShape{TargetClass: idOf(node["sh:targetClass"])}
		if closed, ok := node["sh:closed"].(bool); ok {
			shape.Closed = closed
		}
		for _, item := range listOf(node["sh:property"]) {
			constraint, ok := item.(map[string]any)
			if !ok {
				continue
			}
			property := promptProperty{
				Path:     idOf(constraint["sh:path"]),
				Datatype: idOf(constraint["sh:datatype"]),
				NodeKind: idOf(constraint["sh:nodeKind"]),
				Class:    idOf(constraint["sh:class"]),
				MinCount: numberOf(constraint["sh:minCount"]),
				MaxCount: numberOf(constraint["sh:maxCount"]),
				Message:  stringOf(constraint["sh:message"]),
				Comment:  stringOf(constraint["sh:description"]),
			}
			// the path may declare the property inline, which is where the
			// form and the init skeleton put its comment and range
			if path, ok := constraint["sh:path"].(map[string]any); ok {
				if property.Comment == "" {
					property.Comment = stringOf(path["rdfs:comment"])
				}
				property.Range = idOf(path["rdfs:range"])
			}
			for key, value := range constraint {
				if summarized[key] {
					continue
				}
				encoded, err := json.Marshal(value)
				if err != nil {
					continue
				}
				property.Other = append(property.Other, key+": "+string(encoded))
			}
			sort.Strings(property.Other)
			shape.Properties = append(shape.Properties, property)
		}
		shapes = append(shapes, shape)
	}
	return shapes
}

// sampleInstance builds the JSON-LD node object sal would hand the run
// command for an instance of the task configured with every property its
// task shape constrains, with a placeholder value of the right kind for
// each, in the serialization `salmodule.ModuleOntology.TaskInstance`
// produces: a literal with a datatype as {"@value", "@type"} and an IRI as
// {"@id"}.
func sampleInstance(task promptTask) string {
	instance := map[string]any{
		"@id":   "https://example.org/project/" + task.ID + "1",
		"@type": task.ID,
	}
	for _, property := range task.Properties {
		if property.Path == "" {
			continue
		}
		instance[property.Path] = sampleValue(property)
	}
	keys := make([]string, 0, len(instance))
	for key := range instance {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out bytes.Buffer
	out.WriteString("{")
	for i, key := range keys {
		if i > 0 {
			out.WriteString(", ")
		}
		name, _ := json.Marshal(key)
		value, _ := json.Marshal(instance[key])
		out.Write(name)
		out.WriteString(": ")
		out.Write(value)
	}
	out.WriteString("}")
	return out.String()
}

func sampleValue(property promptProperty) any {
	datatype := property.Datatype
	if datatype == "" {
		datatype = property.Range
	}
	switch datatype {
	case "", "xsd:string", "rdf:langString":
		if property.NodeKind == "sh:IRI" || property.Class != "" || strings.HasPrefix(property.NodeKind, "sh:BlankNodeOrIRI") {
			return map[string]any{"@id": "https://example.org/" + property.Path}
		}
		return "example"
	case "xsd:integer", "xsd:int", "xsd:long", "xsd:nonNegativeInteger", "xsd:positiveInteger":
		return map[string]any{"@value": "3", "@type": datatype}
	case "xsd:decimal", "xsd:double", "xsd:float":
		return map[string]any{"@value": "1.5", "@type": datatype}
	case "xsd:boolean":
		return map[string]any{"@value": "true", "@type": datatype}
	case "xsd:date":
		return map[string]any{"@value": "2026-01-01", "@type": datatype}
	case "xsd:dateTime":
		return map[string]any{"@value": "2026-01-01T00:00:00Z", "@type": datatype}
	case "xsd:anyURI":
		return map[string]any{"@value": "https://example.org/resource", "@type": datatype}
	default:
		if strings.HasPrefix(datatype, "xsd:") {
			return map[string]any{"@value": "example", "@type": datatype}
		}
		return map[string]any{"@id": "https://example.org/" + property.Path}
	}
}

// stringOf is a JSON string value, or "" for anything else.
func stringOf(value any) string {
	s, _ := value.(string)
	return s
}

// idOf is the IRI a value names: a string, or the @id of an object.
func idOf(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		return stringOf(typed["@id"])
	}
	return ""
}

// idsOf is idOf over a value that may be one item or an array.
func idsOf(value any) []string {
	var ids []string
	for _, item := range listOf(value) {
		if id := idOf(item); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// listOf is a JSON array, or the one value as a list, or nothing for nil.
func listOf(value any) []any {
	switch typed := value.(type) {
	case nil:
		return nil
	case []any:
		return typed
	default:
		return []any{typed}
	}
}

// numberOf is a JSON number rendered as it was written, or "" for anything else.
func numberOf(value any) string {
	switch typed := value.(type) {
	case float64:
		if typed == float64(int64(typed)) {
			return fmt.Sprintf("%d", int64(typed))
		}
		return fmt.Sprintf("%g", typed)
	case string:
		return typed
	}
	return ""
}

func contains(items []string, item string) bool {
	for _, candidate := range items {
		if candidate == item {
			return true
		}
	}
	return false
}

var promptTemplate = template.Must(template.New("prompt").Funcs(template.FuncMap{
	"join": strings.Join,
}).Parse(`Implement the SAL module whose ontology is {{.OntologyFile}} in {{.Dir}}.

A SAL module is a git repository with a Dockerfile in its root. sal builds the image and runs it as a CLI
with two subcommands; nothing else about the language, framework, or base image matters:

    docker run IMAGE {{"salmodule ontology"}}                          # print {{.OntologyFile}} to stdout, verbatim
    docker run -e {{.EnvVar}}='{...}' IMAGE {{"salmodule run"}}   # run one task instance

{{- if .Skills}}

Before writing any code, read the SAL module skill installed in this repository, which holds the full
contract and the fixes for the mistakes modules usually make:
{{- range .Skills}}
    {{.}}
{{- end}}
{{- else}}

Before writing any code, install and read the SAL module skill, which holds the full contract and the
fixes for the mistakes modules usually make. Either of these puts it under .claude/skills/salmodule and
.agents/skills/salmodule:
    npx skills add cgs-earth/sal
    sal init --salmodule --skills
It is also at {{.SkillsRepository}}/tree/main/skills/salmodule/SKILL.md, and the specification is at
https://cgs-earth.github.io/sal/reference/salmodule-description/.
{{- end}}

## The module

- Namespace: {{.Namespace}} (every relative "@id" in {{.OntologyFile}} resolves against it)
{{- if .Title}}
- Title: {{.Title}}
{{- end}}
{{- if .Creator}}
- Creator: {{.Creator}}
{{- end}}
{{- if .Version}}
- Version: {{.Version}}
{{- end}}
{{- if .Comment}}
- Description: {{.Comment}}
{{- end}}
- Task instance environment variable: {{.EnvVar}}
- Prefixes bound in @context, which every key a task prints must be written with:
{{- range .Prefixes}}
    {{.Prefix}}: {{.Namespace}}
{{- end}}

{{.OntologyFile}} is the source of truth: read it in full before implementing, and keep "salmodule ontology"
printing exactly that file (copy it into the image and print it; do not generate it in code).
{{- if .Properties}}

Properties the ontology declares as nodes of the graph:
{{- range .Properties}}
- {{.Path}}{{if .Range}} ({{.Range}}){{end}}{{if .Comment}}: {{.Comment}}{{end}}
{{- end}}
{{- end}}
{{- if .Other}}

Other nodes in the graph, which this summary does not describe: {{join .Other ", "}}.
{{- end}}

## Tasks

The ontology declares {{len .Tasks}} task class{{if ne (len .Tasks) 1}}es{{end}}. "salmodule run" reads the task instance from ${{.EnvVar}}
as a JSON-LD node object and dispatches on its "@type", which is the class name as written below.
{{- range .Tasks}}

### {{.ID}}
{{- if .Label}}
{{.Label}}.
{{- end}}
{{- if .Comment}}
{{.Comment}}
{{- end}}

IRI: {{.IRI}}
{{- if .Properties}}

Configuration (from salmodule:taskShape; sal validates the instance against it before running the task):
{{- range .Properties}}
- {{.Path}}: {{.Kind}}, {{.Cardinality}}{{if .Comment}}. {{.Comment}}{{end}}{{if .Message}} (violation message: "{{.Message}}"){{end}}{{if .Other}} [{{join .Other "; "}}]{{end}}
{{- end}}
{{- else}}

Configuration: the task shape constrains no properties, so the instance carries only "@id" and "@type".
{{- end}}

Sample instance, in the serialization sal hands the container (a typed literal is {"@value", "@type"} and
an IRI is {"@id"}; accept both the bare and the "@value" form of a literal):
    {{.Instance}}
{{- if .Stdout}}

Output (from salmodule:stdoutShape; sal validates every line the task prints against it and stops the task
on the first violation):
- every node printed{{if .Stdout.TargetClass}} with "@type": "{{.Stdout.TargetClass}}"{{end}} must satisfy{{if .Stdout.Closed}}, and may hold no other property than{{end}}:
{{- range .Stdout.Properties}}
    - {{.Path}}: {{.Kind}}, {{.Cardinality}}{{if .Comment}}. {{.Comment}}{{end}}{{if .Message}} (violation message: "{{.Message}}"){{end}}{{if .Other}} [{{join .Other "; "}}]{{end}}
{{- end}}
{{- else}}

Output: the class declares no salmodule:stdoutShape, so sal runs the task unchecked and warns. Decide what the
task produces, declare it as a stdoutShape in {{$.OntologyFile}} (sal edit opens a form for that), and print nodes that satisfy it.
{{- end}}
{{- if .Stdin}}

Input (from salmodule:stdinShape; the nodes another task produced arrive on standard input, one JSON object per line):
- every node read{{if .Stdin.TargetClass}} with "@type": "{{.Stdin.TargetClass}}"{{end}} satisfies:
{{- range .Stdin.Properties}}
    - {{.Path}}: {{.Kind}}, {{.Cardinality}}{{if .Comment}}. {{.Comment}}{{end}}{{if .Other}} [{{join .Other "; "}}]{{end}}
{{- end}}
{{- end}}
{{- end}}

## The run command's contract

- Write output as newline delimited JSON: one complete JSON object per line, never a wrapping array and
  never pretty printed across lines. Keys are resolved through the ontology's @context, so write them with
  the prefixes above ("schema:name", not "name"), and write a typed literal as {"@value": ..., "@type": "xsd:..."}.
- A relative "@id" resolves against the project that runs the task, not the module. Use absolute IRIs for
  anything owned by an external source.
- Only stdout is data. Logs and progress go to stderr, which sal surfaces as warnings.
- On failure, print {"@type": "salmodule:Error", "rdfs:comment": "what went wrong"} and exit non-zero; sal
  reports the message and fails the build.
- To hand over a file, write it completely inside the container and then reference it as {"@id": "file:///abs/path"};
  a directory ends in a slash. sal copies it out while the task runs, so finish writing before printing the line.
- The image's ENTRYPOINT must take the subcommand as its arguments: "salmodule ontology" and "salmodule run".

## Checking the work

    sal validate --salmodule                                       # the ontology against the SAL Module specification, no build
    docker build -t {{.Image}} .
    docker run --rm {{.Image}} salmodule ontology | diff - {{.OntologyFile}}   # must print the file verbatim
{{- range .Tasks}}
    docker run --rm -e {{$.EnvVar}}='{{.Instance}}' {{$.Image}} salmodule run
{{- end}}

Each run must print one JSON object per line satisfying the stdout shape, or a salmodule:Error node, and
nothing else on stdout. Then reference the module from a SAL project with a salmodule:// prefix and run
"sal build" there; the sal skill, if installed, says how.
`))
