package build

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/cgs-earth/sal/salmodule"
	rdflibgo "github.com/tggo/goRDFlib"
)

// salModuleTask is a SAL module task instance declared in a project's RDF.
type salModuleTask struct {
	subject rdflibgo.Subject
	// classIRI is the salmodule:// term the instance is typed with.
	classIRI string
	ref      salmodule.ModuleRef
	// declaredTask records that the project's RDF types the instance as a SAL
	// Module task class itself, so the module ontology does not have to be
	// consulted to know that it is runnable.
	declaredTask bool
}

// findSalModuleTasks collects every instance in the graph typed by a term from a
// salmodule:// vocabulary. What each instance is configured with is read from its
// own properties later, once the module's vocabulary is known.
func findSalModuleTasks(graph *rdflibgo.Graph) ([]salModuleTask, error) {
	subjects := map[string]rdflibgo.Subject{}
	moduleTypes := map[string][]string{}
	declaredTasks := map[string]bool{}

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
		// taskInstanceEnvVar names the environment variable a module reads its
		// task instance from; it is declared by a module's ontology, not by a
		// project, whose instances are configured with the module's own properties
		if triple.Predicate.Value() == salmodule.Namespace+"taskInstanceEnvVar" {
			slog.Warn(triple.Subject.String() + " sets " + salmodule.Namespace + "taskInstanceEnvVar, which is ignored; configure the task with the properties the module's vocabulary defines")
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

		taskInstance, err := ontology.TaskInstance(graph, task.subject, task.classIRI)
		if err != nil {
			return fmt.Errorf("build: %w", err)
		}

		slog.Info("Running SAL module task " + task.classIRI)
		output, runErr := resolver.RunTask(ctx, task.ref, ontology.TaskInstanceEnvVar, taskInstance)
		moduleGraph, err := ontology.GraphFromTaskOutput(output)
		// a failing task reports why it failed as salmodule:Error nodes before it
		// exits, so those messages are preferred over the container's exit status
		var taskErr salmodule.TaskError
		switch {
		case errors.As(err, &taskErr):
			return fmt.Errorf("build: %w", taskErr)
		case runErr != nil:
			return fmt.Errorf("build: %w", runErr)
		case err != nil:
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
