package get

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func captureStdout(t *testing.T, run func()) string {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = writer
	run()
	os.Stdout = original
	require.NoError(t, writer.Close())
	out, err := io.ReadAll(reader)
	require.NoError(t, err)
	return string(out)
}

func TestSubjectPrefixIsEmptyWhenAllIsRequested(t *testing.T) {
	prefix, err := subjectPrefix(true)
	require.NoError(t, err)
	require.Empty(t, prefix)
}

func TestNoneFoundPointsAtAllWhenTheListingWasRestricted(t *testing.T) {
	out := captureStdout(t, func() {
		noneFound("instances", "the data product has no rdf:type statements", "https://github.com/cgs-earth/sal/")
	})
	require.Equal(t, "no instances found under the project base https://github.com/cgs-earth/sal/; run with --all to list every namespace\n", out)
}

func TestNoneFoundDoesNotPointAtAllWhenEveryNamespaceWasListed(t *testing.T) {
	out := captureStdout(t, func() { noneFound("instances", "the data product has no rdf:type statements", "") })
	require.Equal(t, "no instances found; the data product has no rdf:type statements\n", out)
}
