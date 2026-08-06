package importation

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/cgs-earth/sal/pkg"
	"oras.land/oras-go/v2/registry/remote"
)

// OciScheme marks an import that is an OCI artifact rather than an ontology
// document. The rest of the reference is an ordinary docker style image
// specification, as in oci://ghcr.io/cgs-earth/sal:e57e9af.
const OciScheme = "oci://"

func IsOciImport(iri string) bool {
	return strings.HasPrefix(iri, OciScheme)
}

// ArtifactCredentials are the registry credentials an OCI import is pulled
// with. Every command that reaches a registry takes the same pair, so they
// satisfy pkg.CmdWithAuth and reach the registry through the shared client.
type ArtifactCredentials struct {
	Username string
	Password string
}

func (credentials ArtifactCredentials) GetUsername() string { return credentials.Username }
func (credentials ArtifactCredentials) GetPassword() string { return credentials.Password }

// ArtifactCredentialsFromEnv reads the credentials from the same environment
// variables the pull, push, and clone flags fall back to. It is how `sal build`
// authenticates, since build has no registry flags of its own.
func ArtifactCredentialsFromEnv() ArtifactCredentials {
	return ArtifactCredentials{
		Username: os.Getenv("OCI_USERNAME"),
		Password: os.Getenv("OCI_PASSWORD"),
	}
}

// ArtifactDir returns where an imported OCI artifact is pulled to, which is a
// directory named after the artifact itself: oci://ghcr.io/cgs-earth/sal:e57e9af
// is pulled to <imports>/sal. A project therefore holds one version of an
// artifact at a time; see supersedeArtifactImport.
func ArtifactDir(importsDir string, iri string) (string, error) {
	name, err := artifactName(iri)
	if err != nil {
		return "", err
	}
	return filepath.Join(importsDir, name), nil
}

func artifactName(iri string) (string, error) {
	reference := strings.TrimSpace(strings.TrimPrefix(iri, OciScheme))
	// an empty reference makes ParseArtifact fall back to the project's own
	// default artifact, which is never what an import asked for
	if reference == "" {
		return "", fmt.Errorf("%s names no OCI artifact", iri)
	}
	ref, err := pkg.ParseArtifact(reference)
	if err != nil {
		return "", fmt.Errorf("%s is not a valid OCI artifact reference: %w", iri, err)
	}
	return ref.ArtifactName, nil
}

// supersedeArtifactImport drops any recorded import of the same artifact at a
// different version and removes what it pulled. Artifacts are laid out by name,
// so a project holds one version of an artifact at a time and importing a new
// version replaces the old rather than sitting beside it.
func supersedeArtifactImport(ontology *Ontology, iri string, importsDir string) error {
	name, err := artifactName(iri)
	if err != nil {
		return err
	}

	kept := make([]string, 0, len(ontology.Imports))
	for _, existing := range ontology.Imports {
		if existing == iri || !IsOciImport(existing) {
			kept = append(kept, existing)
			continue
		}
		existingName, err := artifactName(existing)
		if err != nil {
			return err
		}
		if existingName != name {
			kept = append(kept, existing)
			continue
		}

		directory, err := ArtifactDir(importsDir, existing)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(directory); err != nil {
			return err
		}
		slog.Warn("Replacing " + existing + " with " + iri)
	}
	ontology.Imports = kept
	return nil
}

// PullArtifact pulls an imported OCI artifact into the project's imports
// directory. An artifact that is already on disk is left alone, so an import is
// only fetched the first time it is seen.
func PullArtifact(ctx context.Context, importsDir string, iri string, credentials pkg.CmdWithAuth) error {
	destination, err := ArtifactDir(importsDir, iri)
	if err != nil {
		return err
	}
	if pulled, err := artifactIsPulled(destination); err != nil {
		return err
	} else if pulled {
		slog.Debug("Skipping " + iri + "; already pulled to " + destination)
		return nil
	}

	reference := strings.TrimPrefix(iri, OciScheme)
	ref, err := pkg.ParseArtifact(reference)
	if err != nil {
		return err
	}

	repo, err := remote.NewRepository(ref.Repository)
	if err != nil {
		return fmt.Errorf("failed creating OCI registry client: %w", err)
	}
	repo.Client = pkg.NewOciClientWithOptionalAuth(credentials, ref)

	desc, manifest, err := pkg.FetchManifest(ctx, repo, ref.Reference)
	if err != nil {
		return err
	}

	slog.Info("Pulling imported artifact " + reference)
	if err := pkg.PullManifestLayers(ctx, repo, manifest, desc, ref.Reference, destination); err != nil {
		// a partial pull would otherwise look like an artifact that is already
		// on disk and never be retried
		_ = os.RemoveAll(destination)
		return err
	}
	return nil
}

// artifactIsPulled reports whether an artifact directory holds a previous pull.
// An empty directory does not count, since a failed pull can leave one behind.
func artifactIsPulled(destination string) (bool, error) {
	entries, err := os.ReadDir(destination)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return len(entries) > 0, nil
}
