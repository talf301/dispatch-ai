// Package llm is dispatch's single model chokepoint. Every call is one-shot
// and stateless: no resident process, no session, no tools (PRD invariant
// I1). Callers own their output contracts — anything that doesn't parse is
// the caller's hard error, never salvaged here.
package llm

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Timeout bounds every call; a label or a judge that takes longer than this
// is worth less than nothing on the capture path.
const Timeout = 30 * time.Second

// Oneshot sends one prompt and returns the raw text response.
// The binary and model come from DISPATCH_LLM_BIN / DISPATCH_LLM_MODEL
// (default: claude -p --model haiku).
func Oneshot(prompt string) (string, error) {
	bin := os.Getenv("DISPATCH_LLM_BIN")
	if bin == "" {
		bin = "claude"
	}
	model := os.Getenv("DISPATCH_LLM_MODEL")
	if model == "" {
		model = "haiku"
	}

	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "-p", "--model", model, prompt)
	out, err := cmd.Output()
	if err != nil {
		msg := err.Error()
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		return "", fmt.Errorf("llm call: %s", msg)
	}
	return strings.TrimSpace(string(out)), nil
}

// StripFence unwraps a markdown-fenced block when the fence is the entire
// output. This is the single permitted normalization across all structured
// call sites; JSON buried in prose stays a hard error.
func StripFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") || !strings.HasSuffix(s, "```") {
		return s
	}
	body := strings.TrimSuffix(strings.TrimPrefix(s, "```"), "```")
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		body = body[i+1:] // drop the language tag line
	}
	return strings.TrimSpace(body)
}
