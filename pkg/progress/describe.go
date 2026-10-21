package progress

import (
	"fmt"
	"strings"
	"time"
)

// hiddenSteps are the spans the progress pane leaves out: a DuckDB statement
// is recorded per query a CONSTRUCT runs, which says nothing a reader of the
// pane wants beyond the query step above it.
var hiddenSteps = map[string]bool{"duckdb.query": true}

// describe names a step for the progress pane and summarizes what its
// attributes say about it, both from the span's name and attributes. A step
// the pane does not know is shown by its span name so that a new span is
// never invisible.
func describe(step *Step) (label string, detail string) {
	var parts []string
	count := func(key string, singular string) {
		if value, ok := step.attrInt(key); ok {
			parts = append(parts, plural(int(value), singular))
		}
	}
	switch step.Name {
	case "sal.build", "sal.run", "sal.validate":
		label = "sal " + strings.TrimPrefix(step.Name, "sal.")
		count("sal.files", "source file")
	case "build.validate_sources":
		label = "Validate sources"
		if total, ok := step.attrInt("sal.documents"); ok {
			parts = append(parts, fmt.Sprintf("%d/%d documents", step.countChildren("validate.document", true), total))
		}
		if vocabularies := step.countChildren("validate.load_vocabulary", false); vocabularies > 0 {
			parts = append(parts, plural(vocabularies, "vocabulary"))
		}
	case "validate.document":
		label = "Validate " + step.attrString("sal.file")
		count("sal.terms", "term")
	case "validate.load_vocabulary":
		label = "Load " + step.attrString("sal.vocabulary")
		if pinned, ok := step.Attrs["sal.vocabulary.pinned"]; ok && pinned.AsBool() {
			parts = append(parts, "pinned")
		}
		count("sal.vocabulary.terms", "term")
	case "salmodule.resolve":
		label = "Resolve module " + step.attrString("sal.module")
		switch step.attrString("sal.module.cache") {
		case "pinned image":
			parts = append(parts, "reused the pinned image")
		case "image":
			parts = append(parts, "reused the image of the clone's commit")
		case "none":
			parts = append(parts, "built the image")
		}
	case "salmodule.clone":
		label = "Clone " + step.attrString("sal.module.clone_url")
	case "salmodule.build_image":
		label = "Build image " + step.attrString("sal.module.image")
	case "salmodule.ontology":
		label = "Read the ontology of " + step.attrString("sal.module")
	case "build.import_ontologies":
		label = "Import ontologies"
		if imports := step.countChildren("build.import_ontology", false); imports > 0 || !step.Running() {
			parts = append(parts, fmt.Sprintf("%d/%d imports", step.countChildren("build.import_ontology", true), imports))
		}
	case "build.import_ontology":
		label = "Import " + step.attrString("sal.import")
	case "build.materialize_modules":
		label = "Run SAL module tasks"
		if total, ok := step.attrInt("sal.module.tasks"); ok {
			parts = append(parts, fmt.Sprintf("%d/%d tasks", step.countChildren("salmodule.task", true), total))
		}
		count("sal.modules", "module")
	case "salmodule.task":
		label = "Task " + step.attrString("sal.module.task_class")
		count("sal.triples.materialized", "triple")
		count("sal.module.files_copied", "file copied")
	case "salmodule.run_task":
		label = "Run " + step.attrString("sal.module")
		if bytes, ok := step.attrInt("sal.module.output_bytes"); ok {
			parts = append(parts, formatBytes(bytes)+" read")
		}
		count("sal.module.files_copied", "file copied")
	case "build.construct":
		label = "Run CONSTRUCT queries"
		if total, ok := step.attrInt("sal.construct.queries"); ok {
			parts = append(parts, fmt.Sprintf("%d/%d queries", step.countChildren("construct.query", true), total))
		}
		count("sal.triples.constructed", "triple constructed")
	case "iceberg.stage":
		label = "Stage the build on the sal-staging branch"
	case "construct.query":
		label = "Query " + step.attrString("sal.construct.query")
		count("sal.triples.constructed", "triple")
	case "sparql.construct":
		label = "Translate and run the query"
	case "iceberg.write":
		label = "Write the Iceberg table"
		if diff := step.child("iceberg.diff"); diff != nil && !diff.Running() {
			added, _ := diff.attrInt("sal.triples.added")
			removed, _ := diff.attrInt("sal.triples.removed")
			if added == 0 && removed == 0 && !step.Running() {
				parts = append(parts, "no changes, no new snapshot")
			} else {
				parts = append(parts, fmt.Sprintf("%d added, %d removed", added, removed))
			}
		}
	case "iceberg.diff":
		label = "Diff the graph against the table"
		for _, key := range []string{"existing", "added", "removed", "unchanged"} {
			if value, ok := step.attrInt("sal.triples." + key); ok {
				parts = append(parts, fmt.Sprintf("%d %s", value, key))
			}
		}
	case "iceberg.write_data_files":
		label = "Write Parquet data files"
		count("sal.iceberg.data_files", "file")
		count("sal.triples.written", "triple")
	case "iceberg.commit":
		label = "Commit the snapshot"
		for _, key := range []string{"added", "removed"} {
			if value, ok := step.attrInt("sal.triples." + key); ok {
				parts = append(parts, fmt.Sprintf("%d %s", value, key))
			}
		}
	case "duckdb.open":
		label = "Open DuckDB"
	case "duckdb.load_spatial":
		label = "Load the DuckDB spatial extension"
	default:
		label = step.Name
	}
	return label, strings.Join(parts, " · ")
}

// plural writes a count with its noun, "1 file" or "3 files"; a noun ending
// in "y" takes "ies", and a noun with a trailing participle, "file copied",
// is counted on the noun itself, "3 files copied".
func plural(count int, singular string) string {
	noun, rest, _ := strings.Cut(singular, " ")
	if rest != "" {
		rest = " " + rest
	}
	switch {
	case count == 1:
	case strings.HasSuffix(noun, "y"):
		noun = strings.TrimSuffix(noun, "y") + "ies"
	default:
		noun += "s"
	}
	return fmt.Sprintf("%d %s%s", count, noun, rest)
}

// formatDuration writes an elapsed time the way the pane shows it: tenths of
// a second under a minute, then minutes and seconds, then hours and minutes.
func formatDuration(d time.Duration) string {
	switch {
	case d < 0:
		return "0.0s"
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// formatBytes writes a byte count in the largest unit that keeps it readable.
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, prefixes := float64(n), "KMGTPE"
	exponent := 0
	for value >= unit && exponent < len(prefixes)-1 {
		value /= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %ciB", value, prefixes[exponent-1])
}
