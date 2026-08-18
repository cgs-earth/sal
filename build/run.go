package build

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/apache/iceberg-go"
	"github.com/apache/iceberg-go/catalog"
	"github.com/apache/iceberg-go/table"
	"github.com/cgs-earth/sal/pkg"
	rdflibgo "github.com/tggo/goRDFlib"
)

type RunCmd struct {
	Paths []string `arg:"positional" help:"RDF files declaring the SAL module tasks to run"`
	Force bool     `arg:"--force" help:"run modules even if the git worktree is dirty or the data product was not built from the current commit"`
}

var ErrRunUncommittedChanges = errors.New("git repository has uncommitted changes; commit them and run `sal build` before `sal run`")

var ErrStaleDataProduct = errors.New("the built data product does not match the git commit the worktree is checked out at; run `sal build` again before `sal run`")

var ErrNoModuleTasks = errors.New("the project declares no SAL module tasks, so there is nothing for `sal run` to run")

var (
	uncommittedChangesInGit = pkg.UncommittedChangesInGit
	gitCommitHash           = pkg.GitCommitHash
	salTableHead            = func() (tableHead, error) {
		tbl, err := pkg.GetSalIcebergTable()
		if err != nil {
			return nil, err
		}
		return tbl.Metadata(), nil
	}
)

// tableHead is the slice of Iceberg table metadata `sal run` checks the
// project against before running anything.
type tableHead interface {
	CurrentSnapshot() *table.Snapshot
	SnapshotByName(name string) *table.Snapshot
	CurrentSchema() *iceberg.Schema
}

// Run runs every SAL module task the project's RDF declares and commits the
// triples the modules produced as a new snapshot of the data product. It
// refuses to run anything unless the worktree is fully committed and the
// table's latest snapshot was built from the commit HEAD is at, so that what a
// module materializes always lands on top of a build of the sources as they
// stand.
func (cfg *RunCmd) Run() (*rdflibgo.Graph, error) {
	var head tableHead
	var err error
	if cfg.Force {
		// a dirty worktree means the sources match no commit at all, so checking
		// the table against HEAD would assure nothing; force skips both checks
		slog.Warn("Running SAL modules with a modified source tree. This should only be done for testing purposes.")
		head, err = loadTableHead()
	} else {
		head, err = verifyBuildMatchesWorktree()
	}
	if err != nil {
		return nil, err
	}

	// the graph the modules' output is committed alongside is rebuilt through
	// the same pipeline `sal build` committed it with, so the only change the
	// snapshot ends up carrying is what the modules produced
	_, typed := head.CurrentSchema().FindFieldByName("object_geometry")
	buildCfg := &BuildCmd{
		Paths:        cfg.Paths,
		Format:       GraphExportFormatIceberg,
		DataTypeCols: typed,
		Force:        cfg.Force,
		runModules:   true,
	}
	return buildCfg.Run()
}

// verifyBuildMatchesWorktree checks that the worktree is clean and that the
// data product's latest snapshot is the one `sal build` tagged with the git
// commit HEAD points at, and returns the table head it verified.
func verifyBuildMatchesWorktree() (tableHead, error) {
	hasChanges, err := uncommittedChangesInGit()
	if err != nil {
		return nil, err
	}
	if hasChanges {
		return nil, ErrRunUncommittedChanges
	}

	head, err := loadTableHead()
	if err != nil {
		return nil, err
	}

	commit, err := gitCommitHash()
	if err != nil {
		return nil, err
	}
	if err := verifyTableMatchesCommit(head, commit); err != nil {
		return nil, err
	}
	return head, nil
}

// loadTableHead opens the metadata of the built data product, reporting a
// table that does not exist yet as something a build has to create first.
func loadTableHead() (tableHead, error) {
	head, err := salTableHead()
	if errors.Is(err, catalog.ErrNoSuchTable) {
		return nil, fmt.Errorf("no built data product to run SAL modules against; run `sal build` first")
	}
	return head, err
}

// verifyTableMatchesCommit checks that the table's current snapshot carries
// the tag `sal build` sets from the git HEAD hash of the build, which is what
// ties a snapshot to the sources it was built from.
func verifyTableMatchesCommit(head tableHead, commit string) error {
	current := head.CurrentSnapshot()
	if current == nil {
		return fmt.Errorf("the built data product has no snapshot to run SAL modules against; run `sal build` first")
	}
	tagged := head.SnapshotByName(commit)
	if tagged == nil || tagged.SnapshotID != current.SnapshotID {
		return ErrStaleDataProduct
	}
	return nil
}
