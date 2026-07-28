package dedup

import "testing"

func TestTopCandidates(t *testing.T) {
	closed := []Candidate{
		{ID: "c5e8", Text: "clean up session leak on merge failure", Reason: "fixed upstream in go-git 5.11"},
		{ID: "a111", Text: "bump sqlite3 to latest"},
		{ID: "b222", Text: "fix the session leak when merges fail"},
	}

	hits := TopCandidates("i want to fix the session leak on failed merges", closed, 5)
	if len(hits) == 0 {
		t.Fatal("expected hits for near-identical thought")
	}
	if hits[0].ID != "b222" && hits[0].ID != "c5e8" {
		t.Errorf("best hit should be a session-leak task, got %s", hits[0].ID)
	}
	for _, h := range hits {
		if h.ID == "a111" {
			t.Error("unrelated task cleared the floor")
		}
	}

	if hits := TopCandidates("rewrite the docs landing page", closed, 5); len(hits) != 0 {
		t.Errorf("unrelated thought matched: %+v", hits)
	}
}

func TestParseJudge(t *testing.T) {
	m, err := ParseJudge(`[{"id": "c5e8", "reason": "same leak"}]`)
	if err != nil || len(m) != 1 || m[0].ID != "c5e8" {
		t.Errorf("valid contract rejected: %v %v", m, err)
	}
	if m, err := ParseJudge("[]"); err != nil || len(m) != 0 {
		t.Errorf("empty array rejected: %v %v", m, err)
	}
	// A fenced pure-JSON body is the one permitted normalization.
	m, err = ParseJudge("```json\n[{\"id\": \"c5e8\", \"reason\": \"same\"}]\n```")
	if err != nil || len(m) != 1 {
		t.Errorf("fenced JSON rejected: %v %v", m, err)
	}
	// Prose is a hard error, never salvaged.
	if _, err := ParseJudge("Sure! Here are the matches: []"); err == nil {
		t.Error("prose accepted")
	}
	if _, err := ParseJudge("Here you go:\n```json\n[]\n```"); err == nil {
		t.Error("JSON buried in prose accepted")
	}
}
