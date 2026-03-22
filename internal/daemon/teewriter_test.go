package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTeeWriter_WritesToBothDestinations(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "test.log")
	f, err := os.Create(logFile)
	if err != nil {
		t.Fatal(err)
	}

	buf := NewRingBuf(10)
	tw := NewTeeWriter(buf, f)

	tw.Write([]byte("line one\nline two\n"))
	f.Close()

	// Check ring buffer got the data.
	if out := buf.String(); !strings.Contains(out, "line one") {
		t.Errorf("ring buffer missing data: %q", out)
	}

	// Check log file got the data.
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "line one") {
		t.Errorf("log file missing data: %q", string(data))
	}
}

func TestTeeWriter_String_DelegatesToRingBuf(t *testing.T) {
	buf := NewRingBuf(10)
	tw := NewTeeWriter(buf, nil)

	tw.Write([]byte("hello\n"))

	if out := tw.String(); !strings.Contains(out, "hello") {
		t.Errorf("String() = %q, want it to contain %q", out, "hello")
	}
}
