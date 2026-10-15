#!/usr/bin/env bash
# Starts, restarts or stops the houston under test.
#
#   houston.sh S dispatcher|tmux   stop a running instance, then start one in that mode
#   houston.sh S restart           same mode as the running one
#   houston.sh S stop              stop it
#
# S is the scratch dir setup.sh made. The instance listens on 127.0.0.1:<$S/port>
# (a port from 17470-17499, never 9090), keeps its state in $S/state and reaches
# only the private tmux server in $S/tmux. Pid file $S/houston.pid, log
# $S/houston.log, last mode $S/houston.mode.
#
# houston runs inside the same bwrap bind as the QA claude: a hook's
# transcript_path is $HOME/.claude/projects/..., which only resolves to the
# scratch projects dir inside the bind, and houston's own transcript discovery
# then never reads the user's real sessions. There is no --die-with-parent,
# since houston must outlive this script; the pid file holds bwrap's pid and
# stopping signals its whole process group.
#
# A start that replaces a running instance waits for the old group to exit and
# the port to be free, so the readiness check cannot be answered by the dying
# instance.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=/dev/null
. "$here/lib.sh"

usage() {
	echo "usage: $0 S dispatcher|tmux|restart|stop" >&2
	exit 2
}

[ $# -eq 2 ] || usage
S=$1
action=$2
qa_check_root
qa_scrub_env

pidfile=$S/houston.pid
modefile=$S/houston.mode

stop() {
	local port i
	qa_pidfile_kill "$pidfile"
	[ -f "$S/port" ] || return 0
	port=$(cat "$S/port")
	qa_check_port "$port"
	for ((i = 0; i < 100; i++)); do
		qa_port_in_use "$port" || return 0
		sleep 0.1
	done
	echo "port $port is still in use after stopping houston" >&2
	return 1
}

start() {
	local mode=$1 port pid i
	[ -f "$S/port" ] && [ -x "$S/houston" ] && [ -d "$S/tmux" ] || {
		echo "$S is not set up (need port, houston binary and tmux dir)" >&2
		exit 1
	}
	port=$(cat "$S/port")
	qa_check_port "$port"
	stop
	TMUX_TMPDIR="$S/tmux" setsid bwrap --dev-bind / / \
		--bind "$S/home/.claude/projects" "$HOME/.claude/projects" \
		"$S/houston" -status-dir "$S/state" -repo-root "$S/repos" \
		-addr "127.0.0.1:$port" -mode "$mode" -no-opencode \
		</dev/null >>"$S/houston.log" 2>&1 &
	pid=$!
	# The forked child has no cmdline for a moment while it execs.
	for ((i = 0; i < 50; i++)); do
		tr '\0' ' ' <"/proc/$pid/cmdline" 2>/dev/null | grep -qF -- "$S/houston " && break
		sleep 0.1
	done
	if [ "$i" = 50 ]; then
		echo "pid $pid is not the houston under test; stopping its group" >&2
		kill -TERM -- "-$pid" 2>/dev/null || true
		exit 1
	fi
	echo "$pid" >"$pidfile"
	echo "$mode" >"$modefile"

	# The token file appears once the new instance has started.
	for ((i = 0; i < 100; i++)); do
		kill -0 "$pid" 2>/dev/null || {
			echo "houston exited during startup; tail of $S/houston.log:" >&2
			tail -n 5 "$S/houston.log" >&2
			exit 1
		}
		if qa_cache_auth; then
			if [ "$(curl -fsS -H "@$S/auth.hdr" "http://127.0.0.1:$port/api/mode" 2>/dev/null || true)" = "{\"mode\":\"$mode\"}" ]; then
				echo "houston $mode on 127.0.0.1:$port (pid $pid)"
				return 0
			fi
		fi
		sleep 0.1
	done
	echo "houston did not answer /api/mode with $mode within 10 s; tail of $S/houston.log:" >&2
	tail -n 5 "$S/houston.log" >&2
	exit 1
}

case $action in
dispatcher | tmux) start "$action" ;;
restart)
	[ -f "$modefile" ] || {
		echo "no recorded mode in $modefile; start it with dispatcher or tmux" >&2
		exit 1
	}
	start "$(cat "$modefile")"
	;;
stop) stop ;;
*) usage ;;
esac
