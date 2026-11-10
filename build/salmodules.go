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

// documentsProgressStep is how many JSON-LD documents, the nodes a task writes
// one per line, a task reads between two reports of how far it has got: the
// progress pane shows the count once a task passes it, and the log says so
// every time it grows by that much, which is what gives a task harvesting
// millions of nodes a sense of scale.
const documentsProgressStep = 20000

// MaterializeSalModules runs every SAL module task instance the graph declares
// and hands the RDF each module produced to sink, or merges it back into the
// graph when sink is nil, reporting how many tasks it ran and, sorted, the
// namespaces the tasks' output stated its triples in (see
// TaskOutputParser.Vocabularies), which the build pins. A build hands it a
// load.Spool, so that what a module produces goes to disk as it is parsed and
// never has to fit in memory. A relative IRI a task emits resolves against the graph's
// base, which is the project namespace, so what a module materializes is named
// as the project's own instance data unless the module wrote absolute IRIs.
// A file a task names with a file:/// IRI is copied out of the container
// into blobDir under its SHA-256 digest, a directory named with a trailing
// slash is copied whole under <digest>/, both are appended to
// blobDir's prov.jsonld, and the graph refers to either as urn:sha256:<digest>
// in place of the container path. Nothing an earlier run copied is removed;
// the blob store only grows until the user clears it.
func MaterializeSalModules(ctx context.Context, graph *rdflibgo.Graph, sink salmodule.TripleSink, resolver *salmodule.Resolver, blobDir string) (tasksRun int, vocabularies []string, err error) {
	tasks, err := findSalModuleTasks(graph)
	if err != nil {
		return 0, nil, err
	}
	modules := map[string]bool{}
	for _, task := range tasks {
		modules[task.ref.Namespace] = true
	}
	// the span over every task says up front how many tasks and modules the
	// build has ahead of it, which is what the progress pane counts against
	ctx, span := telemetry.Start(ctx, "build.materialize_modules", attribute.Int("sal.module.tasks", len(tasks)), attribute.Int("sal.modules", len(modules)))
	defer func() { telemetry.End(span, err) }()

	used := map[string]bool{}
	for _, task := range tasks {
		ran, taskVocabularies, err := materializeTask(ctx, graph, sink, resolver, blobDir, task)
		if err != nil {
			return tasksRun, nil, err
		}
		if ran {
			tasksRun++
		}
		for _, namespace := range taskVocabularies {
			used[namespace] = true
		}
	}
	for namespace := range used {
		vocabularies = append(vocabularies, namespace)
	}
	sort.Strings(vocabularies)
	return tasksRun, vocabularies, nil
}

