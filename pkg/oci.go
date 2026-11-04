package pkg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/apache/iceberg-go/catalog"
	"github.com/cgs-earth/sal/pkg/telemetry"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"go.opentelemetry.io/otel/attribute"
	"golang.org/x/sync/errgroup"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/file"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

const DefaultAssumedRegistry = "ghcr.io"

// pullAttempts is how many times a layer is fetched before the pull gives
// up on it. A registry, or the CDN it redirects blobs to, drops a stream
// now and then on a long pull (an HTTP/2 PROTOCOL_ERROR partway through a
// body, a reset connection, a timeout), which the registry client's own
// retry does not cover once the response has started; the layer is fetched
// again from the start, after pullRetryBackoff doubled per attempt.
const pullAttempts = 4

// pullRetryBackoff is the wait before the second attempt at a layer, doubled
// for each attempt after it; tests shorten it.
var pullRetryBackoff = time.Second

// DefaultParallelTransfers is how many layers a push or pull moves at once
// unless --parallel says otherwise. Most layers of a data product are small
// Parquet and Avro files, so the request latency to the registry counts for
// more than the bandwidth, and more in flight is faster.
const DefaultParallelTransfers = 16

type CmdWithAuth interface {
	GetUsername() string
	GetPassword() string
}

type OciArtifactCmdWithAuth interface {
	CmdWithAuth
	GetArtifactReference() (ArtifactReference, error)
}

func FetchManifest(ctx context.Context, src oras.ReadOnlyTarget, reference string) (ocispec.Descriptor, ocispec.Manifest, error) {
	desc, manifestBytes, err := oras.FetchBytes(ctx, src, reference, oras.DefaultFetchBytesOptions)
	if err != nil {
		return ocispec.Descriptor{}, ocispec.Manifest{}, fmt.Errorf("fetch artifact manifest %s: %w", reference, err)
	}

	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return ocispec.Descriptor{}, ocispec.Manifest{}, fmt.Errorf("decode artifact manifest %s: %w", reference, err)
	}

	return desc, manifest, nil
}

func RepoDirFromSource(source string) string {
	source = strings.TrimSuffix(source, "/")
	source = strings.TrimSuffix(source, ".git")
	if i := strings.LastIndex(source, "/"); i != -1 {
		return source[i+1:]
	}
	if i := strings.LastIndex(source, ":"); i != -1 {
		return source[i+1:]
	}
	return source
}

type ArtifactReference struct {
	// ghcr.io/cgs-earth/sal
	Repository string
	// example latest
	Reference string
	// example ghcr.io
	RegistryName string
	// example cgs-earth
	Owner string
	// example sal
	ArtifactName string
}

func GuessDefaultArtifact() (string, error) {
	gitProjectName, err := GitProjectName()
	if err != nil {
		return "", err
	}
	gitProjectOwner, err := GitProjectOwner()
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%s/%s/%s:latest", DefaultAssumedRegistry, gitProjectOwner, gitProjectName), nil
}

func ParseArtifact(artifact string) (ArtifactReference, error) {

	if artifact == "" {
		var err error
		artifact, err = GuessDefaultArtifact()
		if err != nil {
			return ArtifactReference{}, err
		}
	}

	artifact = strings.TrimPrefix(artifact, "https://")
	artifact = strings.TrimPrefix(artifact, "http://")

	ref, err := registry.ParseReference(artifact)
	if err != nil {
		return ArtifactReference{}, fmt.Errorf("invalid OCI artifact reference: %w", err)
	}

	owner, _, _ := strings.Cut(ref.Repository, "/")
	artifactName := RepoDirFromSource(ref.Repository)
	return ArtifactReference{
		Repository:   ref.Registry + "/" + ref.Repository,
		Reference:    ref.ReferenceOrDefault(),
		RegistryName: ref.Registry,
		Owner:        owner,
		ArtifactName: artifactName,
	}, nil
}

func NewOciClientWithOptionalAuth(cmd CmdWithAuth, ref ArtifactReference) *auth.Client {
	username := cmd.GetUsername()
	password := cmd.GetPassword()
	if password != "" || username != "" {
		if username == "" && password != "" {
			username = ref.Owner
		}

		credential := auth.Credential{
			Username: username,
			Password: password,
		}
		return &auth.Client{
			Client:     retry.DefaultClient,
			Cache:      auth.NewCache(),
			Credential: auth.StaticCredential(ref.RegistryName, credential),
		}
	}
	return auth.DefaultClient
}

// CountUploads makes client send every request through transport, which
// counts the bytes of their bodies, so a push can be followed as it uploads.
// The client is copied rather than changed, since auth.DefaultClient is
// shared by every command.
func CountUploads(client *auth.Client, transport func(http.RoundTripper) http.RoundTripper) *auth.Client {
	counted := *client
	httpClient := *retry.DefaultClient
	if client.Client != nil {
		httpClient = *client.Client
	}
	next := httpClient.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	httpClient.Transport = transport(next)
	counted.Client = &httpClient
	return &counted
}

func FetchAndDiffSnapshots(repo *remote.Repository, reference string) (SnapshotDiffReport, error) {
	return fetchAndDiffSnapshots(repo, reference, GetLocalSalSnapshots)
}

