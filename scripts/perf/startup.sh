#!/usr/bin/env bash
# Measures a houston binary's startup against a loadfixture dir. Runs its own
# instance on its own port and a copy of the fixture's state dir; never touches
# the real service. Every time is measured from process start:
#   - when every fixture session is listed (Fleet complete),
#   - when the largest run's chat first answers (and its cold and warm fetch
#     times), and when the last-scanned run's chat first has updates,
#   - CPU seconds and RSS at 10/30/60 s and the end, and when the CPU settles,
#   - the size and latency of a /api/runs snapshot once settled (the payload
#     the runs stream sends on every (re)connect),
#   - a reconnect storm: 10 rounds of 50 concurrent snapshots, wall time and
#     server CPU-seconds. With PPROF=<file> (binary built from a tree with
#     -debug pprof) a CPU profile of the storm is saved there.
#
#   go run -tags tools ./cmd/loadfixture -dir /tmp/fx
#   scripts/perf/startup.sh ./houston /tmp/fx 9391 [seconds] [mode]
set -euo pipefail

bin=$1 fx=$2 port=$3 secs=${4:-90} mode=${5:-tmux}
state=$(mktemp -d)
cp -r "$fx/state/." "$state/"
log=$(mktemp)

start=$(date +%s.%N)
debug=()
[ -n "${PPROF:-}" ] && debug=(-debug)
HOME="$fx/home" "$bin" -addr "127.0.0.1:$port" -status-dir "$state" -no-opencode -mode "$mode" "${debug[@]}" >"$log" 2>&1 &
pid=$!
trap 'kill "$pid" 2>/dev/null; wait "$pid" 2>/dev/null; rm -rf "$state" "$log"' EXIT INT TERM

elapsed() { awk -v a="$start" -v b="$(date +%s.%N)" 'BEGIN {printf "%.1f", b - a}'; }
hz=$(getconf CLK_TCK)
cpu() { awk -v hz="$hz" '{printf "%.2f", ($14 + $15) / hz}' "/proc/$pid/stat"; }
rss() { awk '/VmRSS/ {printf "%.0f", $2 / 1024}' "/proc/$pid/status"; }

for _ in $(seq 200); do
	[ -s "$state/token" ] && curl -s -o /dev/null "http://127.0.0.1:$port/" && break
	sleep 0.05
done
token=$(cat "$state/token")
get() { curl -s -H "Authorization: Bearer $token" "$@"; }

want=$(find "$state/claude" -name '*.json' | wc -l)
# Session 0 has the largest transcript and is scanned first; the last session
# is scanned last.
chat="http://127.0.0.1:$port/api/runs/sess-00000000-0000-4000-8000-000000000000/chat?limit=50"
last=$(printf 'http://127.0.0.1:%s/api/runs/sess-00000000-0000-4000-8000-%012d/chat?limit=50' "$port" $((want - 1)))
listed="" chatted="" lastchat="" settled="" prev=0 mark=0
declare -A shown
while :; do
	sleep 0.25
	t=$(elapsed)
	ti=${t%.*}
	if [ -z "$listed" ] && [ "$(get "http://127.0.0.1:$port/api/runs" | grep -o '"id":"sess-' | wc -l)" -ge "$want" ]; then
		listed=$t
	fi
	if [ -z "$chatted" ]; then
		read -r code took < <(get -o /dev/null -w '%{http_code} %{time_total}' "$chat") || true
		if [ "$code" = 200 ]; then
			chatted="$t (cold ${took}s, warm $(get -o /dev/null -w '%{time_total}' "$chat")s)"
		fi
	fi
	if [ -z "$lastchat" ] && [ "$(get "$last" | grep -c '"seq"')" -gt 0 ]; then
		lastchat=$t
	fi
	for at in 10 30 60 "$secs"; do
		if [ "$ti" -ge "$at" ] && [ -z "${shown[$at]:-}" ]; then
			shown[$at]=1
			echo "t=${at}s cpu=$(cpu)s rss=$(rss)MB"
		fi
	done
	# Settled: a 5 s window that used under 0.25 CPU-seconds.
	if [ -z "$settled" ] && [ "$ti" -ge $((mark + 5)) ]; then
		c=$(cpu)
		if [ "$mark" -gt 0 ] && awk -v c="$c" -v p="$prev" 'BEGIN {exit !(c - p < 0.25)}'; then settled=$ti; fi
		prev=$c mark=$ti
	fi
	[ "$ti" -ge "$secs" ] && break
done
echo "all $want sessions listed after: ${listed:-never}s"
echo "largest run's chat answered after: ${chatted:-never}s"
echo "last-scanned run's chat had updates after: ${lastchat:-never}s"
echo "cpu settled by: ${settled:-never}s"
echo "snapshot: $(get -o /dev/null -w '%{size_download} bytes in %{time_total}s' "http://127.0.0.1:$port/api/runs")"

prof=""
if [ -n "${PPROF:-}" ]; then
	get -o "$PPROF" "http://127.0.0.1:$port/api/debug/pprof/profile?seconds=5" &
	prof=$!
	sleep 0.2
fi
c0=$(cpu) w0=$(date +%s.%N)
for _ in $(seq 10); do
	pids=()
	for _ in $(seq 50); do
		get -o /dev/null "http://127.0.0.1:$port/api/runs" &
		pids+=($!)
	done
	wait "${pids[@]}"
done
echo "storm (10x50 snapshots): $(awk -v a="$w0" -v b="$(date +%s.%N)" 'BEGIN {printf "%.2f", b - a}')s wall, $(awk -v a="$c0" -v b="$(cpu)" 'BEGIN {printf "%.2f", b - a}') CPU-s"
[ -n "$prof" ] && wait "$prof" && echo "storm profile: $PPROF"
