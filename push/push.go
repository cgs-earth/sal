package push

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/cgs-earth/sal/importation"
	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/pkg/telemetry"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"go.opentelemetry.io/otel/attribute"
	"golang.org/x/sync/errgroup"

	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
)

const SalGitHashAnnotation = "sal.git-commit-hash"

type PushCmd struct {
	Repository string `arg:"positional" help:"Full URL of the OCI registry and repository to push the built SAL data product to. Example: ghcr.io/my-username/my-repository"`
	Username   string `arg:"--username,env:OCI_USERNAME" help:"Username for the OCI registry. This should match the username used to create the password token"`
	Password   string `arg:"--password,env:OCI_PASSWORD" help:"Password or access token for the OCI registry."`
	NoTUI      bool   `arg:"--no-tui" help:"write the log to stderr as it happens instead of opening the progress and log panes"`
	Parallel   int    `arg:"--parallel" default:"16" help:"how many files to upload to the registry at once"`
}

func (p *PushCmd) GetUsername() string {
	return p.Username
}

func (p *PushCmd) GetPassword() string {
	return p.Password
}

var _ pkg.CmdWithAuth = (*PushCmd)(nil)

// push uploads all files in dataDir as OCI layers, then packs and tags a
// manifest that references those uploaded layers. The upload is the
// `oci.push_layers` span, which counts the files and bytes sent as the
// registry reads them, so the progress pane can follow it; the manifest is
// `oci.push_manifest`.
func push(ctx context.Context, dataDir string, repo *remote.Repository, destination string, parallel int) error {
	if parallel <= 0 {
		parallel = pkg.DefaultParallelTransfers
	}
	slog.Info("Pushing SAL data product in " + dataDir + " to " + destination)

	type uploadFile struct {
		path  string
		title string
		size  int64
	}
	var files []uploadFile
	var bytes int64
	err := filepath.WalkDir(dataDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(dataDir, path)
		if err != nil {
			return fmt.Errorf("relative path for %s: %w", path, err)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}

		files = append(files, uploadFile{path: path, title: filepath.ToSlash(rel), size: info.Size()})
		bytes += info.Size()
		return nil
	})
	if err != nil {
		return err
	}

	if len(files) == 0 {
		return fmt.Errorf("no files found in SAL data directory: %s", dataDir)
	}

	SnapShots, err := pkg.GetLocalSalSnapshots()
	if err != nil {
		return fmt.Errorf("error getting snapshot data %w", err)
	}

	// the uploads are counted as the registry reads them, through the
	// client's transport, since the client buffers what it pushes before
	// sending any of it
	uploadCtx, uploadSpan := telemetry.Start(ctx, "oci.push_layers", attribute.String("sal.oci.repository", destination), attribute.Int("sal.oci.parallel", parallel))
	transfer := pkg.NewTransfer(uploadSpan, "Uploaded", len(files), bytes)
	if client, ok := repo.Client.(*auth.Client); ok {
		repo.Client = pkg.CountUploads(client, transfer.Transport)
	}
	layers := make([]ocispec.Descriptor, len(files))
	group, uploadCtx := errgroup.WithContext(uploadCtx)
	group.SetLimit(parallel)
	for i, file := range files {
		group.Go(func() error {
			b, err := os.ReadFile(file.path)
			if err != nil {
				return fmt.Errorf("read %s: %w", file.path, err)
			}

			desc, err := oras.PushBytes(uploadCtx, repo, ocispec.MediaTypeImageLayer, b)
			if err != nil {
				return fmt.Errorf("push layer %s: %w", file.title, err)
			}

			desc.Annotations = map[string]string{
				"org.opencontainers.image.title": file.title,
			}

			layers[i] = desc
			transfer.FileDone()
			return nil
		})
	}
	err = group.Wait()
	telemetry.End(uploadSpan, err)
	if err != nil {
		return err
	}

	gitRemote, err := pkg.DefaultGitRemote()
	if err != nil {
		slog.Warn("failed to get git remote URL for artifact annotation: " + err.Error())
	}

	gitHash, err := pkg.GitCommitHash()
	if err != nil {
		slog.Warn("failed to get git commit hash for artifact annotation: " + err.Error())
	}

	// the latest snapshot's build message goes in the description, the way
	// the latest commit's message is what a repository is browsed by
	description := fmt.Sprintf("SAL-produced iceberg data product for %s at commit %s", gitRemote, gitHash)
	if message := latestSnapshotMessage(); message != "" {
		description += ": " + message
	}

	annotations := map[string]string{
		"org.opencontainers.image.source":      gitRemote,
		SalGitHashAnnotation:                   gitHash,
		"org.opencontainers.image.description": description,
		pkg.SalIcebergSnapshotsAnnotation:      strings.Join(SnapShots, ","),
	}
	// the project's own description travels with the artifact, so a pull
	// into an empty directory can initialize the project with it
	if projectDescription := projectDescription(); projectDescription != "" {
		annotations[pkg.SalDescriptionAnnotation] = projectDescription
	}
	manifestCtx, manifestSpan := telemetry.Start(ctx, "oci.push_manifest", attribute.String("sal.oci.repository", destination), attribute.StringSlice("sal.oci.tags", []string{"latest", gitHash}))
	err = pushManifest(manifestCtx, repo, layers, gitHash, annotations)
	telemetry.End(manifestSpan, err)
	if err != nil {
		return err
	}

	slog.Info("Pushed data product to " + destination + ":latest totaling " + pkg.BytesToHumanReadable(bytes) + " in " + fmt.Sprint(len(files)) + " files at " + transfer.Rate())
	return nil
}

