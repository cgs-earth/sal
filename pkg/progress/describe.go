package progress

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cgs-earth/sal/pkg"
	"go.opentelemetry.io/otel/attribute"
)

// hiddenSteps are the spans the progress pane leaves out: a DuckDB statement
// is recorded per query a CONSTRUCT runs, which says nothing a reader of the
// pane wants beyond the query step above it.
var hiddenSteps = map[string]bool{"duckdb.query": true}

// describe names a step for the progress pane and summarizes what its
// attributes say about it, both from the span's name and attributes. A step
// the pane does not know is shown by its span name so that a new span is
// never invisible.
func describe(step *Step, now time.Time) (label string, detail string) {
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
	case "sal.push", "sal.pull", "sal.clone":
		label = "sal " + strings.TrimPrefix(step.Name, "sal.")
		if repository := step.attrString("sal.oci.repository"); repository != "" {
			parts = append(parts, repository)
		}
	case "oci.fetch_manifest":
		label = "Fetch the manifest"
		if reference := step.attrString("sal.oci.reference"); reference != "" {
			label += " of " + reference
		}
	case "oci.pull_layers":
		label = "Pull layers"
		if destination := step.attrString("sal.oci.destination"); destination != "" {
			label += " to " + destination
		}
		parts = append(parts, transferDetail(step, now)...)
	case "oci.push_layers":
		label = "Upload layers"
		if repository := step.attrString("sal.oci.repository"); repository != "" {
			label += " to " + repository
		}
		parts = append(parts, transferDetail(step, now)...)
	case "oci.push_manifest":
		label = "Push the manifest"
		if tags, ok := step.Attrs["sal.oci.tags"]; ok && tags.Type() == attribute.STRINGSLICE {
			parts = append(parts, "tagged "+strings.Join(tags.AsStringSlice(), ", "))
		}
	case "clone.repository":
		label = "Clone " + step.attrString("sal.clone.source")
	case "clone.checkout":
		label = "Check out " + step.attrString("sal.clone.commit")
	case "clone.init":
		label = "Initialize the SAL project in " + step.attrString("sal.clone.directory")
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
		// the task reports its documents every 20,000 it reads, so the count
		// only shows once a task is large enough to need a sense of scale
		if documents, ok := step.attrInt("sal.module.documents"); ok && documents >= 20000 {
			parts = append(parts, groupDigits(documents)+" JSON-LD documents")
		}
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
	case "iceberg.read_existing":
		label = "Read the triple hashes the table holds"
		count("sal.triples.existing", "triple")
	case "iceberg.diff":
		label = "Diff the build against the table"
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
	case "docker.ping":
		label = "Ping the docker daemon"
	case "docker.image.build":
		label = "Build the image"
	case "docker.image.inspect":
		label = "Check for a cached image"
		if status, ok := step.attrInt("http.response.status_code"); ok {
			if status == http.StatusNotFound {
				parts = append(parts, "not found, so the module is built")
			} else if status < 400 {
				parts = append(parts, "found")
			}
		}
	case "docker.container.create":
		label = "Create the container"
	case "docker.container.start":
		label = "Start the container"
	case "docker.container.logs":
		label = "Read the container's output"
	case "docker.container.wait":
		label = "Wait for the container to exit"
	case "docker.container.kill":
		label = "Kill the container"
	case "docker.container.remove":
		label = "Remove the container"
	case "docker.container.copy":
		label = "Copy files out of the container"
	case "duckdb.load_spatial":
		label = "Load the DuckDB spatial extension"
	default:
		label = step.Name
	}
	return label, strings.Join(parts, " · ")
}

// transferBarWidth is how many cells wide the bar of a transfer is drawn.
const transferBarWidth = 20

// transferDetail says how far a transfer is from the attributes a
// pkg.Transfer reports on its span: the files and bytes moved out of the
// total, a bar of the bytes, and the average speed since the step started.
// The bytes are clamped to the total, since an upload sent again after an
// auth challenge is counted again.
func transferDetail(step *Step, now time.Time) []string {
	files, ok := step.attrInt(pkg.TransferFilesAttribute)
	if !ok {
		return nil
	}
	filesDone, _ := step.attrInt(pkg.TransferFilesDoneAttribute)
	bytes, _ := step.attrInt(pkg.TransferBytesAttribute)
	bytesDone, _ := step.attrInt(pkg.TransferBytesDoneAttribute)
	bytesDone = min(bytesDone, bytes)
	parts := []string{fmt.Sprintf("%d/%d files", filesDone, files)}
	if bytes > 0 {
		parts = append(parts, formatBytes(bytesDone)+" of "+formatBytes(bytes), transferBar(bytesDone, bytes))
	}
	if elapsed := step.Elapsed(now); bytesDone > 0 && !step.Started.IsZero() && elapsed > 0 {
		parts = append(parts, formatBytes(int64(float64(bytesDone)/elapsed.Seconds()))+"/s")
	}
	return parts
}

// transferBar draws done out of total as a bar with its percentage.
func transferBar(done, total int64) string {
	filled := 0
	if total > 0 {
		filled = int(done * transferBarWidth / total)
	}
	filled = max(0, min(transferBarWidth, filled))
	bar := strings.Repeat("▰", filled) + strings.Repeat("▱", transferBarWidth-filled)
	return fmt.Sprintf("%s %d%%", bar, done*100/max(total, 1))
}

// groupDigits writes a count with its thousands separated, 1,737,674, which
// is what makes a count in the millions readable at a glance.
func groupDigits(count int64) string {
	digits := fmt.Sprint(count)
	for i := len(digits) - 3; i > 0 && digits[i-1] != '-'; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	return digits
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
