package build

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/pkg/telemetry"
	"github.com/cgs-earth/sal/salmodule"
	rdflibgo "github.com/tggo/goRDFlib"
	"go.opentelemetry.io/otel/attribute"
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

	pkg.Triples(graph, nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		key := triple.Subject.String()
		subjects[key] = triple.Subject

		if triple.Predicate.Equal(rdflibgo.RDF.Type) {
			if object, ok := triple.Object.(rdflibgo.URIRef); ok {
				if salmodule.IsModuleIRI(object.Value()) {
					moduleTypes[key] = append(moduleTypes[key], object.Value())
				}
				if salmodule.IsTaskBaseClass(object.Value()) {
					declaredTasks[key] = true
					// the NodeProducer, NodeConsumer, and NodeProcessor classes are
					// inferred from a task class's shapes, not asserted; a project
					// that wants to say an instance is a task says salmodule:Task
					if salmodule.IsInferredTaskClass(object.Value()) {
						slog.Warn(triple.Subject.String() + " is typed " + object.Value() + ", which the SAL Module ontology infers from a task class's shapes rather than has declared; type it salmodule:Task, or leave the type to the module's own class")
					}
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
// and merges the RDF each module produced back into the graph, reporting how
// many tasks it ran. A relative IRI a task emits resolves against the graph's
// base, which is the project namespace, so what a module materializes is named
// as the project's own instance data unless the module wrote absolute IRIs.
// A file a task names with a file:/// IRI is copied out of the container
// into blobDir under its SHA-256 digest, a directory named with a trailing
// slash is copied whole under <digest>/, both are appended to
// blobDir's prov.jsonld, and the graph refers to either as urn:sha256:<digest>
// in place of the container path. Nothing an earlier run copied is removed;
// the blob store only grows until the user clears it.
func MaterializeSalModules(ctx context.Context, graph *rdflibgo.Graph, resolver *salmodule.Resolver, blobDir string) (tasksRun int, err error) {
	tasks, err := findSalModuleTasks(graph)
	if err != nil {
		return 0, err
	}
	modules := map[string]bool{}
	for _, task := range tasks {
		modules[task.ref.Namespace] = true
	}
	// the span over every task says up front how many tasks and modules the
	// build has ahead of it, which is what the progress pane counts against
	ctx, span := telemetry.Start(ctx, "build.materialize_modules", attribute.Int("sal.module.tasks", len(tasks)), attribute.Int("sal.modules", len(modules)))
	defer func() { telemetry.End(span, err) }()

	for _, task := range tasks {
		ran, err := materializeTask(ctx, graph, resolver, blobDir, task)
		if err != nil {
			return tasksRun, err
		}
		if ran {
			tasksRun++
		}
	}
	return tasksRun, nil
}

// materializeTask runs one task instance and merges what it produced into
// graph, reporting whether it ran at all: an instance whose class neither the
// project nor the module's ontology declares a task is left alone.
func materializeTask(ctx context.Context, graph *rdflibgo.Graph, resolver *salmodule.Resolver, blobDir string, task salModuleTask) (_ bool, err error) {
	ctx, span := telemetry.Start(ctx, "salmodule.task",
		attribute.String("sal.module", task.ref.Namespace),
		attribute.String("sal.module.task_class", task.classIRI),
		attribute.String("sal.module.task", task.subject.String()),
	)
	defer func() { telemetry.End(span, err) }()

	ontology, err := resolver.Ontology(ctx, task.ref)
	if err != nil {
		return false, fmt.Errorf("run: %w", err)
	}
	if !task.declaredTask && !ontology.IsTaskClass(task.classIRI) {
		slog.Debug("Skipping " + task.subject.String() + "; " + task.classIRI + " is not a SAL module task class")
		return false, nil
	}

	taskInstance, err := ontology.TaskInstance(graph, task.subject, task.classIRI)
	if err != nil {
		return false, fmt.Errorf("run: %w", err)
	}

	// the shape a task class declares for its output is enforced line by
	// line as the task writes. A class without one is run unchecked, with a
	// warning: declaring what a task produces is part of the SAL Module
	// contract, but a module that predates the shape is not failed for it
	validator, err := ontology.StdoutValidator(task.classIRI, graph.Base())
	if err != nil {
		return false, fmt.Errorf("run: %w", err)
	}

	slog.Info("Running SAL module task " + task.classIRI)
	if validator != nil {
		slog.Info("Validating the output of " + task.classIRI + " against its salmodule:stdoutShape")
	} else {
		slog.Warn(task.classIRI + " declares no salmodule:stdoutShape, so what its task writes is not validated; a SAL module should declare the shape of the nodes it produces")
	}
	// the output is parsed into the graph being built as the task writes it,
	// never held whole, so a task may produce more than fits in memory as
	// JSON; a task that fails leaves what it wrote so far in a graph the
	// failed build discards anyway
	before := pkg.GraphLen(graph)
	parser := ontology.NewTaskOutputParser(graph, graph.Base())
	result, runErr := resolver.RunTask(ctx, task.ref, ontology.TaskInstanceEnvVar, taskInstance, blobDir, validator, parser.Line)
	// whatever was read before the task ended is parsed even when it failed:
	// a line that violates the output shape stopped the task, so nothing
	// after it was read; a failing task reports why it failed as
	// salmodule:Error nodes before it exits, so those messages are
	// preferred over the container's exit status
	err = parser.Finish()
	var shapeErr salmodule.StdoutShapeError
	var taskErr salmodule.TaskError
	switch {
	case errors.As(runErr, &shapeErr):
		return false, fmt.Errorf("run: %w", shapeErr)
	case errors.As(err, &taskErr):
		return false, fmt.Errorf("run: %w", taskErr)
	case runErr != nil:
		return false, fmt.Errorf("run: %w", runErr)
	case err != nil:
		return false, fmt.Errorf("run: %w", err)
	}
	if err := salmodule.LinkCopiedFiles(graph, parser.FileTriples(), result.Files, blobDir); err != nil {
		return false, fmt.Errorf("run: %w", err)
	}

	materialized := pkg.GraphLen(graph) - before
	span.SetAttributes(attribute.Int("sal.triples.materialized", materialized), attribute.Int("sal.module.files_copied", len(result.Files)))
	slog.Info(fmt.Sprintf("Materialized %d triples from %s", materialized, task.classIRI))
	if len(result.Files) > 0 {
		slog.Info(fmt.Sprintf("Copied %d files from %s into %s", len(result.Files), task.classIRI, blobDir))
	}
	return true, nil
}
