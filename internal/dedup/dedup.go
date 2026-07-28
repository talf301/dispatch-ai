// Package dedup finds closed work similar to a new thought, at capture time.
// Stage 1 is deterministic retrieval here; stage 2 (the judge) is one model
// call whose strict output contract is also enforced here.
//
// ponytail: retrieval is in-memory token overlap over the closed ledger, not
// FTS5 — the default go-sqlite3 build lacks FTS5 (needs -tags sqlite_fts5 on
// every build). At <10k short rows this is sub-millisecond; revisit FTS5 or
// modernc.org/sqlite if the ledger outgrows it.
package dedup

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/dispatch-ai/dispatch/internal/llm"
)

// stopwords: tokens that carry no signal in a one-line thought.
var stopwords = map[string]bool{
	"i": true, "a": true, "an": true, "the": true, "to": true, "of": true,
	"in": true, "on": true, "for": true, "and": true, "or": true, "is": true,
	"it": true, "its": true, "this": true, "that": true, "want": true,
	"with": true, "into": true, "out": true, "my": true, "we": true,
	"should": true, "would": true, "lets": true, "let's": true, "be": true,
}

// stem trims the inflections that make "fixed"/"fix" and "merges"/"merge"
// miss each other. Retrieval only has to surface candidates — the judge
// decides — so a three-suffix stem is enough.
func stem(w string) string {
	for _, suf := range []string{"ing", "ed", "s"} {
		if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 3 {
			return w[:len(w)-len(suf)]
		}
	}
	return w
}

func tokens(s string) map[string]bool {
	out := make(map[string]bool)
	for _, w := range strings.Fields(strings.ToLower(s)) {
		w = strings.Trim(w, ".,;:!?\"'()[]{}")
		if len(w) < 2 || stopwords[w] {
			continue
		}
		out[stem(w)] = true
	}
	return out
}

// Similarity is cosine overlap between token sets: |A∩B| / sqrt(|A|·|B|).
func Similarity(a, b string) float64 {
	ta, tb := tokens(a), tokens(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	shared := 0
	for w := range ta {
		if tb[w] {
			shared++
		}
	}
	return float64(shared) / math.Sqrt(float64(len(ta))*float64(len(tb)))
}

// Candidate is a closed task considered similar to the new thought.
type Candidate struct {
	ID      string
	Text    string // thought (or title for pre-v2 rows)
	Status  string
	Reason  string // kill_reason if any
	Closed  string // updated_at
	Score   float64
}

// Floor below which a candidate isn't worth showing. Budget: err toward
// showing — a false positive costs one keystroke.
const Floor = 0.3

// TopCandidates scores every closed task against the thought and returns up
// to n above the floor, best first. The searchable text is the verbatim
// thought plus kill reason and acceptance — never the label.
func TopCandidates(thought string, closed []Candidate, n int) []Candidate {
	var hits []Candidate
	for _, c := range closed {
		text := c.Text
		if c.Reason != "" {
			text += " " + c.Reason
		}
		c.Score = Similarity(thought, text)
		if c.Score >= Floor {
			hits = append(hits, c)
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > n {
		hits = hits[:n]
	}
	return hits
}

// Match is the judge's verdict on one candidate.
type Match struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// JudgePrompt renders the one-shot dedup judgment (PRD site 2).
func JudgePrompt(thought string, cands []Candidate) string {
	var b strings.Builder
	b.WriteString("A developer is about to start this task:\n\n  ")
	b.WriteString(thought)
	b.WriteString("\n\nThese previously closed tasks looked similar:\n\n")
	for _, c := range cands {
		fmt.Fprintf(&b, "  id=%s status=%s closed=%s\n    %q\n", c.ID, c.Status, c.Closed, c.Text)
		if c.Reason != "" {
			fmt.Fprintf(&b, "    closed because: %s\n", c.Reason)
		}
	}
	b.WriteString("\nWhich of these, if any, are the SAME work as the new task (not merely related)?\n")
	b.WriteString("Reply with ONLY a raw JSON array — no prose, no markdown fences. Empty array if none.\n")
	b.WriteString(`Format: [{"id": "<id>", "reason": "<one line why it's the same work>"}]`)
	return b.String()
}


// ParseJudge enforces the output contract. Anything that isn't a bare JSON
// array of {id, reason} is a hard error carrying the raw output — no salvage
// parsing, by design (PRD §13).
func ParseJudge(raw string) ([]Match, error) {
	var out []Match
	if err := json.Unmarshal([]byte(llm.StripFence(raw)), &out); err != nil {
		return nil, fmt.Errorf("dedup judge broke its output contract; raw output:\n%s", raw)
	}
	return out, nil
}
