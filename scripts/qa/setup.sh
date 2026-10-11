#!/usr/bin/env bash
# Builds and starts the isolated QA environment: a scratch dir, a private tmux
# server holding the loadfixture crew windows plus real claude and pi agents, and
# a houston under test in dispatcher mode.
#
#   setup.sh [--no-agents] [S]
#
# S defaults to a fresh `mktemp -d /tmp/hqa.XXXXXX`; an existing S is accepted
# only when empty or already marked .houstonqa, and a real directory you own
# with mode 700 (a new one is created that way). Keep it short: the tmux socket
# path must fit sun_path. On success it prints PORT=<port> and S=<S> and writes
# $S/qa.env (sourceable KEY=value lines) for scripts/qa/qa.cjs. --no-agents
# skips the claude and pi windows for scenarios that need fixture data only.
# Tear down with scripts/qa/teardown.sh S.
#
# The live houston (127.0.0.1:9090, ~/.local/state/houston) and the user's tmux
# server must receive nothing, hence:
#   - one private tmux server (scripts/perf/crewtmux.sh, @crewtmux marker), and
#     every tmux call here goes through tp (lib.sh)
#   - the crew and Claude session variables of the calling agent are unset and
#     removed from the server's global environment: an agent started with
#     CREW_WORKER_ID would have hookyard post "exited" to the real crew bus
#   - HOUSTON_STATE_DIR/HOUSTON_STATUS_DIR point the agents' hooks (the deployed
#     `houston hook`) at $S/state before any agent starts
#   - claude runs under bwrap with $S/home/.claude/projects bound over
#     ~/.claude/projects, so the live houston, which rescans that dir, never sees
#     QA transcripts; the rest of ~/.claude is the real one (credentials, plugins)
#   - pi writes its sessions to $S/pi-sessions
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
# shellcheck source=/dev/null
. "$here/lib.sh"

agents=1
S=
for a in "$@"; do
	case $a in
	--no-agents) agents= ;;
	*) S=$a ;;
	esac
done

if [ -z "$S" ]; then
	S=$(mktemp -d /tmp/hqa.XXXXXX)
elif [ -d "$S" ] && [ ! -f "$S/.houstonqa" ] && [ -n "$(ls -A -- "$S")" ]; then
	echo "refusing: $S exists, is not empty and has no .houstonqa marker" >&2
	exit 2
fi
[[ $S =~ ^/tmp/hqa\.[A-Za-z0-9]+$ ]] || {
	echo "refusing: S must be a /tmp/hqa.<random> dir, got: $S" >&2
	exit 2
}
if [ -e "$S" ] || [ -L "$S" ]; then
	qa_check_dir
else
	mkdir -m 700 -- "$S"
fi
touch "$S/.houstonqa"
qa_check_root

ok=
trap '[ -n "$ok" ] || echo "setup failed; clean up with: scripts/qa/teardown.sh $S" >&2' EXIT

qa_scrub_env
QA_PI_MODEL=${QA_PI_MODEL:-openrouter/~google/gemini-flash-latest}
cd "$root"

echo "== build"
[ -d ui/node_modules ] || npm ci --prefix ui
just ui-build
go build -o "$S/houston" .
go run -tags tools ./cmd/loadfixture -dir "$S" -sessions 12 -crews 2 -workers 7

echo "== private tmux server"
mkdir -p -- "$S/tmux"
chmod 700 -- "$S/tmux"
"$root/scripts/perf/crewtmux.sh" start "$S" "$S"
for v in CREW_WORKER_ID CREW_ID CREW_ROLE_ID DISPATCH_SPEC; do
	tp set-environment -gu "$v"
done
tp set-environment -g HOUSTON_STATE_DIR "$S/state"
tp set-environment -g HOUSTON_STATUS_DIR "$S/state"

# The second crew becomes an orphan: no dispatcher pane file, so its workers
# have no dispatcher run to nest under.
crews=()
while IFS=$'\t' read -r kind crew _; do
	[ "$kind" = dispatcher ] && crews+=("$crew")
