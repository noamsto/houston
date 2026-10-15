#!/usr/bin/env bash
# Offline checks for the harness guards (no houston, no tmux, no agents):
#   scripts/qa/selftest.sh
# Each line is one check; exit 1 if any fails.
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd)
fails=0
ok() { echo "pass: $1"; }
bad() {
	echo "FAIL: $1"
	fails=$((fails + 1))
}

root=$(mktemp -d /tmp/hqa.XXXXXX)
other=$(mktemp -d /tmp/hqa.XXXXXX)
trap 'rm -rf "$root" "$other"; [ -z "${sleeper:-}" ] || kill "$sleeper" 2>/dev/null' EXIT

# H1: the approval guard needs the exact command on the detail's first line.
# shellcheck disable=SC2016
h1=$(node -e '
const sh = require(process.argv[1] + "/scenarios/_shared.cjs")
const f = "/tmp/hqa.x/perm-probe"
const dlg = (cmd) => `Bash command\nCreate the probe file\n${cmd}\nThis command requires approval`
const cases = [
  [dlg(`touch ${f}`), true],
  [dlg(`touch ${f} && rm -rf ~`), false],
  [dlg(`echo hi; touch ${f}`), false],
  [`touch ${f}\nsomething else\nThis command requires approval`, false],
]
for (const [d, want] of cases) if (sh.dialogIsFor({ detail: d }, f) !== want) { console.log("wrong for", JSON.stringify(d)); process.exit(1) }
' "$here")
if [ -z "$h1" ]; then ok "H1 dialogIsFor: exact command only"; else bad "H1 dialogIsFor: $h1"; fi

# H2: load() refuses a symlinked, group-readable or foreign-owned S.
mkdir -m 700 "$root/real"
touch "$root/.houstonqa"
chmod 755 "$root"
node_load() { S=$1 node -e 'try { require(process.argv[1] + "/lib.cjs").load([]) } catch (e) { console.log(e.message); process.exit(3) }' "$here" 2>&1; }
out=$(node_load "$root")
case $out in *"mode 700"*) ok "H2 load: mode 755 refused" ;; *) bad "H2 load: mode 755 accepted: $out" ;; esac
chmod 700 "$root"
ln -s "$root" "$other.link" 2>/dev/null
rm -rf "$other"
ln -s "$root" "$other"
out=$(node_load "$other")
case $out in *"mode 700"*) ok "H2 load: symlinked S refused" ;; *) bad "H2 load: symlinked S accepted: $out" ;; esac
rm -f "$other" "$other.link"

# H2: ctx.tmux refuses a tmux dir that resolves to a default socket dir.
mkdir -m 700 "$root/h2"
ln -s /tmp "$root/h2/tmux"
out=$(node -e '
const fs = require("fs")
const S = process.argv[1] + "/h2"
const lib = require(process.argv[2] + "/lib.cjs").createLib({ S, PORT: "17470" })
try { lib.tmux("list-sessions"); console.log("ran tmux") } catch (e) { console.log(e.message) }
' "$root" "$here" 2>&1)
case $out in *"default tmux socket dir"*) ok "H2 ctx.tmux: symlink to /tmp refused" ;; *) bad "H2 ctx.tmux: $out" ;; esac

# H3: a leftover whose cmdline quotes $S is found; /tmp/hqa.ab never matches /tmp/hqa.abc.
S=$root
# shellcheck source=/dev/null
source "$here/lib.sh"
set +e
sh -c "cd '$S'; sleep 30; :" &
sleeper=$!
sleep 0.3
if qa_pid_names_root "$sleeper"; then ok "H3 quoted \$S is seen"; else bad "H3 quoted \$S is invisible"; fi
S="${root}x"
if qa_pid_names_root "$sleeper"; then bad "H3 prefix path matched"; else ok "H3 prefix path does not match"; fi
S=$root

# H4: the token cache refuses an empty token and heals a stale header.
mkdir -p "$root/state"
: >"$root/state/token"
if qa_cache_auth; then bad "H4 empty token cached"; elif [ ! -e "$root/auth.hdr" ]; then ok "H4 empty token is not cached"; else bad "H4 auth.hdr written for an empty token"; fi
echo 'Authorization: Bearer ' >"$root/auth.hdr"
echo tok123 >"$root/state/token"
if qa_cache_auth && [ "$(cat "$root/auth.hdr")" = "Authorization: Bearer tok123" ]; then ok "H4 stale header replaced by the real token"; else bad "H4 stale header kept"; fi

exit $((fails > 0))
