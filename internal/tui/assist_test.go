package tui

import "testing"

func TestParseProposal(t *testing.T) {
	cmds, err := parseProposal(`["block 4e9a \"waiting\"", "note 4e9a \"why\""]`)
	if err != nil || len(cmds) != 2 {
		t.Errorf("valid contract rejected: %v %v", cmds, err)
	}
	// Fenced pure JSON is the single permitted normalization.
	if cmds, err = parseProposal("```json\n[\"done 4e9a\"]\n```"); err != nil || len(cmds) != 1 {
		t.Errorf("fenced JSON rejected: %v %v", cmds, err)
	}
	// Prose is a hard error — the raw output is shown, never salvaged.
	if _, err := parseProposal(`I'd suggest: ["done 4e9a"]`); err == nil {
		t.Error("prose accepted")
	}
	// A JSON object (not array) breaks the contract too.
	if _, err := parseProposal(`{"commands": ["done 4e9a"]}`); err == nil {
		t.Error("object accepted")
	}
}