// materializeTask runs one task instance and hands what it produced to sink,
// or merges it into graph when sink is nil, reporting whether it ran at all,
// an instance whose class neither the project nor the module's ontology
// declares a task is left alone, and the namespaces its output used.
func materializeTask(ctx context.Context, graph *rdflibgo.Graph, sink salmodule.TripleSink, resolver *salmodule.Resolver, blobDir string, task salModuleTask) (_ bool, _ []string, err error) {
	ctx, span := telemetry.Start(ctx, "salmodule.task",
		attribute.String("sal.module", task.ref.Namespace),
		attribute.String("sal.module.task_class", task.classIRI),
		attribute.String("sal.module.task", task.subject.String()),
	)
	defer func() { telemetry.End(span, err) }()

	ontology, err := resolver.Ontology(ctx, task.ref)
	if err != nil {
		return false, nil, fmt.Errorf("run: %w", err)
	}
	if !task.declaredTask && !ontology.IsTaskClass(task.classIRI) {
		slog.Warn("Not running " + task.subject.String() + ": " + task.classIRI + " is not a subclass of salmodule:Task in the module's ontology, so the instance is data rather than a task; type it salmodule:Task as well if it should run")
		return false, nil, nil
	}

	taskInstance, err := ontology.TaskInstance(graph, task.subject, task.classIRI)
	if err != nil {
		return false, nil, fmt.Errorf("run: %w", err)
	}

	// the shape a task class declares for its output is enforced line by
	// line as the task writes. A class without one is run unchecked, with a
	// warning: declaring what a task produces is part of the SAL Module
	// contract, but a module that predates the shape is not failed for it.
	// The shape it declares for stderr, its control channel, is enforced the
	// same way; a class without one is the usual case, since the SAL Module
	// ontology already says what a salmodule:Error carries, an rdfs:label
	// and an rdfs:comment, which the stderr parser requires of every one
	stdoutValidator, err := ontology.StdoutValidator(task.classIRI, graph.Base())
	if err != nil {
		return false, nil, fmt.Errorf("run: %w", err)
	}
	stderrValidator, err := ontology.StderrValidator(task.classIRI, graph.Base())
	if err != nil {
		return false, nil, fmt.Errorf("run: %w", err)
	}

	slog.Info("Running SAL module task " + task.classIRI)
	if stdoutValidator != nil {
		slog.Info("Validating the output of " + task.classIRI + " against its salmodule:stdoutShape")
	} else {
		slog.Warn(task.classIRI + " declares no salmodule:stdoutShape, so what its task writes is not validated; a SAL module should declare the shape of the nodes it produces")
	}
	if stderrValidator != nil {
		slog.Info("Validating the nodes " + task.classIRI + " writes to stderr against its salmodule:stderrShape")
	}
	// the output is parsed as the task writes it, never held whole, and handed
	// to the sink, which writes it to disk, so a task may produce more than
	// fits in memory; a task that fails leaves what it wrote so far in a
	// spool the failed build discards anyway. Stderr is parsed apart from it,
	// since what a task reports there is about the run, not data for the build
	var materialized int64
	if sink == nil {
		sink = func(triple rdflibgo.Triple) error {
			pkg.AddTriple(graph, triple.Subject, triple.Predicate, triple.Object)
			return nil
		}
	}
	counted := func(triple rdflibgo.Triple) error {
		materialized++
		return sink(triple)
	}
	parser := ontology.NewTaskOutputStream(counted, graph.Base())
	reports := ontology.NewTaskStderrParser(graph.Base())
	documents := 0
	readDocument := func(line []byte, lineNumber int) error {
		if err := parser.Line(line, lineNumber); err != nil {
			return err
		}
		if documents++; documents%documentsProgressStep == 0 {
			span.SetAttributes(attribute.Int("sal.module.documents", documents), attribute.Int64("sal.triples.materialized", materialized))
			slog.Info(fmt.Sprintf("Read %d JSON-LD documents from %s", documents, task.classIRI), "triples", materialized)
		}
		return nil
	}
	result, runErr := resolver.RunTask(ctx, task.ref, taskInstance, blobDir,
		salmodule.TaskStream{Validator: stdoutValidator, Sink: readDocument},
		salmodule.TaskStream{Validator: stderrValidator, Sink: reports.Line})
	// whatever was read before the task ended is parsed even when it failed:
	// a line that violates a shape stopped the task, so nothing after it was
	// read; a failing task reports why it failed as salmodule:Error nodes
	// before it exits, on stderr where the specification puts them or on
	// stdout where an older module does, so those messages are preferred
	// over the container's exit status
	reportErr := reports.Finish()
	err = parser.Finish()
	var shapeErr salmodule.StreamShapeError
	var taskErr salmodule.TaskError
	switch {
	case errors.As(runErr, &shapeErr):
		return false, nil, fmt.Errorf("run: %w", shapeErr)
	case errors.As(reportErr, &taskErr), errors.As(err, &taskErr):
		return false, nil, fmt.Errorf("run: %w", taskErr)
	case runErr != nil:
		return false, nil, fmt.Errorf("run: %w", runErr)
	case reportErr != nil:
		return false, nil, fmt.Errorf("run: %w", reportErr)
	case err != nil:
		return false, nil, fmt.Errorf("run: %w", err)
	}
	if err := salmodule.LinkCopiedFilesTo(counted, parser.FileTriples(), result.Files, blobDir); err != nil {
		return false, nil, fmt.Errorf("run: %w", err)
	}

	span.SetAttributes(attribute.Int64("sal.triples.materialized", materialized), attribute.Int("sal.module.files_copied", len(result.Files)), attribute.Int("sal.module.documents", documents))
	slog.Info(fmt.Sprintf("Materialized %d triples from %d JSON-LD documents written by %s", materialized, documents, task.classIRI))
	if materialized == 0 && len(result.Files) == 0 {
		slog.Warn(task.classIRI + " produced no triples and copied no files; the task ran and exited cleanly but wrote nothing to stdout, so check its configuration in " + task.subject.String())
	}
	if len(result.Files) > 0 {
		slog.Info(fmt.Sprintf("Copied %d files from %s into %s", len(result.Files), task.classIRI, blobDir))
	}
	return true, parser.Vocabularies(), nil
}
