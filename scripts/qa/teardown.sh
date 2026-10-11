#!/usr/bin/env bash
# Stops everything setup.sh started and proves nothing is left.
#
#   teardown.sh [--purge] S
#
# Stops the houston under test, kills the private tmux server (only one carrying
# crewtmux's @crewtmux marker), then checks that no tmux server answers on
# $S/tmux and that no process names $S in its command line or has
# HOUSTON_STATE_DIR=$S/state in its environment (a process an agent started in
# a pane inherits that). Exit 1 lists what is left, or reports that houston
# would not stop (the tmux kill and the scan still run first). The check skips
# this script, its descendants and its ancestors, whose command lines
# legitimately name $S.
# --purge also moves $S to the trash. Touches nothing outside $S.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
# shellcheck source=/dev/null
. "$here/lib.sh"

purge=
S=
for a in "$@"; do
	case $a in
	--purge) purge=1 ;;
	*) S=$a ;;
	esac
done
[ -n "$S" ] || {
	echo "usage: $0 [--purge] S" >&2
	exit 2
}
qa_check_root
qa_scrub_env

# A failed stop (port still busy) must not leave the private tmux server running.
rc=0
"$here/houston.sh" "$S" stop || rc=1

if [ -d "$S/tmux" ] && tp ls >/dev/null 2>&1; then
	"$root/scripts/perf/crewtmux.sh" stop "$S"
fi

# Processes need a moment to see their pane close.
i=0
sock=$S/tmux/tmux-$(id -u)/default
until ! { tp ls >/dev/null 2>&1; } && ! ss -xl 2>/dev/null | grep -qF -- "$sock"; do
	i=$((i + 1))
	[ $i -lt 50 ] || {
		echo "a tmux server still answers on $S/tmux" >&2
		exit 1
	}
	sleep 0.1
done

# is_self_family succeeds for this script, its descendants and its ancestors.
is_self_family() {
	local p=$1 q
	q=$$
	while [ "$q" -gt 1 ]; do
		[ "$q" = "$p" ] && return 0
		q=$(qa_ppid "$q") || break
	done
	q=$p
	while [ "$q" -gt 1 ]; do
		[ "$q" = "$$" ] && return 0
		q=$(qa_ppid "$q") || break
	done
	return 1
}

left=
for ((i = 0; i < 100; i++)); do
	left=
	for d in /proc/[0-9]*; do
		pid=${d#/proc/}
		[ -O "$d" ] || continue
		reason=
		if qa_pid_names_root "$pid"; then
			reason=cmdline
		elif { tr '\0' '\n' <"$d/environ" | grep -qxF "HOUSTON_STATE_DIR=$S/state"; } 2>/dev/null; then
			reason=environ
		fi
		[ -n "$reason" ] || continue
		is_self_family "$pid" && continue
		left+="$pid ($reason): $(tr '\0' ' ' <"$d/cmdline" | cut -c1-160)"$'\n'
	done
	[ -z "$left" ] && break
	sleep 0.1
done
if [ -n "$left" ]; then
	printf 'processes still tied to %s:\n%s' "$S" "$left" >&2
	exit 1
fi
if [ "$rc" != 0 ]; then
	echo "houston could not be stopped; $S kept" >&2
	exit 1
fi

# gtrash cannot trash across filesystems (no /.Trash-<uid> for /tmp), so a
# failed put falls back to removing the validated /tmp/hqa.* scratch dir.
if [ -n "$purge" ]; then
	gtrash put "$S" 2>/dev/null || rm -rf -- "$S"
fi
echo "teardown of $S complete"
