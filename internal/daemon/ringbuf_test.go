package daemon

import (
	"fmt"
	"testing"
)

func TestRingBuf_UnderCapacity(t *testing.T) {
	rb := NewRingBuf(5)
	rb.Write([]byte("line1\nline2\nline3\n"))
	got := rb.String()
	want := "line1\nline2\nline3"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRingBuf_OverCapacity(t *testing.T) {
	rb := NewRingBuf(3)
	for i := 1; i <= 5; i++ {
		rb.Write([]byte(fmt.Sprintf("line%d\n", i)))
	}
	got := rb.String()
	want := "line3\nline4\nline5"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRingBuf_Empty(t *testing.T) {
	rb := NewRingBuf(5)
	if got := rb.String(); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestRingBuf_PartialLines(t *testing.T) {
	rb := NewRingBuf(3)
	rb.Write([]byte("partial"))
	rb.Write([]byte(" still partial\nline2\n"))
	got := rb.String()
	want := "partial still partial\nline2"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRingBuf_ImplementsWriter(t *testing.T) {
	var _ interface{ Write([]byte) (int, error) } = NewRingBuf(5)
}
