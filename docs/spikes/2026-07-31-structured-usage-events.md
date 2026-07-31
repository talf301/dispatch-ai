# Structured usage events spike

Date: 2026-07-31

This is a CLI feasibility spike for `codex-cli 0.146.0` and `Claude Code 2.1.220`.
The commands below are the reproducible fixtures. They use bounded prompts and
do not modify the repository.

## Codex

Fixture:

```sh
codex exec --json --skip-git-repo-check \
  'Reply with exactly FIXTURE_OK and nothing else.'
```

Observed JSONL:

```json
{"type":"thread.started","thread_id":"019fba07-7bf4-74d0-bdb6-6e6024ea2038"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"FIXTURE_OK"}}
{"type":"turn.completed","usage":{"input_tokens":21284,"cached_input_tokens":11008,"cache_write_input_tokens":0,"output_tokens":7,"reasoning_output_tokens":0}}
```

The stable accounting fields observed are `thread.started.thread_id`,
`turn.started`, `turn.completed.usage.input_tokens`,
`cached_input_tokens`, `cache_write_input_tokens`, `output_tokens`, and
`reasoning_output_tokens`. The stable final text field is
`item.completed.item.text` when `item.type` is `agent_message`.

A command fixture emitted `item.started` and `item.completed` with
`item.type=command_execution`, `command`, `aggregated_output`, `exit_code`,
and `status`. This is enough to retain a bounded readable tool log. The
fixture did not expose a model field, provider cost, or a separate turn-final
status field. `reasoning_output_tokens` was present and zero, so consumers
must treat it as optional and not assume all versions emit it. The caller must
retain the model it requested separately because this event stream does not
identify one for pricing.

Resume fixture:

```sh
codex exec resume --json --skip-git-repo-check \
  019fba07-7bf4-74d0-bdb6-6e6024ea2038 \
  'Reply with exactly RESUME_OK and nothing else.'
```

It emitted a new `turn.started`, `item.completed` agent message, and
`turn.completed` usage while preserving the original `thread_id`. Therefore
completed Codex processes can be continued by a bounded result prompt when
the thread is persisted. This is provider-specific session resume, not a
generic deferred-command protocol: the caller must retain the provider
thread ID and issue a new prompt.

## Claude Code

Fixture:

```sh
claude -p --tools '' --output-format stream-json --verbose \
  'Reply with exactly CLAUDE_FIXTURE_OK and nothing else.'
```

The successful run returned `session_id=3c988ee9-02d8-455b-b098-4227c95d07a8`
and emitted this representative subset of the stream:

```json
{"type":"system","subtype":"init","session_id":"<uuid>","model":"claude-sonnet-5","claude_code_version":"2.1.220", "...":"..."}
{"type":"assistant","message":{"model":"claude-sonnet-5","usage":{"input_tokens":2,"output_tokens":1,"cache_creation_input_tokens":41728,"cache_read_input_tokens":0},"content":[{"type":"text","text":"CLAUDE_FIXTURE_OK"}]},"session_id":"<uuid>"}
{"type":"result","is_error":false,"num_turns":1,"session_id":"<uuid>","total_cost_usd":0.250644,"usage":{"input_tokens":2,"output_tokens":18,"cache_creation_input_tokens":41728,"cache_read_input_tokens":0,"iterations":[{"input_tokens":2,"output_tokens":18,"cache_read_input_tokens":0,"cache_creation_input_tokens":41728,"type":"message"}]},"modelUsage":{"claude-sonnet-5":{"inputTokens":2,"outputTokens":18,"cacheReadInputTokens":0,"cacheCreationInputTokens":41728,"costUSD":0.250644,"contextWindow":1000000,"maxOutputTokens":64000,"canonicalModel":"claude-sonnet-5","provider":"firstParty"}},"terminal_reason":"completed","subtype":"success","result":"CLAUDE_FIXTURE_OK"}
```

The stable successful-run fields observed are `system.session_id`,
`system.model`, `assistant.message.usage`, `assistant.message.content`,
`result.session_id`, `result.is_error`, `result.num_turns`, `result.usage`,
`result.modelUsage`, `result.total_cost_usd`, `result.terminal_reason`, and
`result.result`. `usage` includes input, output, cache creation/read, service
tier, and `iterations`; `modelUsage` is keyed by canonical model and includes
cost, context window, maximum output, and provider. No reasoning-token field
was exposed. With `--tools ''`, no tool-call event contract was exercised.

Resume fixture:

```sh
claude -p --resume 3c988ee9-02d8-455b-b098-4227c95d07a8 --tools '' \
  --output-format stream-json --verbose \
  'Reply with exactly CLAUDE_RESUME_OK and nothing else.'
```

It returned the same `session_id`, `is_error=false`,
`terminal_reason=completed`, `num_turns=1`, a new assistant message, and a
new result usage block. Therefore a completed Claude process can also be
continued by retaining its session ID and issuing a bounded prompt. This is
provider-specific session resume, not a generic deferred-command protocol.

The earlier `--bare` failure is still useful as a negative fixture: it returned
`result.subtype=success` together with `is_error=true` and
`terminal_reason=api_error`. Therefore `subtype` is not a success indicator;
consumers must key off `is_error` and `terminal_reason`.

## Parsing and decision

Both formats are line-delimited JSON and can be parsed incrementally. Keep a
bounded human-readable log by selecting event type, session/thread ID,
assistant text, command output, exit status, and terminal error, truncating
large text fields before rendering. Preserve the raw usage object separately
when present.

Go for usage accounting: yes for both providers. Codex exposes stable
turn-final token fields, and Claude exposes successful usage, cache, model,
cost, and terminal fields. Keep provider-specific parsing, treat optional
fields and unknown event types defensively, and retain the requested Codex
model outside its event stream. Go for generic deferred-command resume: no.
Both tested CLIs support provider-specific resume hooks keyed by their
persisted thread/session IDs, but arbitrary providers do not share that
protocol.
