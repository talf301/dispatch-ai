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
{"type":"turn.completed","usage":{"input_tokens":21284,"cached_input_tokens":11008,"output_tokens":7,"reasoning_output_tokens":3}}`, 21284, 11008, 7, 3, 1},
		{"claude", "claude", `{"type":"system","session_id":"s1","model":"claude-sonnet"}
{"type":"assistant","message":{"usage":{"input_tokens":2,"output_tokens":1}}}
{"type":"result","is_error":false,"num_turns":1,"usage":{"input_tokens":2,"output_tokens":18,"cache_read_input_tokens":4},"result":"ok"}`, 2, 4, 18, 0, 1},
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
