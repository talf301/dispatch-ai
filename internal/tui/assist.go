package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dispatch-ai/dispatch/internal/db"
	"github.com/dispatch-ai/dispatch/internal/llm"
)

// M4: the two assistance call sites. The model prepares decisions; it does
// not make them. Site 3 (fuzzy command) renders for confirmation and
// executes via dt batch; site 4 (brief) is prose, displayed verbatim.

const batchVerbs = `add "<title>" [-d "<desc>"] [-p <parent-id>] [--after <id>] [-r <repo-path>]
edit <id> [-t "<title>"] [-d "<desc>"] [-r <repo-path>]
dep <task-id> <depends-on-id>
undep <task-id> <depends-on-id>
claim <id> <assignee>
release <id>
done <id>
block <id> "<reason>"
reopen <id>
note <id> "<content>"
kill <id> "<reason>"
park <id>
resume <id>
relabel <id> "<text>"`

func proposePrompt(snapshot []byte, instruction string) string {
	return fmt.Sprintf(`You translate an instruction about a task board into dt batch commands.

Current board state (JSON):

%s

Available commands, one per line, exactly this syntax:

%s

Instruction: %s

Reply with ONLY a raw JSON array of command strings — no prose, no markdown fences.
Example: ["block 4e9a \"waiting on schema migration\"", "note 4e9a \"blocked by tal via board\""]
If the instruction cannot be expressed with these commands, reply with [].`,
		snapshot, batchVerbs, instruction)
}

// parseProposal enforces site 3's contract: a bare JSON array of strings.
// Anything else is a hard error shown to the human with the raw output.
func parseProposal(raw string) ([]string, error) {
	var cmds []string
	if err := json.Unmarshal([]byte(llm.StripFence(raw)), &cmds); err != nil {
		return nil, fmt.Errorf("model broke the command contract; raw output:\n%s", raw)
	}
	return cmds, nil
}

func briefPrompt(snapshot []byte, since time.Time) string {
	when := "ever"
	if !since.IsZero() {
		when = since.UTC().Format("2006-01-02 15:04 MST")
	}
	return fmt.Sprintf(`This is a developer's task board (JSON). Fields: status is the lifecycle
(live/unattended/blocked/parked/proposed/done/killed), thought is what they
wanted verbatim, updated_at is the last change.

%s

Write a brief for the developer: what changed or needs their attention since %s.
Lead with anything blocked or proposed, then finished work, then the quiet stuff.
Plain prose, no headings, at most 200 words. Mention task ids inline like (4e9a).`,
		snapshot, when)
}

// propose runs site 3 end to end: snapshot → model → strict parse.
func propose(store *db.DB, instruction string) ([]string, error) {
	snap, err := store.SnapshotJSON()
	if err != nil {
		return nil, err
	}
	raw, err := llm.Oneshot(proposePrompt(snap, instruction))
	if err != nil {
		return nil, err
	}
	cmds, err := parseProposal(raw)
	if err != nil {
		return nil, err
	}
	if len(cmds) == 0 {
		return nil, fmt.Errorf("the model found no commands for that instruction")
	}
	return cmds, nil
}

// brief runs site 4: snapshot + last-seen → prose.
func brief(store *db.DB) (string, error) {
	snap, err := store.SnapshotJSON()
	if err != nil {
		return "", err
	}
	since, err := store.LastSeen()
	if err != nil {
		return "", err
	}
	text, err := llm.Oneshot(briefPrompt(snap, since))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
}
