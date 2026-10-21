package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/cgs-earth/sal/build"
	"github.com/cgs-earth/sal/clean"
	"github.com/cgs-earth/sal/clone"
	"github.com/cgs-earth/sal/construct"
	"github.com/cgs-earth/sal/describe"
	"github.com/cgs-earth/sal/edit"
	"github.com/cgs-earth/sal/export"
	"github.com/cgs-earth/sal/get"
	"github.com/cgs-earth/sal/importation"
	"github.com/cgs-earth/sal/initialization"
	"github.com/cgs-earth/sal/pkg/progress"
	"github.com/cgs-earth/sal/pkg/telemetry"
	"github.com/cgs-earth/sal/push"
	"github.com/cgs-earth/sal/query"
	"github.com/cgs-earth/sal/salmodule"
	"github.com/cgs-earth/sal/serve"
	"github.com/cgs-earth/sal/test"
	"github.com/cgs-earth/sal/upload"

	"github.com/alexflint/go-arg"
	"github.com/lmittmann/tint"
)

// All subcommands that sal supports. These should be in a useful order as
// the order changes how the CLI presents them in the help message.
type args struct {
	Init      *initialization.InitCmd        `arg:"subcommand:init" help:"Initialize a SAL project in the current directory"`
	Import    *importation.ImportCmd         `arg:"subcommand:import" help:"Import an external ontology so builds merge it into the data product"`
	Build     *build.BuildCmd                `arg:"subcommand:build" help:"Build RDF data into a SAL data product in the iceberg table format"`
	RunTasks  *build.RunCmd                  `arg:"subcommand:run" help:"Re-run the SAL module tasks the project declares on top of the last build, for debugging a module; sal build runs them by default"`
	Construct *construct.ConstructCmd        `arg:"subcommand:construct" help:"Run the SPARQL CONSTRUCT queries in sparql/ against the last build and write what they construct to .sal/constructed, for debugging a query; sal build runs them by default"`
	Validate  *build.ValidateCmd             `arg:"subcommand:validate" help:"Validate all RDF data is properly defined and structured"`
	Query     *query.QueryCmd                `arg:"subcommand:query" help:"Open a SQL shell over a built SAL data product"`
	Get       *get.GetCmd                    `arg:"subcommand:get" help:"Look up RDF resources inside a built SAL data product"`
	Describe  *describe.DescribeCmd          `arg:"subcommand:describe" help:"Show every statement a built SAL data product makes about a subject IRI"`
	Export    *export.ExportCmd              `arg:"subcommand:export" help:"Stream the built triples table to standard out as N-Triples"`
	Serve     *serve.ServeCmd                `arg:"subcommand:serve" help:"Serve the built triples table as a read-only SPARQL endpoint on port 8080"`
	Clean     *clean.CleanCmd                `arg:"subcommand:clean" help:"Remove build artifacts produced by a SAL project"`
	Push      *push.PushCmd                  `arg:"subcommand:push" help:"Push a built SAL data product to a remote OCI registry"`
	SalModule *salmodule.SalModuleCmd        `arg:"subcommand:salmodule" help:"Output salmodule information about this project"`
	Clone     *clone.OciArtifactRetrievalCmd `arg:"subcommand:clone" help:"Clone an OCI artifact and the associated git repository for a built SAL data product"`
	Edit      *edit.EditCmd                  `arg:"subcommand:edit" help:"Edit the metadata of a built SAL data product"`
	Upload    *upload.UploadCmd              `arg:"subcommand:upload" help:"Upload a built SAL data product to an object store"`
	Test      *test.TestCmd                  `arg:"subcommand:test" help:"Run tests on a built SAL data product"`
	Pull      *clone.OciArtifactRetrievalCmd `arg:"subcommand:pull" help:"Pull a built SAL data product from a remote OCI registry"`
	// DuckDB is linked into sal, but its extensions are downloaded on first use.
	// This primes that cache and exists for the container image to call, so it is
	// kept out of the help output.
	DuckDBExtensions *query.InstallExtensionsCmd `arg:"subcommand:duckdb-extensions,hidden" help:"Download the DuckDB extensions sal queries load"`
}

func (args) Description() string {
	return "Validate and process RDF data"
}

