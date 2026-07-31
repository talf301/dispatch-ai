package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/dispatch-ai/dispatch/internal/db"
)

// usageCapture turns provider JSONL into a bounded readable log while keeping
// the provider's raw final usage object for the usage ledger.
type usageCapture struct {
	mu                                                sync.Mutex
	provider                                          string
	out                                               io.Writer
	buf                                               bytes.Buffer
	model, session                                    string
	input, cached, outputTokens, reasoning, toolBytes int64
	turns                                             int
	hasUsage                                          bool
	raw                                               json.RawMessage
}

func newUsageCapture(provider string, out io.Writer) *usageCapture {
	return &usageCapture{provider: provider, out: out}
}

func (c *usageCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buf.Write(p)
	for {
		line, err := c.buf.ReadString('\n')
		if err == io.EOF {
			c.buf.WriteString(line)
			break
		}
		c.line(strings.TrimSpace(line))
	}
	return len(p), nil
}

func (c *usageCapture) Flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.buf.Len() > 0 {
		c.line(strings.TrimSpace(c.buf.String()))
		c.buf.Reset()
	}
}

func (c *usageCapture) line(line string) {
	if line == "" {
		return
	}
	var event map[string]any
	if json.Unmarshal([]byte(line), &event) != nil {
		fmt.Fprintln(c.out, line)
		return
	}
	c.parse(event)
	if text := readableEvent(event); text != "" {
		fmt.Fprintln(c.out, text)
	}
}

func (c *usageCapture) parse(e map[string]any) {
	switch c.provider {
	case "codex":
		if e["type"] == "thread.started" {
			c.session, _ = e["thread_id"].(string)
		}
		if e["type"] == "turn.completed" {
			c.turns++
			c.addUsage(e["usage"], false)
		}
		if item, ok := e["item"].(map[string]any); ok && item["type"] == "command_execution" {
			c.toolBytes += int64(len(fmt.Sprint(item["aggregated_output"])))
		}
	case "claude":
		if e["type"] == "system" {
			c.session, _ = e["session_id"].(string)
			c.model, _ = e["model"].(string)
		}
		if e["type"] == "assistant" {
			if m, ok := e["message"].(map[string]any); ok {
				if c.model == "" {
					c.model, _ = m["model"].(string)
				}
				c.addUsage(m["usage"], true)
			}
		}
		if e["type"] == "result" {
			c.session, _ = e["session_id"].(string)
			if n, ok := e["num_turns"].(float64); ok {
				c.turns = int(n)
			}
			// The final result is authoritative; assistant events repeat partial usage.
			c.input, c.cached, c.outputTokens, c.reasoning = 0, 0, 0, 0
			c.addUsage(e["usage"], true)
			facts := map[string]any{"usage": e["usage"], "modelUsage": e["modelUsage"], "total_cost_usd": e["total_cost_usd"], "terminal_reason": e["terminal_reason"], "is_error": e["is_error"]}
			c.raw, _ = json.Marshal(facts)
		}
	}
}

func (c *usageCapture) addUsage(v any, claude bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	c.hasUsage = true
	num := func(key string) int64 { x, _ := m[key].(float64); return int64(x) }
	if claude {
		c.input += num("input_tokens")
		c.input += num("cache_creation_input_tokens")
		c.cached += num("cache_read_input_tokens") + num("cached_input_tokens")
		c.outputTokens += num("output_tokens")
		c.reasoning += num("reasoning_output_tokens")
	} else {
		// Codex emits final usage for each completed turn, not a cumulative
		// thread total; sum the per-turn deltas.
		c.input += num("input_tokens")
		c.input += num("cache_write_input_tokens")
		c.cached += num("cached_input_tokens")
		c.outputTokens += num("output_tokens")
		c.reasoning += num("reasoning_output_tokens")
	}
	if !claude {
		c.raw, _ = json.Marshal(v)
	}
}

func (c *usageCapture) attempt(model *string) db.Attempt {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.model != "" {
		model = &c.model
	}
	a := db.Attempt{Model: model, RawUsage: c.raw}
	if c.hasUsage {
		a.InputTokens, a.CachedInputTokens, a.OutputTokens, a.ReasoningTokens = ptr64(c.input), ptr64(c.cached), ptr64(c.outputTokens), ptr64(c.reasoning)
		a.TurnCount = ptr(c.turns)
	}
	if c.toolBytes > 0 {
		a.ToolOutputBytes = ptr64(c.toolBytes)
	}
	return a
}

func ptr64(v int64) *int64 { return &v }
func ptr(v int) *int       { return &v }

func readableEvent(e map[string]any) string {
	t, _ := e["type"].(string)
	if t == "item.completed" {
		if i, ok := e["item"].(map[string]any); ok {
			switch i["type"] {
			case "agent_message":
				return fmt.Sprint(i["text"])
			case "command_execution":
				return fmt.Sprintf("$ %s\n%s (exit %v)", i["command"], i["aggregated_output"], i["exit_code"])
			}
		}
	}
	if t == "assistant" {
		if m, ok := e["message"].(map[string]any); ok {
			if cs, ok := m["content"].([]any); ok {
				for _, v := range cs {
					if x, ok := v.(map[string]any); ok && x["type"] == "text" {
						return fmt.Sprint(x["text"])
					}
				}
			}
		}
	}
	if t == "result" {
		if e["is_error"] == true {
			return "provider error: " + fmt.Sprint(e["terminal_reason"])
		}
		return fmt.Sprint(e["result"])
	}
	return ""
}

func exitStatus(err error) int {
	if err == nil {
		return 0
	}
	if e, ok := err.(interface{ ExitCode() int }); ok {
		return e.ExitCode()
	}
	return -1
}
