#!/usr/bin/env bash
# Content checks that the QA run left the user's live environment untouched.
#
#   isolation.sh S               run the checks; exit 1 on any hit, 2 when a check
#                                could not run
#   isolation.sh S --self-test   prove check (e) detects a planted dir
#
#   (b) no live houston hook state has a cwd or transcript_path under $S or a QA
#       session id, and nothing else in the live state dir mentions $S
#   (c) no pane of the live tmux server sits in a path under $S
#   (e) no ~/.claude/projects dir is named after a $S path
#
# Modification times prove nothing here, since the live houston and the user's
# agents write those dirs all the time, so the checks read content. They only
# read: (c) is the one plain `tmux` call in the harness, a listing against the
# caller's own server, and it refuses to run when the caller's TMUX_TMPDIR
# points into $S, since it would then inspect the private server. Overrides for
# tests: ISOLATION_LIVE_STATE, ISOLATION_PROJECTS, ISOLATION_CHECKS (default bce).
# With no live tmux server to inspect, (c) cannot run: that is exit 2, unless
# ISOLATION_NO_LIVE_TMUX=1 accepts it (the summary then leaves (c) out).
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=/dev/null
. "$here/lib.sh"

[ $# -ge 1 ] || {
	echo "usage: $0 S [--self-test]" >&2
	exit 2
}
S=$1
qa_check_root

live_state=${ISOLATION_LIVE_STATE:-$HOME/.local/state/houston}
projects=${ISOLATION_PROJECTS:-$HOME/.claude/projects}
checks=${ISOLATION_CHECKS:-bce}
encoded=$(printf %s "$S" | sed 's/[^A-Za-z0-9]/-/g')

if [ "${2:-}" = --self-test ]; then
	plant=$(mktemp -d "$S/isolation-selftest.XXXXXX")
	mkdir -p -- "$plant/$encoded-repos-qa-repo"
	rc=0
	ISOLATION_PROJECTS=$plant ISOLATION_CHECKS=e "$0" "$S" >/dev/null 2>&1 || rc=$?
	rm -rf -- "$plant"
	[ "$rc" = 1 ] || {
		echo "self-test failed: check (e) exited $rc on a planted dir, expected 1" >&2
		exit 1
	}
	echo "self-test ok: check (e) exits 1 on a planted dir"
	exit 0
fi

fail=0
incomplete=
ran=
hit() {
	echo "ISOLATION HIT ($1): $2" >&2
	fail=1
}

if [[ $checks == *b* ]]; then
	ran+=b
	if [ -d "$live_state" ]; then
		# Hook state records cwd, transcript_path and the session id. Other
		# fields (tool_input_hint, last_message) can legitimately quote a command
		# of the agent running this harness, so only those three are read.
		ids=()
		for f in "$S"/state/claude/*.json; do
			[ -f "$f" ] || continue
			jq -e --arg s "$S/" '(.cwd // "") | startswith($s)' "$f" >/dev/null 2>&1 || continue
			ids+=("$(jq -r '.session_id // empty' "$f")")
		done
		qa_ids=$(printf '%s\n' "${ids[@]}" | jq -R . | jq -sc .)
		for f in "$live_state"/claude/*.json; do
			[ -f "$f" ] || continue
			jq -e --arg s "$S/" --arg e "$encoded" --argjson ids "$qa_ids" \
				'((.cwd // "") | startswith($s)) or ((.transcript_path // "") | contains($e)) or (.session_id as $i | $ids | index($i) != null)' \
				"$f" >/dev/null 2>&1 && hit b "$f names $S or a QA session"
		done
		# Anything else the live houston keeps (repo registry, launch files).
		while IFS= read -r f; do
			hit b "$f mentions $S"
		done < <(grep -rlF --exclude-dir=claude -- "$S" "$live_state" 2>/dev/null || true)
	fi
fi

if [[ $checks == *c* ]]; then
	case ${TMUX_TMPDIR:-} in
	"$S"*)
		echo "refusing: TMUX_TMPDIR points into $S, a plain tmux would list the private server" >&2
		exit 2
		;;
	esac
	if panes=$(tmux list-panes -a -F '#{pane_current_path}' 2>/dev/null); then
		ran+=c
		while IFS= read -r p; do
			case $p in
			"$S" | "$S"/*) hit c "live tmux pane in $p" ;;
			esac
		done <<<"$panes"
	elif [ "${ISOLATION_NO_LIVE_TMUX:-}" = 1 ]; then
		echo "note: no live tmux server answered; check (c) skipped (ISOLATION_NO_LIVE_TMUX=1)" >&2
	else
		echo "could not inspect the live tmux server; check (c) did not run (ISOLATION_NO_LIVE_TMUX=1 accepts that)" >&2
		incomplete=1
	fi
fi

if [[ $checks == *e* ]]; then
	ran+=e
	if [ -d "$projects" ]; then
		while IFS= read -r d; do
			hit e "$d"
		done < <(find "$projects" -maxdepth 1 -mindepth 1 -name "*$encoded*" 2>/dev/null)
	fi
fi

[ "$fail" = 0 ] || exit 1
[ -z "$incomplete" ] || exit 2
echo "isolation ok for $S (checks: $ran)"
