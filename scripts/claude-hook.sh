#!/usr/bin/env bash
# Claude Code hook script for tmux-dashboard
# Writes structured status to JSON files for dashboard monitoring
#
# Install: Add to ~/.claude/settings.json:
# {
#   "hooks": {
#     "Notification": [{"hooks": [{"type": "command", "command": "path/to/claude-hook.sh"}]}]
#   }
# }
#
# Optional hooks (not required - terminal parsing handles most cases):
# - Stop: marks session as idle when agent finishes
# - PreToolUse: shows current tool (noisy, fires constantly)

set -euo pipefail

STATUS_DIR="${TMUX_DASHBOARD_STATUS_DIR:-$HOME/.local/state/tmux-dashboard}"
mkdir -p "$STATUS_DIR"

# Get tmux session name (escape slashes for filename)
TMUX_SESSION=$(tmux display-message -p '#S' 2>/dev/null || echo "unknown")
FILENAME=$(echo "$TMUX_SESSION" | tr '/' '%')

# Read JSON from stdin
INPUT=$(cat)

# Extract event type
EVENT=$(echo "$INPUT" | jq -r '.hook_event_name // empty')

# Build status based on event type
case "$EVENT" in
    Notification)
        TYPE=$(echo "$INPUT" | jq -r '.notification_type // empty')
        MESSAGE=$(echo "$INPUT" | jq -r '.message // empty')

        case "$TYPE" in
            permission_prompt)
                STATUS="permission"
                ;;
            idle_prompt)
                STATUS="waiting"
                ;;
            *)
                STATUS="notification"
                ;;
        esac

        jq -n \
            --arg session "$TMUX_SESSION" \
            --arg status "$STATUS" \
            --arg type "$TYPE" \
            --arg message "$MESSAGE" \
            '{
                tmux_session: $session,
                status: $status,
                notification_type: $type,
                message: $message,
                timestamp: now | floor
            }' > "$STATUS_DIR/$FILENAME.json"
        ;;

    Stop|SubagentStop)
        jq -n \
            --arg session "$TMUX_SESSION" \
            '{
                tmux_session: $session,
                status: "idle",
                message: "Agent stopped",
                timestamp: now | floor
            }' > "$STATUS_DIR/$FILENAME.json"
        ;;

    PreToolUse)
        TOOL=$(echo "$INPUT" | jq -r '.tool_name // empty')
        DESC=$(echo "$INPUT" | jq -r '.tool_input.description // .tool_input.command // empty' | head -c 100)

        jq -n \
            --arg session "$TMUX_SESSION" \
            --arg tool "$TOOL" \
            --arg desc "$DESC" \
            '{
                tmux_session: $session,
                status: "working",
                tool: $tool,
                message: $desc,
                timestamp: now | floor
            }' > "$STATUS_DIR/$FILENAME.json"
        ;;
esac
