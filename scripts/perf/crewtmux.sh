#!/usr/bin/env bash
# Starts or stops a private tmux server holding the dispatcher and worker
# windows of a loadfixture crew fixture (cmd/loadfixture -crews N -workers M),
# so houston -mode dispatcher can be measured against it.
#
#   crewtmux.sh start FX WORKDIR   windows per FX/crews.tsv, pane/pid files under FX/repo/.git/crew/crews/
#   crewtmux.sh stop WORKDIR       kills that private server only
#
# On success `start` prints TMUX_TMPDIR=<WORKDIR>/tmux: run houston with that
# variable (and without TMUX/TMUX_PANE) so it lists this server.
#
# Every tmux call goes through tp: with $TMUX set tmux ignores TMUX_TMPDIR and
# would act on the caller's live server, so TMUX and TMUX_PANE are unset and
# the socket dir is refused when it could be the caller's default one.
set -euo pipefail

usage() {
	echo "usage: $0 start FX WORKDIR | $0 stop WORKDIR" >&2
	exit 2
}

sock="" ok=""
tp() { env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$sock" tmux -f /dev/null "$@"; }

# prepare WORKDIR sets $sock to <WORKDIR>/tmux after refusing any dir whose
# socket directory (<sock>/tmux-<uid>) is the caller's default.
prepare() {
	local work=$1
	[ -n "$work" ] || {
		echo "WORKDIR is empty" >&2
		exit 2
	}
	work=$(realpath -m -- "$work")
	local uid want d
	uid=$(id -u)
	want=$(realpath -m -- "$work/tmux/tmux-$uid")
	for d in "/tmp/tmux-$uid" "${TMUX_TMPDIR:-/tmp}/tmux-$uid"; do
		if [ "$want" = "$(realpath -m -- "$d")" ]; then
			echo "refusing: $want is the default tmux socket dir" >&2
			exit 2
		fi
	done
	sock=$work/tmux
	# sun_path holds about 108 bytes; tmux would fail with "File name too long".
	[ ${#sock} -le 80 ] || {
		echo "WORKDIR is too long for a unix socket path: $sock" >&2
		exit 2
	}
	mkdir -p -- "$sock"
	chmod 700 -- "$sock"
}

start() {
	local fx work
	fx=$(realpath -- "$1")
	prepare "$2"
	work=$(realpath -m -- "$2")
	local bus=$fx/repo/.git/crew
	[ -f "$fx/crews.tsv" ] || {
		echo "no $fx/crews.tsv: run loadfixture with -crews" >&2
		exit 1
	}
	if tp has-session -t fx 2>/dev/null; then
		echo "private server in $work is already running; stop it first" >&2
		exit 1
	fi

	trap '[ -n "$ok" ] || tp kill-server 2>/dev/null || true' EXIT
	tp new-session -d -s fx -n keepalive 'sleep 100000'

	local now n=0 kind crew branch name state wname ids win pane panepid
	now=$(date +%s)
	while IFS=$'\t' read -r kind crew branch name state; do
		n=$((n + 1))
		case $kind in
		dispatcher) wname=dispatcher-$crew ;;
		worker) wname=$name ;;
		*) continue ;;
		esac
		ids=$(tp new-window -d -t fx: -n "$wname" -P -F '#{window_id} #{pane_id} #{pane_pid}' 'sleep 100000')
		read -r win pane panepid <<<"$ids"
		tp set-option -w -t "$win" @git_root "$fx/repo"
		if [ "$kind" = dispatcher ]; then
			tp set-option -w -t "$win" @crew_name dispatcher
			tp set-option -p -t "$pane" @claude_status "idle $now"
			# Written after the window exists so both mtimes postdate the server start.
			mkdir -p -- "$bus/crews/$crew"
			printf '%s\n' "$pane" >"$bus/crews/$crew/pane"
			printf '%s\n' "$panepid" >"$bus/crews/$crew/pid"
			continue
		fi
		tp set-option -w -t "$win" @crew_name "$name"
		tp set-option -w -t "$win" @crew_color "colour$((17 + n % 200))"
		tp set-option -w -t "$win" @branch "$branch"
		# A blocked worker is waiting on its dispatcher, not idle.
		if [ "$state" = blocked ]; then
			tp set-option -p -t "$pane" @claude_status "processing $now"
		else
			tp set-option -p -t "$pane" @claude_status "idle $now"
		fi
	done <"$fx/crews.tsv"

	ok=1
	echo "TMUX_TMPDIR=$work/tmux"
}

case ${1:-} in
start)
	[ $# -eq 3 ] || usage
	start "$2" "$3"
	;;
stop)
	[ $# -eq 2 ] || usage
	prepare "$2"
	tp kill-server 2>/dev/null || true
	;;
*) usage ;;
esac
