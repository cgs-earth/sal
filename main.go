package main

import (
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
	"github.com/cgs-earth/sal/edit"
	"github.com/cgs-earth/sal/initialization"
	"github.com/cgs-earth/sal/push"
	"github.com/cgs-earth/sal/query"
	"github.com/cgs-earth/sal/salmodule"
	"github.com/cgs-earth/sal/test"
	"github.com/cgs-earth/sal/upload"

	"github.com/alexflint/go-arg"
	"github.com/lmittmann/tint"
)

// All subcommands that sal supports. These should be in a useful order as
// the order changes how the CLI presents them in the help message.
type args struct {
	Init      *initialization.InitCmd        `arg:"subcommand:init" help:"Initialize a SAL project in the current directory"`
	Build     *build.BuildCmd                `arg:"subcommand:build" help:"Build RDF data into a SAL data product in the iceberg table format"`
	Validate  *build.ValidateCmd             `arg:"subcommand:validate" help:"Validate all RDF data is properly defined and structured"`
	Query     *query.QueryCmd                `arg:"subcommand:query" help:"Use duckdb to query a built SAL data product"`
	Clean     *clean.CleanCmd                `arg:"subcommand:clean" help:"Remove build artifacts produced by a SAL project"`
	Push      *push.PushCmd                  `arg:"subcommand:push" help:"Push a built SAL data product to a remote OCI registry"`
	SalModule *salmodule.SalModuleCmd        `arg:"subcommand:salmodule" help:"Output salmodule information about this project"`
	Clone     *clone.OciArtifactRetrievalCmd `arg:"subcommand:clone" help:"Clone an OCI artifact and the associated git repository for a built SAL data product"`
	Edit      *edit.EditCmd                  `arg:"subcommand:edit" help:"Edit the metadata of a built SAL data product"`
	Upload    *upload.UploadCmd              `arg:"subcommand:upload" help:"Upload a built SAL data product to an object store"`
	Test      *test.TestCmd                  `arg:"subcommand:test" help:"Run tests on a built SAL data product"`
	Pull      *clone.OciArtifactRetrievalCmd `arg:"subcommand:pull" help:"Pull a built SAL data product from a remote OCI registry"`
}

func (args) Description() string {
	return "Validate and process RDF data"
}

func main() {

	logWriter, closeLog := newLogWriter(os.Stderr, "/tmp")
	defer closeLog()

	slog.SetDefault(slog.New(
		tint.NewHandler(logWriter, &tint.Options{
			Level:      slog.LevelDebug,
			TimeFormat: time.Kitchen,
			AddSource:  true,
		}),
	))

	if len(os.Args) == 1 {
		os.Args = append(os.Args, "--help")
	}

	var cli args
	arg.MustParse(&cli)
	var err error
	switch {
	case cli.Build != nil:
		_, err = cli.Build.Run()
		// Errors from build should be directly written to stdout
		// not written as a log which adds extra noise
		// Special errors like UncommittedChangesErr should be handled normally
		// since they belong in the log
		if err != nil && !errors.Is(err, build.ErrUncommittedChanges) {
			fmt.Println(err.Error())
			os.Exit(1)
		}
	case cli.Init != nil:
		err = cli.Init.Run()
	case cli.Query != nil:
		err = cli.Query.Run()
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
	case cli.Validate != nil:
		_, err = cli.Validate.Run()
		if err != nil {
			fmt.Println(err.Error())
			os.Exit(1)
		}
	}
	if err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}

// newLogWriter writes logs to stderr and appends the same output to a temp log file.
func newLogWriter(stderr io.Writer, tmpDir string) (io.Writer, func()) {
	logDir := filepath.Join(tmpDir, "sal", "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		_, _ = fmt.Fprintf(stderr, "failed to create SAL log directory: %v\n", err)
		return stderr, func() {}
	}
	current_time := time.Now().Format(time.DateTime)
	logFile, err := os.OpenFile(filepath.Join(logDir, fmt.Sprintf("%s_sal.log", current_time)), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "failed to open SAL log file: %v\n", err)
		return stderr, func() {}
	}

	return io.MultiWriter(stderr, logFile), func() {
		if err := logFile.Close(); err != nil {
			_, _ = fmt.Fprintf(stderr, "failed to close SAL log file: %v\n", err)
		}
	}
}
