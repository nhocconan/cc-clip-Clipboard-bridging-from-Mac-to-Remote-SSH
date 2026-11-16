#!/usr/bin/env bash
# §1: verify process ownership and server-side cleanup before releasing tunnels.
set -euo pipefail
cd "$(dirname "$0")/../.."
go test ./scripts ./internal/tunnel ./internal/tunnelmgr -count=1 \
  -run 'Test(SSHWrapper|RemoteGuard|RemoteWatchdog|MasterArgs|BackendStopSignalsOnlyOwnedChild)'
