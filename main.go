package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/cgs-earth/sal/build"
	"github.com/cgs-earth/sal/clean"
	"github.com/cgs-earth/sal/deploy"
	"github.com/cgs-earth/sal/edit"
	"github.com/cgs-earth/sal/initialization"
	"github.com/cgs-earth/sal/load"
	"github.com/cgs-earth/sal/pull"
	"github.com/cgs-earth/sal/push"
	"github.com/cgs-earth/sal/query"
	"github.com/cgs-earth/sal/salmodule"

	"github.com/alexflint/go-arg"
	"github.com/lmittmann/tint"
)

// All subcommands that sal supports. These should be in a useful order as
// the order changes how the CLI presents them in the help message.
type args struct {
	Init      *initialization.InitCmd `arg:"subcommand:init" help:"Initialize a SAL project."`
	Load      *load.LoadCmd           `arg:"subcommand:load" help:"Load N-Quads gzip files into a local Iceberg triples table."`
	Build     *build.BuildCmd         `arg:"subcommand:build" help:"Build a vocabulary."`
	Validate  *build.ValidateCmd      `arg:"subcommand:validate" help:"Validate a vocabulary."`
	Query     *query.QueryCmd         `arg:"subcommand:query" help:"Use duckdb to query a built SAL data product."`
	Clean     *clean.CleanCmd         `arg:"subcommand:clean" help:"Clean build artifacts produced by a SAL project."`
	Push      *push.PushCmd           `arg:"subcommand:push" help:"Push a built SAL data product to an OCI registry."`
	SalModule *salmodule.SalModuleCmd `arg:"subcommand:salmodule" help:"Output salmodule information about this project."`
	Pull      *pull.PullCmd           `arg:"subcommand:pull" help:"Pull an OCI artifact of a built SAL data product."`
	Edit      *edit.EditCmd           `arg:"subcommand:edit" help:"Edit a built SAL data product."`
	Deploy    *deploy.DeployCmd       `arg:"subcommand:deploy" help:"Deploy a built SAL data product."`
}

func (args) Description() string {
	return "Validate and process RDF data"
}

func main() {

	slog.SetDefault(slog.New(
		tint.NewHandler(os.Stderr, &tint.Options{
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
		_, err = build.Run(cli.Build)
		// Errors from build should be directly written to stdout
		// not written as a log which adds extra noise
		// Special errors like UncommittedChangesErr should be handled normally
		// since they belong in the log
		if err != nil && !errors.Is(err, build.ErrUncommittedChanges) {
			fmt.Println(err.Error())
			os.Exit(1)
		}
	case cli.Load != nil:
		err = load.Run(cli.Load)
	case cli.Init != nil:
		err = initialization.Run(cli.Init)
	case cli.Query != nil:
		err = query.Run(cli.Query)
	case cli.Clean != nil:
		err = clean.Run(cli.Clean)
	case cli.SalModule != nil:
		err = salmodule.Run(cli.SalModule)
	case cli.Push != nil:
		err = push.Run(cli.Push)
	case cli.Pull != nil:
		err = pull.Run(cli.Pull)
	case cli.Edit != nil:
		err = cli.Edit.Run()
	case cli.Deploy != nil:
		err = cli.Deploy.Run()
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
