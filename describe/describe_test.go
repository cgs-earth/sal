package describe

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubjectIRIKeepsAPlainIRI(t *testing.T) {
	require.Equal(t, "https://geoconnex.us/ontologies/method/pastor", subjectIRI("https://geoconnex.us/ontologies/method/pastor"))
}

func TestSubjectIRIStripsTheAngleBracketsAnIRIIsWrittenIn(t *testing.T) {
	require.Equal(t, "https://geoconnex.us/ontologies/method/pastor", subjectIRI("  <https://geoconnex.us/ontologies/method/pastor>  "))
}

func TestSubjectIRIKeepsBracketsThatAreNotAPair(t *testing.T) {
	require.Equal(t, "https://example.org/a<b", subjectIRI("https://example.org/a<b"))
}

func TestSubjectIRIIsEmptyForBlankInput(t *testing.T) {
	require.Empty(t, subjectIRI("   "))
}
