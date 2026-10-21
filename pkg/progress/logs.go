package progress

import (
	"bytes"
	"sync"
)

// logBuffer collects the log as the slog handler writes it, line by line,
// for the log pane to show. Writes may come from any goroutine.
type logBuffer struct {
	mu      sync.Mutex
	lines   []string
	partial []byte
}

// Write splits what the handler wrote into lines, keeping an unfinished line
// until the rest of it arrives.
func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.partial = append(b.partial, p...)
	for {
		i := bytes.IndexByte(b.partial, '\n')
		if i < 0 {
			break
		}
		b.lines = append(b.lines, string(b.partial[:i]))
		b.partial = b.partial[i+1:]
	}
	return len(p), nil
}

// LinesFrom returns the lines written since the first n, so a reader that
// has seen n lines gets only the new ones.
func (b *logBuffer) LinesFrom(n int) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n >= len(b.lines) {
		return nil
	}
	return append([]string(nil), b.lines[n:]...)
}

// Len is how many complete lines have been written.
func (b *logBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.lines)
}
