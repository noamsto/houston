#!/usr/bin/env bash
# Helpers sourced by the scripts/qa/*.sh harness scripts. Not run directly.
#
# The harness runs real agents next to the user's live houston (127.0.0.1:9090,
# ~/.local/state/houston) and live tmux server, so every helper here exists to
# keep the two apart:
#
#   qa_check_root     refuses any $S that is not a marked /tmp/hqa.* scratch dir
#                     (real dir, yours, mode 700), so a typo can never aim a kill
#                     or a purge at another path
#   qa_scrub_env      unsets the crew and Claude session variables of the calling
#                     agent: an agent started with them would post to the real
#                     crew bus (hookyard's dispatch-notify) or think it is nested
#   tp                the only way to reach tmux: the private server in $S/tmux
#   qa_pick_port      a free port in 17470-17499, never 9090
#   qa_pidfile_kill   stops a process group, but only one that names $S
set -euo pipefail

QA_PORT_MIN=17470
QA_PORT_MAX=17499

# qa_check_dir requires an existing $S to be a real directory (not a symlink)
# that the caller owns with mode 700, so another local user cannot plant it.
qa_check_dir() {
	{ [ -d "$S" ] && [ ! -L "$S" ] && [ -O "$S" ] && [ "$(stat -c %a -- "$S")" = 700 ]; } || {
		echo "refusing: $S must be a real directory owned by you with mode 700" >&2
		return 2
	}
}

# qa_check_root requires $S to be an existing /tmp/hqa.<random> dir that setup.sh
# marked with .houstonqa.
qa_check_root() {
	[[ ${S:-} =~ ^/tmp/hqa\.[A-Za-z0-9]+$ ]] || {
		echo "refusing: S must be a /tmp/hqa.<random> dir, got: ${S:-}" >&2
		return 2
	}
	qa_check_dir || return 2
	[ -f "$S/.houstonqa" ] || {
		echo "refusing: $S is not a QA scratch dir (no .houstonqa marker)" >&2
		return 2
	}
}

# qa_scrub_env drops the variables that tie an agent to the crew it runs in.
qa_scrub_env() {
	local name
	# Names only, and through export -p because minimal bashes lack compgen.
	while IFS= read -r name; do
		unset "$name"
	done < <(export -p | sed -n 's/^declare -x \([A-Za-z_][A-Za-z0-9_]*\).*/\1/p' |
		grep -E '^(CREW_|DISPATCH_|DISPATCHER_|CLAUDE_CODE_|CLAUDECODE$|CLAUDE_PID$|CLAUDE_EFFORT$|TMUX$|TMUX_PANE$)' || true)
}

# tp runs tmux against the private server only. With $TMUX set tmux ignores
# TMUX_TMPDIR and would act on the caller's live server, so TMUX and TMUX_PANE
# are unset; tmux 3.2+ also falls back to /tmp when TMUX_TMPDIR is not an
# existing directory, so tp refuses unless $S/tmux exists. It also refuses a
# socket dir that is the caller's default one.
tp() {
	qa_check_root || return 2
	local sock=$S/tmux uid want d
	[ -d "$sock" ] || {
		echo "tmux socket dir is missing: $sock" >&2
		return 2
	}
	uid=$(id -u)
	want=$(realpath -m -- "$sock/tmux-$uid")
	for d in "/tmp/tmux-$uid" "${TMUX_TMPDIR:-/tmp}/tmux-$uid"; do
		if [ "$want" = "$(realpath -m -- "$d")" ]; then
			echo "refusing: $want is the default tmux socket dir" >&2
			return 2
		fi
	done
	env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$sock" tmux -f /dev/null "$@"
}

# qa_port_in_use succeeds when something accepts connections on 127.0.0.1:$1.
qa_port_in_use() {
	(exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null
}

# qa_check_port refuses any port outside the QA range, which excludes 9090.
qa_check_port() {
	[[ ${1:-} =~ ^[0-9]+$ ]] && [ "$1" -ge "$QA_PORT_MIN" ] && [ "$1" -le "$QA_PORT_MAX" ] || {
		echo "refusing: port ${1:-} is outside $QA_PORT_MIN-$QA_PORT_MAX" >&2
		return 2
	}
}

# qa_pick_port prints the first unused port in the QA range.
qa_pick_port() {
	local p
	for ((p = QA_PORT_MIN; p <= QA_PORT_MAX; p++)); do
		qa_port_in_use "$p" || {
			echo "$p"
			return 0
		}
	done
	echo "no free port in $QA_PORT_MIN-$QA_PORT_MAX" >&2
	return 1
}

# qa_pid_names_root succeeds when /proc/$1/cmdline mentions $S as a whole path:
# /tmp/hqa.ab must not match /tmp/hqa.abc (mktemp suffixes are alphanumeric, so any
# other character ends the path, a quote included).
qa_pid_names_root() {
	local cmd
	[ -r "/proc/$1/cmdline" ] || return 1
	cmd=$(tr '\0' ' ' <"/proc/$1/cmdline" 2>/dev/null) || return 1
	[[ $cmd =~ (^|[^A-Za-z0-9])"$S"([^A-Za-z0-9]|$) ]]
}

# qa_cache_auth writes $S/auth.hdr from $S/state/token and fails while the token is
# still empty: houston truncates the file before writing it, and an empty token
# cached once would fail every later start on this $S. The token goes in a 0600
# header file because argv is readable by every local uid.
qa_cache_auth() {
	local token
	[ -s "$S/state/token" ] || return 1
	token=$(cat "$S/state/token")
	[ -n "$token" ] || return 1
	(umask 077 && printf 'Authorization: Bearer %s\n' "$token" >"$S/auth.hdr")
}

# qa_ppid prints the parent pid of $1. The comm field may hold spaces and
# parentheses, so everything up to the last ")" is dropped first.
qa_ppid() {
	local stat rest
	stat=$(cat "/proc/$1/stat" 2>/dev/null) || return 1
	rest=${stat##*) }
	read -ra rest <<<"$rest"
	echo "${rest[1]}"
}

# qa_pgid prints the process group of $1.
qa_pgid() {
	local stat rest
	stat=$(cat "/proc/$1/stat" 2>/dev/null) || return 1
	rest=${stat##*) }
	read -ra rest <<<"$rest"
	echo "${rest[2]}"
}

# qa_pidfile_kill FILE stops the process group whose leader pid FILE holds, when
# that process names $S, and waits for the whole group to exit (TERM, then KILL
# after 10 s). The pid is trusted only after the cmdline check: pids are
# recycled, and a stale file must never aim a signal at an unrelated process.
qa_pidfile_kill() {
	local file=$1 pid i
	[ -f "$file" ] || return 0
	pid=$(cat "$file")
	if [[ $pid =~ ^[0-9]+$ ]] && qa_pid_names_root "$pid" && [ "$(qa_pgid "$pid")" = "$pid" ]; then
		kill -TERM -- "-$pid" 2>/dev/null || true
		for ((i = 0; i < 100; i++)); do
			kill -0 -- "-$pid" 2>/dev/null || break
			sleep 0.1
		done
		if kill -0 -- "-$pid" 2>/dev/null; then
			kill -KILL -- "-$pid" 2>/dev/null || true
			sleep 0.2
		fi
	else
		echo "pid file $file does not name a live process group of $S; not signalling" >&2
	fi
	rm -f -- "$file"
}
