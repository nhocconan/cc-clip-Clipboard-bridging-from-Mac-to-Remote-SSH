#!/usr/bin/env bash
# Reconnect a screen attachment without leaving a background reconnect loop.
set -u

host="${1:-}"
screen_name="${2:-claude-${host}}"
if [[ $# -lt 1 || $# -gt 2 || ! "$host" =~ ^[A-Za-z0-9_][A-Za-z0-9._-]*$ || ! "$screen_name" =~ ^[A-Za-z0-9_][A-Za-z0-9._-]*$ ]]; then
  echo "Usage: $0 host [screen-name] (letters, numbers, dots, underscores and hyphens only)" >&2
  exit 2
fi

forward_wait_seconds="${CC_CLIP_FORWARD_WAIT_SECONDS:-20}"
if [[ ! "$forward_wait_seconds" =~ ^[0-9]+$ || ${#forward_wait_seconds} -gt 5 ]]; then
  echo "CC_CLIP_FORWARD_WAIT_SECONDS must be an integer from 0 to 99999." >&2
  exit 2
fi
forward_wait_seconds=$((10#$forward_wait_seconds))

# A common option set applies even when Host * enables backgrounding or mux.
ssh_options=(-o ConnectTimeout=8 -o ConnectionAttempts=1
  -o ServerAliveInterval=5 -o ServerAliveCountMax=2
  -o ControlMaster=no -o ControlPath=none -o ControlPersist=no
  -o ForkAfterAuthentication=no -o StdinNull=no -o SessionType=default -o RemoteCommand=none)
config=$(ssh -G "${ssh_options[@]}" -- "$host") || exit 1
forward_count=$(printf '%s\n' "$config" | awk '$1 == "remoteforward" { n++ } END { print n+0 }')
[[ "$forward_count" == 1 ]] || { echo "Expected exactly one clipboard RemoteForward for $host." >&2; exit 2; }
remote_listen=$(printf '%s\n' "$config" | awk '$1 == "remoteforward" { print $2; exit }')
case "$remote_listen" in
  *:*)
    [[ "$remote_listen" == 127.0.0.1:* || "$remote_listen" == '[127.0.0.1]:'* || "$remote_listen" == localhost:* ]] || {
      echo "RemoteForward must listen on IPv4 loopback for $host." >&2; exit 2;
    } ;;
esac
remote_port=${remote_listen##*:}
if [[ ! "$remote_port" =~ ^[0-9]+$ || ${#remote_port} -gt 5 ]] || (( 10#$remote_port < 1 || 10#$remote_port > 65535 )); then
  echo "Expected a numeric RemoteForward listen port in SSH config for $host." >&2
  exit 2
fi

# Stable workstation ID scopes remote leases across host aliases, never across
# computers. Hard-link creation publishes a complete ID atomically.
owner_root="${XDG_CACHE_HOME:-$HOME/.cache}/cc-clip"
umask 077
mkdir -p "$owner_root" || exit 1
owner_file="$owner_root/screen-owner-id"
if [[ ! -f "$owner_file" ]]; then
  owner_tmp=$(mktemp "$owner_root/owner.XXXXXX") || exit 1
  od -An -N16 -tx1 /dev/urandom | tr -d ' \n' > "$owner_tmp"
  ln "$owner_tmp" "$owner_file" 2>/dev/null || true
  rm -f "$owner_tmp"
fi
owner_id=$(cat "$owner_file")
[[ "$owner_id" =~ ^[0-9a-f]{32}$ ]] || { echo "Invalid workstation ID: $owner_file" >&2; exit 1; }

lock_root="${XDG_CACHE_HOME:-$HOME/.cache}/cc-clip/screen-locks"
mkdir -p "$lock_root" || exit 1
lock="$lock_root/${host}-${remote_port}"
# Kernel-backed flock on Linux; shlock on macOS atomically checks/replaces
# dead PID locks. Neither leaves a separate acquisition directory after SIGKILL.
# Keep the legacy directory path untouched so old wrappers remain detectable.
if [[ -d "$lock" ]]; then
  owner=$(cat "$lock/pid" 2>/dev/null || true)
  if [[ "$owner" =~ ^[0-9]+$ ]] && ! kill -0 "$owner" 2>/dev/null; then
    rm -f "$lock/pid" "$lock/probe"
    rmdir "$lock" 2>/dev/null || exit 1
  else
    echo "A reconnect wrapper already owns $host:$remote_port (PID ${owner:-unknown})." >&2
    exit 1
  fi
fi
if command -v flock >/dev/null 2>&1; then
  # Do not unlink a flock inode: waiters may already have it open.
  exec 9>"${lock}.flock"
  flock -n 9 || { echo "A reconnect wrapper already owns $host:$remote_port." >&2; exit 1; }
  lock_kind=flock
elif command -v shlock >/dev/null 2>&1; then
  shlock -p "$$" -f "${lock}.pid" || { echo "A reconnect wrapper already owns $host:$remote_port." >&2; exit 1; }
  lock_kind=shlock
else
  echo "Install flock (util-linux) or shlock to use this reconnect wrapper." >&2
  exit 1
fi
work=$(mktemp -d "$lock_root/probe.XXXXXX") || exit 1
child=
timer=
cleanup() {
  trap '' HUP INT TERM
  if [[ -n "$timer" ]]; then
    stop_child "$timer"
  fi
  if [[ -n "$child" ]]; then
    stop_child "$child"
  fi
  rm -f "$work/probe"
  rmdir "$work" 2>/dev/null || true
  if [[ "$lock_kind" == shlock && $(cat "${lock}.pid" 2>/dev/null) == "$$" ]]; then
    rm -f "${lock}.pid"
  fi
}
stop_child() {
  local pid="$1" n
  kill -TERM "$pid" 2>/dev/null || true
  for (( n=0; n<10; n++ )); do
    kill -0 "$pid" 2>/dev/null || break
    sleep 0.1
  done
  kill -KILL "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

# This POSIX payload also runs under fish logins through an explicit sh -c.
# BEGIN REMOTE GUARD
remote_script=
IFS= read -r -d '' remote_script <<'REMOTE' || true
set -u
mode=$1 port=$2 owner=$3 screen_name=$4
uid=$(id -u)
lease_dir="$HOME/.cache/cc-clip/screen-tunnels"
lease="$lease_dir/$owner-$port"
# Linux /proc start ticks distinguish a reused PID from our original process.
# The fallback deliberately disables recovery, rather than guessing ownership.
ticks() {
  [ -r "/proc/$1/stat" ] || return 1
  sed 's/^[^)]*) //' "/proc/$1/stat" | awk '{print $20}'
}
session_matches() {
  case "$1:$2" in *[!0-9:]*|:*|*:) return 1 ;; esac
  [ "$1" -gt 1 ] 2>/dev/null || return 1
  [ "$(ticks "$1")" = "$2" ] || return 1
  [ "$(ps -o uid= -p "$1" | tr -d ' ')" = "$uid" ] || return 1
  case "$(cat "/proc/$1/comm" 2>/dev/null)" in sshd|sshd-session) return 0 ;; *) return 1 ;; esac
}
healthy() {
  curl --noproxy '*' -fsS --connect-timeout 1 --max-time 2 "http://127.0.0.1:$port/health" 2>/dev/null |
    grep -Eq '"service"[[:space:]]*:[[:space:]]*"cc-clip"'
}
if [ "$mode" = probe ]; then
  if healthy; then echo cc-clip-forward-healthy; exit; fi
  # Recover only a lease written by this workstation for this exact port.
  # A failed public health probe alone never authorizes a process kill.
  if [ -f "$lease" ]; then
    read -r pid born boot < "$lease" || true
    if [ -n "${boot:-}" ] && [ "$boot" = "$(cat /proc/sys/kernel/random/boot_id 2>/dev/null)" ] && session_matches "${pid:-}" "${born:-}"; then
      if kill -TERM "$pid" 2>/dev/null; then
        echo cc-clip-forward-released
        exit
      fi
    fi
  fi
  if command -v ss >/dev/null 2>&1; then
    if ! listeners=$(ss -H -ltn "sport = :$port" 2>/dev/null); then echo cc-clip-forward-unknown
    elif [ -n "$listeners" ]; then echo cc-clip-forward-held
    else echo cc-clip-forward-free; fi
  else echo cc-clip-forward-unknown; fi
  exit
fi
# Find only our own unprivileged SSH session; newer OpenSSH calls it sshd-session.
session=$PPID
while [ "$session" -gt 1 ] 2>/dev/null; do
  case "$(cat "/proc/$session/comm" 2>/dev/null)" in sshd|sshd-session) break ;; esac
  session=$(ps -o ppid= -p "$session" | tr -d ' ')
done
born=$(ticks "$session" 2>/dev/null || true)
if ! session_matches "$session" "$born"; then
  echo 'Cannot start the Linux SSH session watchdog; refusing an unguarded forward.' >&2
  exit 2
fi
umask 077
mkdir -p "$lease_dir" || exit 2
# Only the connection which created the forward publishes its ownership.
if [ "$mode" = own ]; then
  tmp="$lease.$$"
  boot=$(cat /proc/sys/kernel/random/boot_id) || exit 2
  printf '%s %s %s\n' "$session" "$born" "$boot" > "$tmp" && mv "$tmp" "$lease" || exit 2
fi
main=$$
main_born=$(ticks "$main")
(
  # Remain alive after a local client dies; never inherit its terminal/stdio.
  trap '' HUP
  failures=0
  while session_matches "$session" "$born" && [ "$(ticks "$main")" = "$main_born" ]; do
    sleep 3
    if healthy; then failures=0; else failures=$((failures + 1)); fi
    if [ "$failures" -ge 3 ]; then
      # Killing our exact sshd closes its listeners even when TCP is blackholed.
      session_matches "$session" "$born" && kill -TERM "$session"
      exit
    fi
  done
) </dev/null >/dev/null 2>&1 &
watcher=$!
finish() {
  kill "$watcher" 2>/dev/null || true
  if [ "$mode" = own ] && [ "$(cat "$lease" 2>/dev/null)" = "$session $born $boot" ]; then rm -f "$lease"; fi
}
trap finish EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
screen -D -RR "$screen_name"
REMOTE
# END REMOTE GUARD
# Single-quote the payload for the remote login shell; identifiers are validated.
remote_quoted=${remote_script//\'/\'\\\'\'}
remote_command() {
  printf "sh -c '%s' sh %s %s %s %s" "$remote_quoted" "$1" "$remote_port" "$owner_id" "$screen_name"
}

# Run probes as children so terminal closure stops blocked SSH probes too.
probe_file="$work/probe"
held_since=
while true; do
  ssh -T "${ssh_options[@]}" -o ClearAllForwardings=yes -- "$host" \
    "$(remote_command probe)" > "$probe_file" <&0 9>&- &
  child=$!
  # ConnectTimeout does not bound a stalled remote command or a ProxyCommand.
  # The watchdog is reaped before a PID can be reused for another child.
  (
    # The lock belongs only to the wrapper, never a child or an orphan sleep.
    exec 9>&-
    sleeper=
    trap 'kill "$sleeper" 2>/dev/null || true; wait "$sleeper" 2>/dev/null || true; exit' HUP INT TERM
    sleep 15 &
    sleeper=$!
    wait "$sleeper"
    if [[ $(ps -o ppid= -p "$child" | tr -d ' ') == "$$" ]]; then
      kill -TERM "$child" 2>/dev/null || true
      sleep 1 &
      sleeper=$!
      wait "$sleeper"
      [[ $(ps -o ppid= -p "$child" | tr -d ' ') != "$$" ]] || kill -KILL "$child" 2>/dev/null
    fi
  ) </dev/null >/dev/null 2>&1 &
  timer=$!
  wait "$child"
  probe_rc=$?
  kill "$timer" 2>/dev/null || true
  wait "$timer" 2>/dev/null || true
  timer=
  child=
  state=$(cat "$probe_file")
  rm -f "$probe_file"
  forward_option=ClearAllForwardings=no
  guard_mode=own
  if [[ $probe_rc -eq 0 ]]; then
    case "$state" in
      *cc-clip-forward-healthy*)
        held_since=
        echo "Reusing the healthy clipboard tunnel on $host:$remote_port." >&2
        forward_option=ClearAllForwardings=yes
        guard_mode=reuse
        ;;
      *cc-clip-forward-released*)
        echo "Released this workstation's stale clipboard SSH session on $host:$remote_port; reconnecting…" >&2
        # Wait for the kernel to close both IPv4 and IPv6 listening sockets.
        sleep 1 9>&- &
        child=$!
        wait "$child"
        child=
        continue
        ;;
      *cc-clip-forward-held*)
        if [[ -z "$held_since" ]]; then
          held_since=$SECONDS
          echo "Waiting for an unowned clipboard listener on $host:$remote_port (up to ${forward_wait_seconds}s)…" >&2
        fi
        if (( SECONDS - held_since < forward_wait_seconds )); then
          sleep 1 9>&- &
          child=$!
          wait "$child"
          child=
          continue
        fi
        echo "Port $remote_port on $host is still occupied; no healthy cc-clip tunnel answered." >&2
        echo "Run: cc-clip doctor --host $host" >&2
        echo "This listener has no verified workstation lease; identify its owner before ending it." >&2
        exit 1
        ;;
      *cc-clip-forward-unknown*)
        echo "Port $remote_port on $host is occupied or cannot be checked; no healthy cc-clip tunnel answered." >&2
        echo "Run: cc-clip doctor --host $host" >&2
        exit 1
        ;;
      *cc-clip-forward-free*) held_since= ;;
      *) echo "Unexpected tunnel probe response from $host: $state" >&2; exit 1 ;;
    esac
    echo "Connecting to $host; remote screen: $screen_name" >&2
    ssh -t "${ssh_options[@]}" -o TCPKeepAlive=yes -o ExitOnForwardFailure=yes \
      -o "$forward_option" -- "$host" "$(remote_command "$guard_mode")" <&0 9>&- &
    child=$!
    wait "$child"
    rc=$?
    child=
    [[ $rc -eq 255 || $rc -eq 129 || $rc -eq 143 ]] || exit "$rc"
  fi
  echo "Connection failed; retrying in 3 seconds…" >&2
  sleep 3 9>&- &
  child=$!
  wait "$child"
  child=
done
