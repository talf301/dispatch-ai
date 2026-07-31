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
must treat it as optional and not assume all versions emit it.

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
claude -p --bare --tools '' --output-format stream-json --verbose \
  'Reply with exactly CLAUDE_FIXTURE_OK and nothing else.'
```

The installed CLI was not authenticated. It still emitted this stream shape:

```json
{"type":"system","subtype":"init","session_id":"<uuid>","model":"claude-fable-5","claude_code_version":"2.1.220", "...":"..."}
{"type":"assistant","message":{"model":"<synthetic>","usage":{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0},"content":[{"type":"text","text":"Not logged in · Please run /login"}]},"session_id":"<uuid>","error":"authentication_failed"}
{"type":"result","is_error":true,"session_id":"<uuid>","usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0},"modelUsage":{},"terminal_reason":"api_error","result":"Not logged in · Please run /login"}
```

The failure stream establishes the envelope fields `system.session_id`,
`system.model`, `assistant.message.usage`, `assistant.content`,
`result.session_id`, `result.is_error`, `result.usage`, `result.modelUsage`,
and `result.terminal_reason`. It does not establish successful-run semantics.
In particular, successful input/output token values, cache token behavior,
reasoning token behavior, turn boundaries, and tool-call event fields remain
unverified. The CLI exposes `--resume`, but resume behavior was not tested
because no authenticated session could be created.

## Parsing and decision

Both formats are line-delimited JSON and can be parsed incrementally. Keep a
bounded human-readable log by selecting event type, session/thread ID,
assistant text, command output, exit status, and terminal error, truncating
large text fields before rendering. Preserve the raw usage object separately
when present.

Go for usage accounting: Codex has a sufficient stable contract now; use
optional fields and tolerate unknown event types. Claude needs one
authenticated fixture before implementation commits to a successful usage
contract. Go for generic deferred-command resume: no. Implement a thin
provider-specific resume hook keyed by Codex `thread_id` only if needed;
do not pretend Claude or arbitrary providers share that protocol.