// pushManifest packs the manifest referencing the uploaded layers with its
// annotations, and tags it latest and with the commit the data product was
// built from.
func pushManifest(ctx context.Context, repo *remote.Repository, layers []ocispec.Descriptor, gitHash string, annotations map[string]string) error {
	desc, err := oras.PackManifest(ctx, repo, oras.PackManifestVersion1_1, "application/octet-stream", oras.PackManifestOptions{
		Layers:              layers,
		ManifestAnnotations: annotations,
	})
	if err != nil {
		return fmt.Errorf("pack manifest: %w", err)
	}

	// tag and push
	if err := repo.Tag(ctx, desc, "latest"); err != nil {
		return fmt.Errorf("tag artifact: %w", err)
	}
	if err := repo.Tag(ctx, desc, gitHash); err != nil {
		return fmt.Errorf("push artifact: %w", err)
	}
	return nil
}

// projectDescription is the dcterms:description of the project node in
// .sal/config.jsonld, or empty when the project has none or the file cannot
// be read, which only warns since the description only annotates the push.
func projectDescription() string {
	path, err := pkg.SalConfigPath()
	if err != nil {
		slog.Warn("failed to find the project config for the artifact description annotation: " + err.Error())
		return ""
	}
	base, err := pkg.DefaultSalBase()
	if err != nil {
		slog.Warn("failed to read the git remote for the artifact description annotation: " + err.Error())
		return ""
	}
	ontology, err := importation.ReadOntology(path, base)
	if err != nil {
		slog.Warn("failed to read the project description for the artifact description annotation: " + err.Error())
		return ""
	}
	if ontology == nil {
		return ""
	}
	return ontology.Description
}

// latestSnapshotMessage is the --message of the build that committed the
// table's current snapshot, or empty when the table cannot be read, has no
// snapshot, or the snapshot was written before builds carried a message;
// the push goes ahead either way, since the message only describes it.
func latestSnapshotMessage() string {
	tbl, err := pkg.GetSalIcebergTable()
	if err != nil {
		slog.Warn("failed to read the latest snapshot's build message for the artifact description: " + err.Error())
		return ""
	}
	snapshot := tbl.CurrentSnapshot()
	if snapshot == nil || snapshot.Summary == nil {
		return ""
	}
	return snapshot.Summary.Properties[pkg.SnapshotMessageProperty]
}

// Run executes the push command, which pushes all files in the SAL data directory
// to the specified OCI registry. The invocation is one trace, `sal.push`,
// which the progress pane follows: the check of the remote's snapshots, the
// upload, and the manifest.
func (p *PushCmd) Run() (err error) {
	if p.Password == "" {
		return fmt.Errorf("password is required for pushing to an OCI registry. See https://oras.land/docs/how_to_guides/remote_registries/#authentication for more information")
	}

	artifactRef, err := pkg.ParseArtifact(p.Repository)
	if err != nil {
		return err
	}

	dataDir, err := pkg.SalDataDir()
	if err != nil {
		return err
	}

	repo, err := remote.NewRepository(artifactRef.Repository)
	if err != nil {
		return fmt.Errorf("failed creating OCI registry client: %w", err)
	}

	repo.Client = pkg.NewOciClientWithOptionalAuth(p, artifactRef)

	ctx, span := telemetry.Start(context.Background(), "sal.push", attribute.String("sal.oci.repository", artifactRef.Repository))
	defer func() { telemetry.End(span, err) }()

	// a remote that cannot be read is pushed to all the same, since it is
	// usually a repository that does not exist yet
	_, manifestSpan := telemetry.Start(ctx, "oci.fetch_manifest", attribute.String("sal.oci.reference", artifactRef.Reference))
	diff, _ := pkg.FetchAndDiffSnapshots(repo, artifactRef.Reference)
	telemetry.End(manifestSpan, nil)
	if len(diff.SnapshotsInRemoteNotLocal) > 0 {
		proceed := pkg.Confirm(fmt.Sprintf("Remote %s contains %d snapshots not present locally. Continue pushing anyways?", p.Repository, len(diff.SnapshotsInRemoteNotLocal)))
		if !proceed {
			return fmt.Errorf("cancelled pushing to %s", p.Repository)
		}
	}

	return push(ctx, dataDir, repo, artifactRef.Repository, p.Parallel)
}
