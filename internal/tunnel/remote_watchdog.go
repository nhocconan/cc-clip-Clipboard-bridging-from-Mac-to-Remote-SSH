package tunnel

import "fmt"

// RemoteWatchdogCommand releases only the SSH connection running this command
// when its reverse forward stops answering. Client keepalives cannot close a
// server-side listener while a VPN silently drops the client's TCP packets.
// Remote hosts are Linux; missing process identity fails closed.
func RemoteWatchdogCommand(port int) string {
	return fmt.Sprintf(`set -u
uid=$(id -u)
p=$PPID
ticks() {
  [ -r "/proc/$1/stat" ] || return 1
  sed 's/^[^)]*) //' "/proc/$1/stat" | awk '{print $20}'
}
while [ "$p" -gt 1 ] 2>/dev/null; do
  case "$(cat "/proc/$p/comm" 2>/dev/null)" in sshd|sshd-session) break ;; esac
  p=$(ps -o ppid= -p "$p" | tr -d ' ')
done
born=$(ticks "$p") || exit 2
case "$born" in ""|*[!0-9]*) exit 2 ;; esac
owned() {
  [ "$p" -gt 1 ] 2>/dev/null &&
  [ "$(ticks "$p")" = "$born" ] &&
  [ "$(ps -o uid= -p "$p" | tr -d ' ')" = "$uid" ] || return 1
  case "$(cat "/proc/$p/comm" 2>/dev/null)" in sshd|sshd-session) return 0 ;; *) return 1 ;; esac
}
owned || exit 2
failures=0
# Allow the two-stage master bootstrap to add its forward before judging it.
limit=10
while owned; do
  sleep 3
  if curl --noproxy '*' -fsS --connect-timeout 1 --max-time 2 http://127.0.0.1:%d/health 2>/dev/null |
      grep -Eq '"service"[[:space:]]*:[[:space:]]*"cc-clip"'; then
    failures=0
    limit=3
  else failures=$((failures + 1)); fi
  if [ "$failures" -ge "$limit" ]; then
    owned && kill -TERM "$p"
    exit
  fi
done`, port)
}
