package daemon

import (
	"io"
)

// TeeWriter writes to both a RingBuf (for in-memory crash context) and
// an optional io.Writer (for disk logging). It implements io.Writer and
// exposes String() for reading the ring buffer contents.
type TeeWriter struct {
	ring *RingBuf
	file io.Writer
}

// NewTeeWriter creates a TeeWriter. If file is nil, only the ring buffer is used.
func NewTeeWriter(ring *RingBuf, file io.Writer) *TeeWriter {
	return &TeeWriter{ring: ring, file: file}
}

func (t *TeeWriter) Write(p []byte) (int, error) {
	n, err := t.ring.Write(p)
	if err != nil {
		return n, err
	}
	if t.file != nil {
		t.file.Write(p)
	}
	return n, nil
}

// String returns the ring buffer contents.
func (t *TeeWriter) String() string {
	return t.ring.String()
}
