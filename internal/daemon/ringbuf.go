package daemon

import (
	"io"
	"strings"
	"sync"
)

// Compile-time check that RingBuf implements io.Writer.
var _ io.Writer = (*RingBuf)(nil)

// RingBuf is a line-oriented ring buffer that keeps the last N lines.
// It implements io.Writer.
type RingBuf struct {
	mu       sync.Mutex
	lines    []string
	maxLines int
	partial  string // incomplete line (no trailing newline yet)
}

// NewRingBuf creates a ring buffer that keeps the last n lines.
func NewRingBuf(n int) *RingBuf {
	return &RingBuf{
		lines:    make([]string, 0, n),
		maxLines: n,
	}
}

// Write implements io.Writer. It splits input on newlines and stores
// complete lines in the ring buffer.
func (r *RingBuf) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	s := r.partial + string(p)
	parts := strings.Split(s, "\n")

	// Last element is either empty (input ended with \n) or a partial line.
	r.partial = parts[len(parts)-1]
	completedLines := parts[:len(parts)-1]

	for _, line := range completedLines {
		r.addLine(line)
	}
	return len(p), nil
}

func (r *RingBuf) addLine(line string) {
	if len(r.lines) >= r.maxLines {
		// Shift left by 1.
		copy(r.lines, r.lines[1:])
		r.lines[len(r.lines)-1] = line
	} else {
		r.lines = append(r.lines, line)
	}
}

// String returns all stored lines joined by newlines.
// Includes any partial (unterminated) line at the end.
func (r *RingBuf) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	result := make([]string, len(r.lines))
	copy(result, r.lines)
	if r.partial != "" {
		result = append(result, r.partial)
	}
	return strings.Join(result, "\n")
}
