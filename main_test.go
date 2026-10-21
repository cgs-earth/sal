package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/cgs-earth/sal/build"
	"github.com/stretchr/testify/require"
)

func TestOpenLogFileWritesUnderTheTempDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	var stderr bytes.Buffer

	logFile, path := openLogFile(&stderr, tmpDir)
	require.NotNil(t, logFile)
	_, err := logFile.WriteString("sal log entry\n")
	require.NoError(t, err)
	require.NoError(t, logFile.Close())

	require.Empty(t, stderr.String())
	require.Equal(t, filepath.Join(tmpDir, "sal", "logs"), filepath.Dir(path))
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "sal log entry\n", string(content))
}

func TestOpenLogFileReportsATempDirectoryThatCannotBeCreated(t *testing.T) {
	tmpDir := t.TempDir()
	blockingFile := filepath.Join(tmpDir, "not-a-directory")
	require.NoError(t, os.WriteFile(blockingFile, []byte(""), 0644))

	var stderr bytes.Buffer
	logFile, path := openLogFile(&stderr, blockingFile)

	require.Nil(t, logFile)
	require.Empty(t, path)
	require.Contains(t, stderr.String(), "failed to create SAL log directory")
}

func TestOnlyBuildAndRunWorkUnderTheProgressPanes(t *testing.T) {
	command, noTUI := progressCommand(args{Build: &build.BuildCmd{}})
	require.Equal(t, "build", command)
	require.False(t, noTUI)

	command, noTUI = progressCommand(args{RunTasks: &build.RunCmd{NoTUI: true}})
	require.Equal(t, "run", command)
	require.True(t, noTUI)

	command, _ = progressCommand(args{Validate: &build.ValidateCmd{}})
	require.Empty(t, command)
}
