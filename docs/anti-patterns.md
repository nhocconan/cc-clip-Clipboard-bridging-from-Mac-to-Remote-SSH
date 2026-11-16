# Enforced tunnel rules

| Concern | Rule | Audit | Gate |
|---|---|---|---|
| Stale SSH forwards, process ownership, reconnect lifecycle | §1 | `scripts/audit/tunnel-lifecycle.sh` | reusable CI checks and `make tunnel-lifecycle-check` |

1. **SSH forward lifecycle:** run `scripts/audit/tunnel-lifecycle.sh`.
   A client keepalive cannot release the server's listener during a blackhole.
   Every reconnecting forward owner needs a server-side watchdog. Recovery may
   signal only its own SSH session, verified by UID, process name and Linux
   start ticks and boot ID; cross-connection recovery also requires a workstation lease.
   Public HTTP health, a PID alone, a matching process name or absence of a
   local SSH client does not prove ownership. Never kill screen's persistent
   server, unrelated SSH sessions or listeners without a verified lease.
