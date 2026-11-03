#!/usr/bin/env bash
# Reconnect a screen attachment without leaving a background reconnect loop.
set -u

host="${1:-}"
screen_name="${2:-claude-${host}}"
if [[ $# -lt 1 || $# -gt 2 || ! "$host" =~ ^[A-Za-z0-9._-]+$ || ! "$screen_name" =~ ^[A-Za-z0-9._-]+$ ]]; then
  echo "Usage: $0 host [screen-name] (letters, numbers, dots, underscores and hyphens only)" >&2
  exit 2
fi

config=$(ssh -G "$host") || exit 1
remote_port=$(printf '%s\n' "$config" | awk '$1 == "remoteforward" { print $2; exit }')
if [[ ! "$remote_port" =~ ^[0-9]+$ ]]; then
  echo "Expected a numeric RemoteForward listen port in SSH config for $host." >&2
  exit 2
fi

lock_root="${XDG_CACHE_HOME:-$HOME/.cache}/cc-clip/screen-locks"
mkdir -p "$lock_root" || exit 1
lock="$lock_root/${host}-${remote_port}"
guard="${lock}.acquire"
if ! mkdir "$guard" 2>/dev/null; then
  echo "Another wrapper is acquiring the lock for $host:$remote_port; retry shortly." >&2
  exit 1
fi
trap 'rmdir "$guard" 2>/dev/null || true' EXIT
if ! mkdir "$lock" 2>/dev/null; then
  owner=$(cat "$lock/pid" 2>/dev/null || true)
  if [[ "$owner" =~ ^[0-9]+$ ]] && ! kill -0 "$owner" 2>/dev/null; then
    rm -f "$lock/pid"
    rmdir "$lock" 2>/dev/null || exit 1
    mkdir "$lock" 2>/dev/null || exit 1
  else
    echo "A reconnect wrapper already owns $host:$remote_port (PID ${owner:-unknown})." >&2
    exit 1
  fi
fi
printf '%s\n' "$$" > "$lock/pid"
child=
cleanup() {
  trap '' HUP INT TERM
  if [[ -n "$child" ]]; then
    kill -TERM "$child" 2>/dev/null || true
    wait "$child" 2>/dev/null || true
  fi
  rm -f "$lock/probe" "$lock/pid"
  rmdir "$lock" 2>/dev/null || true
}
rmdir "$guard"
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

# Run probes as children too, so terminal closure also stops a blocked probe.
probe_file="$lock/probe"
while true; do
  ssh -T -o ConnectTimeout=10 \
    -o ServerAliveInterval=15 -o ServerAliveCountMax=3 \
    -o ControlMaster=no -o ControlPath=none -o ClearAllForwardings=yes "$host" \
    "if curl -fsS --connect-timeout 2 --max-time 4 http://127.0.0.1:${remote_port}/health 2>/dev/null | grep -Eq '\"service\"[[:space:]]*:[[:space:]]*\"cc-clip\"'; then
       echo cc-clip-forward-healthy
     elif command -v ss >/dev/null 2>&1; then
       if [ -n \"\$(ss -H -ltn 'sport = :${remote_port}')\" ]; then
         echo cc-clip-forward-held
       else
         echo cc-clip-forward-free
       fi
     else
       echo cc-clip-forward-unknown
     fi" > "$probe_file" <&0 &
  child=$!
  wait "$child"
  probe_rc=$?
  child=
  state=$(cat "$probe_file")
  rm -f "$probe_file"
  forward_option=ClearAllForwardings=no
  if [[ $probe_rc -eq 0 ]]; then
    case "$state" in
      *cc-clip-forward-healthy*)
        echo "Reusing the healthy clipboard tunnel on $host:$remote_port." >&2
        forward_option=ClearAllForwardings=yes
        ;;
      *cc-clip-forward-held*|*cc-clip-forward-unknown*)
        echo "Port $remote_port on $host is occupied or cannot be checked; no healthy cc-clip tunnel answered." >&2
        echo "Run: cc-clip doctor --host $host" >&2
        echo "Identify the stale SSH session before ending it; no sessions were killed." >&2
        exit 1
        ;;
      *cc-clip-forward-free*) ;;
      *) echo "Unexpected tunnel probe response from $host: $state" >&2; exit 1 ;;
    esac
    echo "Connecting to $host; remote screen: $screen_name" >&2
    ssh -t -o ConnectTimeout=10 -o ControlMaster=no -o ControlPath=none \
      -o ServerAliveInterval=30 -o ServerAliveCountMax=3 \
      -o TCPKeepAlive=yes -o ExitOnForwardFailure=yes \
      -o "$forward_option" "$host" "screen -D -RR $screen_name" <&0 &
    child=$!
    wait "$child"
    rc=$?
    child=
    [[ $rc -eq 255 ]] || exit "$rc"
  fi
  echo "Connection failed; retrying in 3 seconds…" >&2
  sleep 3 &
  child=$!
  wait "$child"
  child=
done
