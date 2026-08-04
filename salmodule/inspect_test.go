package salmodule

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModuleIRIKeepsAFullIRI(t *testing.T) {
	require.Equal(t, "salmodule://github.com/adplincinst/sample-salmodule-1/", moduleIRI(" salmodule://github.com/adplincinst/sample-salmodule-1/ "))
}

func TestModuleIRIAddsTheSchemeToARepositoryReference(t *testing.T) {
	require.Equal(t, "salmodule://adplincinst/sample-salmodule-1", moduleIRI("adplincinst/sample-salmodule-1"))
}

func TestModuleIRIAcceptsAGitURL(t *testing.T) {
	require.Equal(t, "salmodule://github.com/adplincinst/sample-salmodule-1", moduleIRI("https://github.com/adplincinst/sample-salmodule-1.git"))
}

func TestInspectRejectsAReferenceWithoutARepository(t *testing.T) {
	_, err := Inspect(t.Context(), "adplincinst")
	require.ErrorContains(t, err, "must be of the form")
}
