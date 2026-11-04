package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/cgs-earth/sal/build"
	"github.com/cgs-earth/sal/clone"
	"github.com/cgs-earth/sal/pkg/progress"
	"github.com/cgs-earth/sal/push"
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

func TestOnlyCommandsWithAPlanWorkUnderTheProgressPanes(t *testing.T) {
	command, noTUI := progressCommand(args{Build: &build.BuildCmd{Message: "Add gauges"}})
	require.Equal(t, "build", command)
	require.False(t, noTUI)

	command, noTUI = progressCommand(args{RunTasks: &build.RunCmd{Message: "Rerun", NoTUI: true}})
	require.Equal(t, "run", command)
	require.True(t, noTUI)

	// a build or run without a message takes the commit's, so it opens them too
	_, noTUI = progressCommand(args{Build: &build.BuildCmd{}})
	require.False(t, noTUI)

	command, noTUI = progressCommand(args{Push: &push.PushCmd{NoTUI: true}})
	require.Equal(t, "push", command)
	require.True(t, noTUI)

	command, _ = progressCommand(args{Pull: &clone.OciArtifactRetrievalCmd{}})
	require.Equal(t, "pull", command)

	command, _ = progressCommand(args{Clone: &clone.OciArtifactRetrievalCmd{}})
	require.Equal(t, "clone", command)

	// every command the panes open for has a plan to draw
	for _, command := range []string{"build", "run", "push", "pull", "clone"} {
		require.NotEmpty(t, progress.Plan(command), command)
	}

	command, _ = progressCommand(args{Validate: &build.ValidateCmd{}})
	require.Empty(t, command)
}
