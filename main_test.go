package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewLogWriterWritesToStderrAndTempLogFile(t *testing.T) {
	tmpDir := t.TempDir()
	var stderr bytes.Buffer

	writer, closeLog := newLogWriter(&stderr, tmpDir)
	_, err := writer.Write([]byte("sal log entry\n"))
	require.NoError(t, err)
	closeLog()

	require.Equal(t, "sal log entry\n", stderr.String())

	// assert that the directory isn't empty
	entries, err := os.ReadDir(tmpDir)
	require.NoError(t, err)
	require.NotEmpty(t, entries)
}

func TestNewLogWriterFallsBackToStderrWhenTempLogDirectoryCannotBeCreated(t *testing.T) {
	tmpDir := t.TempDir()
	blockingFile := filepath.Join(tmpDir, "not-a-directory")
	require.NoError(t, os.WriteFile(blockingFile, []byte(""), 0644))

	var stderr bytes.Buffer
	writer, closeLog := newLogWriter(&stderr, blockingFile)
	_, err := writer.Write([]byte("fallback log entry\n"))
	require.NoError(t, err)
	closeLog()

	require.Contains(t, stderr.String(), "failed to create SAL log directory")
	require.Contains(t, stderr.String(), "fallback log entry")
}
