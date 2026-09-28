package construct

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cgs-earth/sal/export"
	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/pkg/telemetry"
	salsparql "github.com/cgs-earth/sal/query/sparql"
	rdflibgo "github.com/tggo/goRDFlib"
	"github.com/tggo/goRDFlib/turtle"
	"go.opentelemetry.io/otel/attribute"
)

// QueryDir is the directory of a SAL project, created by `sal init`, that
// holds its SPARQL queries.
const QueryDir = "sparql"

// ConstructCmd runs every SPARQL CONSTRUCT query in the project's sparql/
// directory against the built triples table and writes the graph each one
// builds to .sal/constructed. It only reads the table; `sal build` is what
// commits the constructed triples, in the same snapshot as everything else.
type ConstructCmd struct{}

// Query is a SPARQL CONSTRUCT query found in the project's sparql/ directory.
type Query struct {
	// Path is the file the query was read from.
	Path string
	// Name is the file's path under sparql/ without its extension, which is
	// what the file of constructed triples is named after.
	Name string
	Text string
	// Prefixes are the prefixes the query declares, which the constructed
	// triples are written with.
	Prefixes map[string]string
}

// Runner runs a CONSTRUCT query and streams the triples it builds, one row
// each in the columns salsparql.ConstructColumns names.
type Runner interface {
	Construct(ctx context.Context, query string, rowFn func([]sql.NullString) error) error
}

func (cmd *ConstructCmd) Run() (err error) {
	ctx, span := telemetry.Start(context.Background(), "sal.construct")
	defer func() { telemetry.End(span, err) }()

	projectDir, err := pkg.SALProjectDir(os.UserHomeDir)
	if err != nil {
		return err
	}
	queries, err := FindQueries(projectDir)
	if err != nil {
		return err
	}
	if len(queries) == 0 {
		slog.Warn("No SPARQL CONSTRUCT queries found in " + filepath.Join(projectDir, QueryDir))
		_, err = Run(ctx, nil, projectDir, nil)
		return err
	}

	table, err := salsparql.LocateTriplesTable()
	if err != nil {
		return err
	}
	runner, err := table.Runner(ctx, 0)
	if err != nil {
		return err
	}
	slog.Warn("sal construct is meant for debugging CONSTRUCT queries: it reads the last build, which already holds what they constructed, and changes nothing in the table. sal build is what commits constructed triples.")
	_, err = Run(ctx, runner, projectDir, queries)
	return err
}

// FindQueries returns every SPARQL CONSTRUCT query under the project's sparql/
// directory, sorted by path. A file is a SPARQL query when it ends in .rq or
// .sparql; one that holds any other kind of query is skipped, and one that
// does not parse, or that sal cannot translate, is an error naming the file,
// so that a build fails on it before anything is run. A project without the
// directory has no queries.
func FindQueries(projectDir string) ([]Query, error) {
	root := filepath.Join(projectDir, QueryDir)
	var queries []Query
	names := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		extension := strings.ToLower(filepath.Ext(path))
		if entry.IsDir() || (extension != ".rq" && extension != ".sparql") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		prefixes, isConstruct, err := salsparql.ConstructPrefixes(string(content))
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if !isConstruct {
			return nil
		}
		if _, err := salsparql.ToSQL(string(content)); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(relative, filepath.Ext(relative))
		if other, ok := names[name]; ok {
			return fmt.Errorf("%s and %s would both write their constructed triples to %s.ttl; rename one of them", other, path, name)
		}
		names[name] = path
		queries = append(queries, Query{Path: path, Name: name, Text: string(content), Prefixes: prefixes})
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("construct: %w", err)
	}
	sort.Slice(queries, func(i, j int) bool { return queries[i].Path < queries[j].Path })
	return queries, nil
}

// Run wipes .sal/constructed, runs every query, writes the graph each one
// builds to .sal/constructed/<name>.ttl, and returns the graphs merged into
// one. The output is Turtle rather than JSON-LD: it groups a subject's
// statements and shortens IRIs with no @context to resolve, so it is both the
// smaller file and the cheaper one to write and read back. The directory is
// wiped even when there are no queries, so it never holds the output of a
// query the project no longer has.
func Run(ctx context.Context, runner Runner, projectDir string, queries []Query) (*rdflibgo.Graph, error) {
	outDir := filepath.Join(projectDir, ".sal", "constructed")
	if err := os.RemoveAll(outDir); err != nil {
		return nil, fmt.Errorf("construct: wipe %s: %w", outDir, err)
	}
	merged := rdflibgo.NewGraph()
	if len(queries) == 0 {
		return merged, nil
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return nil, err
	}
	if err := ignoreInGit(projectDir, outDir); err != nil {
		return nil, err
	}

	for _, query := range queries {
		graph, err := constructGraph(ctx, runner, query)
		if err != nil {
			return nil, err
		}
		outPath := filepath.Join(outDir, query.Name+".ttl")
		if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
			return nil, err
		}
		file, err := os.Create(outPath)
		if err != nil {
			return nil, err
		}
		err = turtle.Serialize(graph, file)
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return nil, fmt.Errorf("construct: write %s: %w", outPath, err)
		}
		slog.Info(fmt.Sprintf("Constructed %d triples from %s into %s", graph.Len(), query.Path, outPath))

		graph.Triples(nil, nil, nil)(func(triple rdflibgo.Triple) bool {
			merged.Add(triple.Subject, triple.Predicate, triple.Object)
			return true
		})
	}
	return merged, nil
}

// constructGraph runs one query and reads the rows it builds back into RDF
// terms the way `sal export` reads the rows of the table.
func constructGraph(ctx context.Context, runner Runner, query Query) (_ *rdflibgo.Graph, err error) {
	ctx, span := telemetry.Start(ctx, "construct.query", attribute.String("sal.construct.query", query.Path))
	defer func() { telemetry.End(span, err) }()
	graph := rdflibgo.NewGraph()
	for prefix, namespace := range query.Prefixes {
		graph.Bind(prefix, rdflibgo.NewURIRefUnsafe(namespace))
	}
	err = runner.Construct(ctx, query.Text, func(row []sql.NullString) error {
		graph.Add(export.SubjectTerm(row[0].String), rdflibgo.NewURIRefUnsafe(row[1].String), export.ObjectTerm(row[2:]))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", query.Path, err)
	}
	span.SetAttributes(attribute.Int("sal.triples.constructed", graph.Len()))
	return graph, nil
}

// ignoreInGit makes sure git ignores the directory of constructed triples.
// When the project's .gitignore does not already cover it, the directory gets
// a .gitignore of its own ignoring everything in it, rather than an entry in
// the project's: that file is tracked, and changing it during a build would
// leave the worktree dirty for the next one.
func ignoreInGit(projectDir string, outDir string) error {
	check := exec.Command("git", "check-ignore", "-q", outDir)
	check.Dir = projectDir
	if check.Run() == nil {
		return nil
	}
	return os.WriteFile(filepath.Join(outDir, ".gitignore"), []byte("*\n"), 0644)
}
