package build

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/cgs-earth/sal/salmodule"
	rdflibgo "github.com/tggo/goRDFlib"
)

// salModuleTask is a SAL module task instance declared in a project's RDF.
type salModuleTask struct {
	subject rdflibgo.Subject
	// classIRI is the salmodule:// term the instance is typed with.
	classIRI string
	ref      salmodule.ModuleRef
	// taskInstance is the JSON passed to the module through its task instance
	// environment variable.
	taskInstance string
	// declaredTask records that the project's RDF types the instance as a SAL
	// Module task class itself, so the module ontology does not have to be
	// consulted to know that it is runnable.
	declaredTask bool
}

// findSalModuleTasks collects every instance in the graph typed by a term from a
// salmodule:// vocabulary, along with the task instance it should be run with.
func findSalModuleTasks(graph *rdflibgo.Graph) ([]salModuleTask, error) {
	subjects := map[string]rdflibgo.Subject{}
	moduleTypes := map[string][]string{}
	declaredTasks := map[string]bool{}
	taskInstances := map[string]string{}

	graph.Triples(nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		key := triple.Subject.String()
		subjects[key] = triple.Subject

		if triple.Predicate.Equal(rdflibgo.RDF.Type) {
			if object, ok := triple.Object.(rdflibgo.URIRef); ok {
				if salmodule.IsModuleIRI(object.Value()) {
					moduleTypes[key] = append(moduleTypes[key], object.Value())
				}
				if salmodule.IsTaskBaseClass(object.Value()) {
					declaredTasks[key] = true
				}
			}
		}
		if triple.Predicate.Value() == salmodule.Namespace+"taskInstanceEnvVar" {
			if literal, ok := triple.Object.(rdflibgo.Literal); ok {
				taskInstances[key] = literal.Lexical()
			}
		}
		return true
	})

	keys := make([]string, 0, len(moduleTypes))
	for key := range moduleTypes {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var tasks []salModuleTask
	for _, key := range keys {
		classes := moduleTypes[key]
		sort.Strings(classes)
		for _, classIRI := range classes {
			ref, err := salmodule.ParseModuleIRI(classIRI)
			if err != nil {
				return nil, fmt.Errorf("build: %w", err)
			}
			tasks = append(tasks, salModuleTask{
				subject:      subjects[key],
				classIRI:     classIRI,
				ref:          ref,
				taskInstance: taskInstances[key],
				declaredTask: declaredTasks[key],
			})
		}
	}
	return tasks, nil
}

// MaterializeSalModules runs every SAL module task instance the graph declares
// and merges the RDF each module produced back into the graph.
func MaterializeSalModules(ctx context.Context, graph *rdflibgo.Graph, resolver *salmodule.Resolver) error {
	tasks, err := findSalModuleTasks(graph)
	if err != nil {
		return err
	}

	for _, task := range tasks {
		ontology, err := resolver.Ontology(ctx, task.ref)
		if err != nil {
			return fmt.Errorf("build: %w", err)
		}
		if !task.declaredTask && !ontology.IsTaskClass(task.classIRI) {
			slog.Debug("Skipping " + task.subject.String() + "; " + task.classIRI + " is not a SAL module task class")
			continue
		}

		taskInstance := task.taskInstance
		if taskInstance == "" {
			taskInstance, err = defaultTaskInstance(task)
			if err != nil {
				return err
			}
			slog.Warn(task.subject.String() + " has no " + salmodule.Namespace + "taskInstanceEnvVar value; running the module with only its identifier and type")
		}

		slog.Info("Running SAL module task " + task.classIRI)
		output, err := resolver.RunTask(ctx, task.ref, ontology.TaskInstanceEnvVar, taskInstance)
		if err != nil {
			return fmt.Errorf("build: %w", err)
		}
		moduleGraph, err := ontology.GraphFromTaskOutput(output)
		if err != nil {
			return fmt.Errorf("build: %w", err)
		}

		var materialized int
		moduleGraph.Triples(nil, nil, nil)(func(rdflibgo.Triple) bool {
			materialized++
			return true
		})
		mergeGraph(graph, moduleGraph)
		slog.Info(fmt.Sprintf("Materialized %d triples from %s", materialized, task.classIRI))
	}
	return nil
}

// defaultTaskInstance builds the minimal task instance node object for an
// instance that does not carry an explicit one in the project's RDF.
func defaultTaskInstance(task salModuleTask) (string, error) {
	instance, err := json.Marshal(map[string]string{
		"@id":   task.subject.String(),
		"@type": strings.TrimPrefix(task.classIRI, task.ref.Namespace),
	})
	if err != nil {
		return "", fmt.Errorf("build: encode task instance for %s: %w", task.classIRI, err)
	}
	return string(instance), nil
}
