package daemon

import (
	"strings"
	"testing"
)

func TestUsageCaptureProviderFixtures(t *testing.T) {
	tests := []struct {
		name, provider, fixture          string
		input, cached, output, reasoning int64
		turns                            int
	}{
		{"codex", "codex", `{"type":"thread.started","thread_id":"t1"}
{"type":"item.completed","item":{"type":"command_execution","command":"printf ok","aggregated_output":"ok","exit_code":0}}
{"type":"turn.completed","usage":{"input_tokens":21284,"cached_input_tokens":11008,"cache_write_input_tokens":100,"output_tokens":7,"reasoning_output_tokens":3}}
{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":2,"cache_write_input_tokens":5,"output_tokens":1,"reasoning_output_tokens":1}}`, 21399, 11010, 8, 4, 2},
		{"claude", "claude", `{"type":"system","session_id":"s1","model":"claude-sonnet"}
{"type":"assistant","message":{"usage":{"input_tokens":2,"output_tokens":1}}}
{"type":"result","is_error":false,"num_turns":1,"usage":{"input_tokens":2,"output_tokens":18,"cache_read_input_tokens":4,"cache_creation_input_tokens":41728},"result":"ok"}`, 41730, 4, 18, 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var log strings.Builder
			c := newUsageCapture(tt.provider, &log)
			_, _ = c.Write([]byte(tt.fixture))
			c.Flush()
			a := c.attempt(nil)
			if *a.InputTokens != tt.input || *a.CachedInputTokens != tt.cached || *a.OutputTokens != tt.output || *a.ReasoningTokens != tt.reasoning || *a.TurnCount != tt.turns {
				t.Fatalf("usage = %+v", a)
			}
			if log.Len() == 0 {
				t.Fatal("expected readable output")
			}
		})
	}
}