done <"$S/crews.tsv"
[ ${#crews[@]} -ge 2 ] || {
	echo "expected two crews in $S/crews.tsv" >&2
	exit 1
}
rm -f -- "$S/repo/.git/crew/crews/${crews[1]}/pane"

echo "== scratch repos"
mkdir -p -- "$S/repos" "$S/pi-sessions"
git init -q -b main "$S/repos/qa-repo"
printf '# qa-repo\n' >"$S/repos/qa-repo/README.md"
printf '#!/bin/sh\necho hello\n' >"$S/repos/qa-repo/hello.sh"
git -C "$S/repos/qa-repo" add README.md hello.sh
git -C "$S/repos/qa-repo" -c user.name=qa -c user.email=qa@example.invalid -c commit.gpgsign=false commit -q -m init
git -C "$S/repos/qa-repo" worktree add -q "$S/repos/qa-wt" -b qa-wt

echo "== windows"
tp new-session -d -E -s qa -n keepalive -c "$S/repos/qa-repo" 'sleep 100000'

# new_window NAME DIR CMD...: opens a stamped window, prints "<window> <pane>".
new_window() {
	local name=$1 dir=$2 ids win pane
	shift 2
	ids=$(tp new-window -d -t qa: -n "$name" -c "$dir" -P -F '#{window_id} #{pane_id}' "$@")
	read -r win pane <<<"$ids"
	tp set-option -w -t "$win" @git_root "$dir"
	echo "$win $pane"
}

# A shell pane is a run only with @claude_status, which lazytmux sets on the host
# and the private server lacks, so the shell panes are stamped like crewtmux's.
shell_window() {
	local ids pane
	ids=$(new_window "$1" "$2" env 'PS1=qa$ ' bash --norc --noprofile -i)
	read -r _ pane <<<"$ids"
	tp set-option -p -t "$pane" @claude_status "idle $(date +%s)"
	echo "$pane"
}
WT_PANE=$(shell_window wt "$S/repos/qa-wt")
LESS_PANE=$(shell_window less "$S/repos/qa-repo")
CLAUDE_PANE=
PI_PANE=

# pane_text prints the pane's current text.
pane_text() {
	tp capture-pane -p -t "$1" 2>/dev/null || true
}

# wait_text PANE REGEX SECONDS succeeds once the pane text matches REGEX.
wait_text() {
	local i text
	for ((i = 0; i < $3 * 2; i++)); do
		text=$(pane_text "$1")
		grep -qiE -- "$2" <<<"$text" && return 0
		sleep 0.5
	done
	return 1
}

if [ -n "$agents" ]; then
	ids=$(new_window claude "$S/repos/qa-repo" bwrap --die-with-parent --dev-bind / / \
		--bind "$S/home/.claude/projects" "$HOME/.claude/projects" \
		claude --model haiku --permission-mode default)
	read -r win CLAUDE_PANE <<<"$ids"
	tp set-option -w -t "$win" remain-on-exit failed

	ids=$(new_window pi "$S/repos/qa-repo" pi --model "$QA_PI_MODEL" --session-dir "$S/pi-sessions")
	read -r win PI_PANE <<<"$ids"
	tp set-option -w -t "$win" remain-on-exit failed

	# The real ~/.claude.json does not trust the scratch repo, and the dialog's
	# cursor starts on "No, exit".
	echo "== claude"
	ready=
	for ((i = 0; i < 120; i++)); do
		text=$(pane_text "$CLAUDE_PANE")
		if grep -q 'Yes, I trust' <<<"$text"; then
			if grep -qE '^ *❯ Yes' <<<"$text"; then
				tp send-keys -t "$CLAUDE_PANE" Enter
			else
				tp send-keys -t "$CLAUDE_PANE" Down
			fi
		elif grep -q 'Claude Code v[0-9]' <<<"$text" && grep -qE '^❯' <<<"$text"; then
			ready=1
			break
		fi
		sleep 0.5
	done
	[ -n "$ready" ] || {
		echo "claude did not reach its prompt in 60 s:" >&2
		echo "$text" >&2
		exit 1
	}

	# Proof that agent hooks land in $S/state and nowhere else: a state file for
	# this pane under the scratch repo must appear.
	tp send-keys -t "$CLAUDE_PANE" -l 'reply with only the word: ok'
	tp send-keys -t "$CLAUDE_PANE" Enter
	hooked=
	for ((i = 0; i < 90; i++)); do
		for f in "$S"/state/claude/*.json; do
			[ -f "$f" ] || continue
			if jq -e --arg d "$S/repos/" --arg p "$CLAUDE_PANE" \
				'((.cwd // "") | startswith($d)) and (.tmux_pane == $p)' "$f" >/dev/null 2>&1; then
				hooked=$f
			fi
		done
		[ -n "$hooked" ] && break
		sleep 1
	done
	[ -n "$hooked" ] || {
		echo "no claude hook state file for $CLAUDE_PANE appeared under $S/state within 90 s" >&2
		exit 1
	}

	text=$(pane_text "$PI_PANE")
	if grep -qi 'error' <<<"$text"; then
		echo "pi reported an error with model $QA_PI_MODEL:" >&2
		echo "$text" >&2
		exit 1
	fi
fi

echo "== houston"
qa_pick_port >"$S/port"
"$here/houston.sh" "$S" dispatcher
PORT=$(cat "$S/port")

echo "== playwright"
# npm ci enforces the integrity hashes in the committed lockfile.
mkdir -p -- "$S/pw"
cp -- "$here/pw/package.json" "$here/pw/package-lock.json" "$S/pw/"
npm ci --prefix "$S/pw" --no-audit --no-fund --silent
if [ -z "${CHROMIUM:-}" ]; then
	CHROMIUM=$(command -v chromium || true)
fi
if [ -z "$CHROMIUM" ]; then
	CHROMIUM=$(nix build --no-link --print-out-paths nixpkgs#chromium | tail -n 1)/bin/chromium
fi
[ -x "$CHROMIUM" ] || {
	echo "chromium not executable: $CHROMIUM" >&2
	exit 1
}

cat >"$S/qa.env" <<EOF
S=$S
PORT=$PORT
TMUX_TMPDIR=$S/tmux
NODE_PATH=$S/pw/node_modules
CHROMIUM=$CHROMIUM
CLAUDE_PANE=$CLAUDE_PANE
PI_PANE=$PI_PANE
WT_PANE=$WT_PANE
LESS_PANE=$LESS_PANE
CREW1=${crews[0]}
CREW2=${crews[1]}
EOF

ok=1
echo "PORT=$PORT"
echo "S=$S"
