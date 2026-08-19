package describe

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func testBase() (string, error) {
	return "https://github.com/my-org/my-project/", nil
}

func noBase() (string, error) {
	return "", errors.New("git repository has no remotes configured")
}

func TestSubjectIRIKeepsAPlainIRI(t *testing.T) {
	subject, err := subjectIRI("https://geoconnex.us/ontologies/method/pastor", testBase)
	require.NoError(t, err)
	require.Equal(t, "https://geoconnex.us/ontologies/method/pastor", subject)
}

func TestSubjectIRIStripsTheAngleBracketsAnIRIIsWrittenIn(t *testing.T) {
	subject, err := subjectIRI("  <https://geoconnex.us/ontologies/method/pastor>  ", testBase)
	require.NoError(t, err)
	require.Equal(t, "https://geoconnex.us/ontologies/method/pastor", subject)
}

func TestSubjectIRIKeepsBracketsThatAreNotAPair(t *testing.T) {
	subject, err := subjectIRI("https://example.org/a<b", testBase)
	require.NoError(t, err)
	require.Equal(t, "https://example.org/a<b", subject)
}

func TestSubjectIRIIsAnErrorForBlankInput(t *testing.T) {
	_, err := subjectIRI("   ", testBase)
	require.ErrorContains(t, err, "a subject IRI is required")
}

func TestSubjectIRIResolvesARelativeTermAgainstTheProjectBase(t *testing.T) {
	subject, err := subjectIRI("MyTerm", testBase)
	require.NoError(t, err)
	require.Equal(t, "https://github.com/my-org/my-project/MyTerm", subject)
}

func TestSubjectIRIResolvesARelativeTermWrittenInAngleBrackets(t *testing.T) {
	subject, err := subjectIRI("<MyTerm>", testBase)
	require.NoError(t, err)
	require.Equal(t, "https://github.com/my-org/my-project/MyTerm", subject)
}

func TestSubjectIRIDoesNotDoubleTheSlashOfARootRelativeTerm(t *testing.T) {
	subject, err := subjectIRI("/MyTerm", testBase)
	require.NoError(t, err)
	require.Equal(t, "https://github.com/my-org/my-project/MyTerm", subject)
}

func TestSubjectIRIKeepsAPrefixedNameAsWritten(t *testing.T) {
	subject, err := subjectIRI("schema:Bob", testBase)
	require.NoError(t, err)
	require.Equal(t, "schema:Bob", subject)
}

func TestSubjectIRIKeepsABlankNodeLabelAsStored(t *testing.T) {
	subject, err := subjectIRI("_:sal_0123456789abcdef01234567", testBase)
	require.NoError(t, err)
	require.Equal(t, "_:sal_0123456789abcdef01234567", subject)
}

func TestSubjectIRIKeepsASalModuleIRI(t *testing.T) {
	subject, err := subjectIRI("salmodule://geoconnex/Pid", testBase)
	require.NoError(t, err)
	require.Equal(t, "salmodule://geoconnex/Pid", subject)
}

func TestSubjectIRIReportsWhyARelativeTermCouldNotBeResolved(t *testing.T) {
	_, err := subjectIRI("MyTerm", noBase)
	require.ErrorContains(t, err, "relative to the project base")
	require.ErrorContains(t, err, "no remotes configured")
}
