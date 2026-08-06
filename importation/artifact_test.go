package importation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestArtifactDirIsNamedAfterTheArtifact(t *testing.T) {
	directory, err := ArtifactDir("/project/.sal/data/imports", "oci://ghcr.io/cgs-earth/sal:e57e9af185a6929e9e0221df0d0987092a8fd248")

	require.NoError(t, err)
	require.Equal(t, filepath.Join("/project/.sal/data/imports", "sal"), directory)
}

func TestArtifactDirRejectsSomethingThatIsNotAnArtifact(t *testing.T) {
	_, err := ArtifactDir("/imports", "oci://")
	require.Error(t, err)
}

func TestImportIRIAcceptsAnOciReference(t *testing.T) {
	value, err := importIRI(" <oci://ghcr.io/cgs-earth/sal:e57e9af> ")

	require.NoError(t, err)
	require.Equal(t, "oci://ghcr.io/cgs-earth/sal:e57e9af", value)
}

func TestImportIRIRejectsAnOciReferenceThatDoesNotParse(t *testing.T) {
	_, err := importIRI("oci://not a registry/x")
	require.ErrorContains(t, err, "not a valid OCI artifact reference")
}

func TestImportIRIAsksForTheSchemeOnABareArtifactReference(t *testing.T) {
	_, err := importIRI("ghcr.io/cgs-earth/sal:latest")
	require.ErrorContains(t, err, "write it as oci://ghcr.io/cgs-earth/sal:latest")
}

func TestSupersedeArtifactImportReplacesAnEarlierVersionOfTheSameArtifact(t *testing.T) {
	imports := t.TempDir()
	previous := filepath.Join(imports, "sal")
	require.NoError(t, os.MkdirAll(previous, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(previous, "old.parquet"), []byte("old"), 0644))

	ontology := &Ontology{Imports: []string{
		"https://example.com/onto1",
		"oci://ghcr.io/cgs-earth/sal:old",
		"oci://ghcr.io/cgs-earth/other:v1",
	}}

	require.NoError(t, supersedeArtifactImport(ontology, "oci://ghcr.io/cgs-earth/sal:new", imports))

	// the superseded reference is dropped, unrelated imports are kept, and what
	// the old version pulled is gone so the new one is not mistaken for it
	require.Equal(t, []string{"https://example.com/onto1", "oci://ghcr.io/cgs-earth/other:v1"}, ontology.Imports)
	require.NoDirExists(t, previous)
}

func TestSupersedeArtifactImportLeavesAnAlreadyRecordedImportAlone(t *testing.T) {
	imports := t.TempDir()
	pulled := filepath.Join(imports, "sal")
	require.NoError(t, os.MkdirAll(pulled, 0755))

	ontology := &Ontology{Imports: []string{"oci://ghcr.io/cgs-earth/sal:same"}}

	require.NoError(t, supersedeArtifactImport(ontology, "oci://ghcr.io/cgs-earth/sal:same", imports))

	require.Equal(t, []string{"oci://ghcr.io/cgs-earth/sal:same"}, ontology.Imports)
	require.DirExists(t, pulled)
}

func TestPullArtifactSkipsAnArtifactThatIsAlreadyOnDisk(t *testing.T) {
	imports := t.TempDir()
	pulled := filepath.Join(imports, "sal")
	require.NoError(t, os.MkdirAll(pulled, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(pulled, "data.parquet"), []byte("pulled"), 0644))

	// no registry is reachable from a test, so returning without an error is
	// what proves the pull was skipped
	require.NoError(t, PullArtifact(t.Context(), imports, "oci://ghcr.io/cgs-earth/sal:e57e9af", ArtifactCredentials{}))
}

func TestArtifactCredentialsFromEnvReadsTheSharedVariables(t *testing.T) {
	t.Setenv("OCI_USERNAME", "someone")
	t.Setenv("OCI_PASSWORD", "a-token")

	credentials := ArtifactCredentialsFromEnv()

	require.Equal(t, "someone", credentials.GetUsername())
	require.Equal(t, "a-token", credentials.GetPassword())
}