func main() {
	if len(os.Args) == 1 {
		os.Args = append(os.Args, "--help")
	}
	var cli args
	arg.MustParse(&cli)

	logFile, logPath := openLogFile(os.Stderr, "/tmp")
	closeLog := func() {
		if logFile == nil {
			return
		}
		if err := logFile.Close(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "failed to close SAL log file: %v\n", err)
		}
		logFile = nil
	}
	defer closeLog()

	// `sal build` and `sal run` work under a progress pane and a log pane
	// when the terminal can show them, with the log going to the pane and the
	// file instead of stderr; every other command, a build asked not to with
	// --no-tui, and a build whose stderr is not a terminal log to stderr as
	// they go
	var ui *progress.UI
	if command, noTUI := progressCommand(cli); command != "" && !noTUI && progress.TerminalSupported() {
		ui = progress.New(command, logFile, logPath)
	}
	logWriter := io.Writer(os.Stderr)
	if logFile != nil {
		logWriter = io.MultiWriter(os.Stderr, logFile)
	}
	logTo := func(w io.Writer) {
		slog.SetDefault(slog.New(tint.NewHandler(w, &tint.Options{
			Level:      slog.LevelDebug,
			TimeFormat: time.Kitchen,
			AddSource:  true,
		})))
	}
	if ui != nil {
		logTo(ui.LogWriter())
	} else {
		logTo(logWriter)
	}

	// traces and metrics are exported only when the environment asks for them;
	// see pkg/telemetry. The progress pane reads the same spans through a
	// processor of its own. Whatever was recorded is flushed on every way out,
	// which os.Exit would otherwise skip.
	var processors []telemetry.SpanProcessor
	if ui != nil {
		processors = append(processors, ui.SpanProcessor())
	}
	shutdownTelemetry, err := telemetry.Setup(context.Background(), processors...)
	if err != nil {
		slog.Error(err.Error())
		closeLog()
		os.Exit(1)
	}
	exit := func(code int) {
		if err := shutdownTelemetry(context.Background()); err != nil {
			slog.Warn("failed to flush telemetry", "error", err)
		}
		closeLog()
		os.Exit(code)
	}
	// runUnderPanes runs a build or run under the panes when they are up,
	// printing the summary once they close so the terminal keeps a record,
	// and directly otherwise
	runUnderPanes := func(work func() error) error {
		if ui == nil {
			return work()
		}
		err := ui.Run(work)
		// whatever is logged from here on, such as the error that ends the
		// command, has no pane to go to any more
		logTo(logWriter)
		ui.PrintSummary(os.Stderr)
		if errors.Is(err, progress.ErrInterrupted) {
			exit(130)
		}
		return err
	}

	switch {
	case cli.Build != nil:
		err = runUnderPanes(func() error {
			_, err := cli.Build.Run()
			return err
		})
		// Errors from build should be directly written to stdout
		// not written as a log which adds extra noise
		// Special errors like UncommittedChangesErr should be handled normally
		// since they belong in the log
		if err != nil && !errors.Is(err, build.ErrUncommittedChanges) {
			fmt.Println(err.Error())
			exit(1)
		}
	case cli.RunTasks != nil:
		err = runUnderPanes(func() error {
			_, err := cli.RunTasks.Run()
			return err
		})
		// like build, validation errors are printed directly while the state
		// errors that tell the user to build again belong in the log
		if err != nil && !errors.Is(err, build.ErrRunUncommittedChanges) && !errors.Is(err, build.ErrStaleDataProduct) {
			fmt.Println(err.Error())
			exit(1)
		}
	case cli.Construct != nil:
		err = cli.Construct.Run()
	case cli.Init != nil:
		err = cli.Init.Run()
	case cli.Import != nil:
		err = cli.Import.Run()
	case cli.Query != nil:
		err = cli.Query.Run()
	case cli.Get != nil:
		err = cli.Get.Run()
	case cli.Describe != nil:
		err = cli.Describe.Run()
	case cli.Export != nil:
		err = cli.Export.Run()
	case cli.Serve != nil:
		err = cli.Serve.Run()
	case cli.Clean != nil:
		err = cli.Clean.Run()
	case cli.SalModule != nil:
		err = cli.SalModule.Run()
	case cli.Push != nil:
		err = cli.Push.Run()
	case cli.Clone != nil:
		err = cli.Clone.RunClone()
	case cli.Edit != nil:
		err = cli.Edit.Run()
	case cli.Upload != nil:
		err = cli.Upload.Run()
	case cli.Test != nil:
		err = cli.Test.Run()
	case cli.Pull != nil:
		err = cli.Pull.RunPull()
	case cli.DuckDBExtensions != nil:
		err = cli.DuckDBExtensions.Run()
	case cli.Validate != nil:
		_, err = cli.Validate.Run()
		if err != nil {
			fmt.Println(err.Error())
			exit(1)
		}
	}
	if err != nil {
		slog.Error(err.Error())
		exit(1)
	}
	exit(0)
}

// progressCommand names the subcommand that works under the progress and
// log panes, "build" or "run", and whether it was asked not to with --no-tui.
// Every other subcommand logs to stderr.
func progressCommand(cli args) (command string, noTUI bool) {
	switch {
	case cli.Build != nil:
		return "build", cli.Build.NoTUI
	case cli.RunTasks != nil:
		return "run", cli.RunTasks.NoTUI
	}
	return "", false
}

// openLogFile opens the file under tmpDir the invocation's log is appended
// to, and returns it with its path. When the directory or the file cannot be
// made, it says so on stderr and returns nil, and the log goes to stderr alone.
func openLogFile(stderr io.Writer, tmpDir string) (*os.File, string) {
	logDir := filepath.Join(tmpDir, "sal", "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		_, _ = fmt.Fprintf(stderr, "failed to create SAL log directory: %v\n", err)
		return nil, ""
	}
	path := filepath.Join(logDir, fmt.Sprintf("%s_sal.log", time.Now().Format(time.DateTime)))
	logFile, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "failed to open SAL log file: %v\n", err)
		return nil, ""
	}
	return logFile, path
}
