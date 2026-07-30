package salmodule

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseModuleIRIWithExplicitHost(t *testing.T) {
	ref, err := ParseModuleIRI("salmodule://www.github.com/test/history-getter/")

	require.NoError(t, err)
	require.Equal(t, "salmodule://www.github.com/test/history-getter/", ref.Namespace)
	require.Equal(t, "https://www.github.com/test/history-getter.git", ref.CloneURL)
	require.Equal(t, "sal-module-www-github-com-test-history-getter:latest", ref.ImageTag)
}

func TestParseModuleIRIDefaultsToGithub(t *testing.T) {
	ref, err := ParseModuleIRI("salmodule://test/history-getter/")

	require.NoError(t, err)
	require.Equal(t, "salmodule://github.com/test/history-getter/", ref.Namespace)
	require.Equal(t, "https://github.com/test/history-getter.git", ref.CloneURL)
}

func TestParseModuleIRIResolvesTermToItsModule(t *testing.T) {
	ref, err := ParseModuleIRI("salmodule://www.github.com/test/history-getter/EducationalHistoryFinder")

	require.NoError(t, err)
	require.Equal(t, "salmodule://www.github.com/test/history-getter/", ref.Namespace)
	require.Equal(t, "https://www.github.com/test/history-getter.git", ref.CloneURL)
}

func TestParseModuleIRIIgnoresFragment(t *testing.T) {
	ref, err := ParseModuleIRI("salmodule://www.github.com/test/history-getter#EducationalHistoryFinder")

	require.NoError(t, err)
	require.Equal(t, "salmodule://www.github.com/test/history-getter/", ref.Namespace)
}

func TestParseModuleIRIRejectsOtherSchemes(t *testing.T) {
	_, err := ParseModuleIRI("https://schema.org/")

	require.Error(t, err)
	require.Contains(t, err.Error(), "not a salmodule:// IRI")
}

func TestParseModuleIRIRejectsMissingRepository(t *testing.T) {
	_, err := ParseModuleIRI("salmodule://history-getter/")

	require.Error(t, err)
	require.Contains(t, err.Error(), "OWNER/REPO")
}

func TestIsTaskBaseClassMatchesOntologyClasses(t *testing.T) {
	require.True(t, IsTaskBaseClass(Namespace+"NodeProcessor"))
	require.True(t, IsTaskBaseClass(Namespace+"Task"))
	require.False(t, IsTaskBaseClass(Namespace+"Error"))
}
