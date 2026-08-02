package tui

import (
	"strings"
	"testing"
)

func TestNewSelectsCurrentRepo(t *testing.T) {
	m := New(nil, nil, "dt", []string{"/one", "/two"}, "/two")
	if m.repoCursor != 1 {
		t.Fatalf("repoCursor = %d, want 1", m.repoCursor)
	}
}

func TestGoVerbArgsIncludesSelectedRepo(t *testing.T) {
	m := Model{captureRepo: "/repo"}
	got := m.goVerbArgs("go", "ship it")
	want := []string{"go", "--repo", "/repo", "ship it"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args = %#v, want %#v", got, want)
		}
	}
}

func TestRenameViewShowsInput(t *testing.T) {
	m := New(nil, nil, "dt", nil, "")
	m.mode = modeRename
	m.input.SetValue("new name")
	if !strings.Contains(m.View(), "new name") {
		t.Fatal("rename view does not show the current input")
	}
}