func fetchAndDiffSnapshots(src oras.ReadOnlyTarget, reference string, getLocalSnapshots func() ([]string, error)) (SnapshotDiffReport, error) {
	ctx := context.Background()
	_, manifest, err := FetchManifest(ctx, src, reference)
	if err != nil {
		return SnapshotDiffReport{}, err
	}

	remoteSnapshots, err := GetSnapshotsFromManifest(manifest)
	if err != nil {
		return SnapshotDiffReport{}, err
	}

	localSnapshots, err := getLocalSnapshots()
	// if the error is that the table just doesn't exist yet, that is
	// ok since it will be created upon pull
	if err != nil && !errors.Is(err, catalog.ErrNoSuchTable) {
		return SnapshotDiffReport{}, err
	}

	return SnapshotDiff(localSnapshots, remoteSnapshots)
}

// pullLayer fetches one layer into the file store under its title, trying
// again after a failure up to pullAttempts times. What a failed attempt
// read is taken back out of the transfer's count, so the bytes are counted
// once. A layer the store already holds under its name, which is one whose
// earlier attempt wrote it and failed after, counts as pulled.
func pullLayer(ctx context.Context, src oras.ReadOnlyTarget, fs *file.Store, layer ocispec.Descriptor, transfer *Transfer) error {
	title := layer.Annotations[ocispec.AnnotationTitle]
	var err error
	for attempt := 1; attempt <= pullAttempts; attempt++ {
		if attempt > 1 {
			wait := pullRetryBackoff << (attempt - 2)
			slog.Warn(fmt.Sprintf("Attempt %d of %d at layer %s failed; trying again in %s", attempt-1, pullAttempts, title, wait), "error", err)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return err
			}
		}
		err = fetchLayer(ctx, src, fs, layer, transfer)
		if err == nil || errors.Is(err, file.ErrDuplicateName) {
			return nil
		}
		if ctx.Err() != nil {
			return err
		}
	}
	return fmt.Errorf("%w (after %d attempts)", err, pullAttempts)
}

// fetchLayer is one attempt at pullLayer.
func fetchLayer(ctx context.Context, src oras.ReadOnlyTarget, fs *file.Store, layer ocispec.Descriptor, transfer *Transfer) error {
	title := layer.Annotations[ocispec.AnnotationTitle]
	rc, err := src.Fetch(ctx, layer)
	if err != nil {
		return fmt.Errorf("fetch layer %s: %w", title, err)
	}
	reader := &countingReader{Reader: rc, transfer: transfer}
	err = fs.Push(ctx, layer, reader)
	closeErr := rc.Close()
	if err != nil {
		transfer.Discard(reader.read)
		return fmt.Errorf("write layer %s: %w", title, err)
	}
	if closeErr != nil {
		return fmt.Errorf("close layer %s: %w", title, closeErr)
	}
	return nil
}

// PullManifestLayers copies an OCI artifact into destination, restoring layers
// according to their exact org.opencontainers.image.title annotations. It backs
// `sal clone`, `sal pull`, and the OCI artifacts `sal import` records. The
// copy is the `oci.pull_layers` span, which counts the files and bytes pulled
// as they arrive, so the progress pane can follow it. parallel is how many
// layers are fetched at once; DefaultParallelTransfers when it is not positive.
func PullManifestLayers(ctx context.Context, src oras.ReadOnlyTarget, manifest ocispec.Manifest, desc ocispec.Descriptor, reference string, destination string, parallel int) (err error) {
	if parallel <= 0 {
		parallel = DefaultParallelTransfers
	}
	var titled []ocispec.Descriptor
	var bytes int64
	for _, layer := range manifest.Layers {
		if layer.Annotations[ocispec.AnnotationTitle] != "" {
			titled = append(titled, layer)
			bytes += layer.Size
		}
	}
	ctx, span := telemetry.Start(ctx, "oci.pull_layers", attribute.String("sal.oci.reference", reference), attribute.String("sal.oci.destination", destination), attribute.Int("sal.oci.parallel", parallel))
	defer func() { telemetry.End(span, err) }()
	transfer := NewTransfer(span, "Pulled", len(titled), bytes)

	if err := os.MkdirAll(destination, 0755); err != nil {
		return fmt.Errorf("create pull destination %s: %w", destination, err)
	}

	fs, err := file.New(destination)
	if err != nil {
		return fmt.Errorf("create destination file store: %w", err)
	}
	defer func() {
		if err := fs.Close(); err != nil {
			slog.Warn("failed to clean up pull file store: " + err.Error())
		}
	}()

	group, pullCtx := errgroup.WithContext(ctx)
	group.SetLimit(parallel)
	for _, layer := range titled {
		group.Go(func() error {
			if err := pullLayer(pullCtx, src, fs, layer, transfer); err != nil {
				return err
			}
			transfer.FileDone()
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}

	if transfer.FilesDone() == 0 {
		return fmt.Errorf("artifact %s has no layers with %s annotations", reference, ocispec.AnnotationTitle)
	}

	slog.Info("Pulled "+reference+" to "+destination, "digest", desc.Digest.String(), "files", transfer.FilesDone(), "bytes", BytesToHumanReadable(transfer.BytesDone()), "rate", transfer.Rate())
	return nil
}
