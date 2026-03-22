#!/bin/bash
# SessionStart hook for the lead agent
# Reads the global state ledger and injects it as context on fresh session start

STATE_FILE="$HOME/.dispatch/state.md"

if [ -f "$STATE_FILE" ]; then
  echo "## Dispatch State (from previous session)"
  echo ""
  cat "$STATE_FILE"
  echo ""
  echo "---"
  echo "Check each project's task list for current details. The above is a summary from your last session."
fi
