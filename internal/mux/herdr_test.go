package mux

import "testing"

func TestShellQuote(t *testing.T) {
	if got := shellQuote("it's a thought\nwith `code`"); got != "'it'\\''s a thought\nwith `code`'" {
		t.Fatalf("shellQuote() = %q", got)
	}
}
