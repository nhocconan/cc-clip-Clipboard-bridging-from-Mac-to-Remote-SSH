# Troubleshooting Guide

## Quick Diagnostics

```bash
cc-clip doctor --host myserver
```

## Step-by-Step Verification

If image paste isn't working, run these checks **in order** to isolate the problem:

```bash
# 1. Local: Is the daemon running?
curl -s http://127.0.0.1:18339/health
# Expected: {"service":"cc-clip","status":"ok","version":"..."}
# ("version" appears from v0.10.0; older daemons omit it)

# 2. Remote: Is the tunnel forwarding?
ssh myserver "curl -s http://127.0.0.1:18339/health"
# Expected: the same body as step 1

# 3. Remote: Is the shim taking priority over real xclip?
ssh myserver "which xclip"
# Expected: /home/<user>/.local/bin/xclip  (NOT /usr/bin/xclip)

# 4. Remote: Does the shim intercept correctly? (copy an image on Mac first)
ssh myserver 'CC_CLIP_DEBUG=1 xclip -selection clipboard -t TARGETS -o'
# Expected: image/png
```

---

## SSH ControlMaster Breaks RemoteForward

**Symptom:** `cc-clip connect` warns that the port is held on the remote but no cc-clip daemon answered, and `curl -s http://127.0.0.1:18339/health` hangs on the remote:

```
      WARNING: port 18339 is held on the remote, but no cc-clip daemon answered.
```

(Before v0.9.2 this same situation was misreported as `tunnel verified`, because the check only completed a TCP handshake.)

**Cause:** If you use SSH `ControlMaster auto` (connection multiplexing), the first SSH connection becomes the "master". All subsequent connections **reuse the master** — even if you later add `RemoteForward` to your config. The old master connection does not have the port forwarding, so the tunnel silently fails.

**Fix:** `cc-clip setup` automatically adds `ControlMaster no` for your host. If you configured SSH manually:

```
# ~/.ssh/config
Host myserver
    HostName 10.x.x.x
    User myuser
    RemoteForward 18339 127.0.0.1:18339
    ControlMaster no
    ControlPath none
```

This ensures every SSH connection creates a fresh tunnel. The trade-off is slightly slower connection setup (no multiplexing), but it guarantees `RemoteForward` works reliably.

---

## Stale sshd Process Blocks RemoteForward

**Symptom:** one of these, on a host where paste used to work:

- `ssh myserver` shows `Warning: remote port forwarding failed for listen port 18339`;
- on the remote, `curl -s http://127.0.0.1:18339/health` connects and then hangs with no output;
- `cc-clip doctor --host myserver` reports `tunnel: [FAIL] port 18339 accepts connections but no cc-clip daemon answered`.

**Cause:** every `ssh myserver` takes the `RemoteForward` from your SSH config, and only the first one gets the port. If that session's client disappears without closing it cleanly (a network drop, a laptop sleep, a tool's background connection that reconnected), its `sshd` on the remote can keep the port while nothing on your machine answers. Later sessions, including new interactive ones, cannot take the port and SSH does not retry.

**Diagnosis:** `cc-clip doctor --host myserver` and `cc-clip connect` list your own `sshd` sessions on the remote with their start times when they see this, and mark the session the check itself ran under. You can also list them yourself; no root is needed:

```bash
# On the remote: your sshd sessions and when they started
ps -o pid,lstart,args -u "$(id -u)" | grep '[s]shd'

# On this machine: the ssh clients still running
ps -o pid,lstart,command -ax | grep '[s]sh'
```

The holder is the remote session with no matching client here. `lsof` and `ss -p` usually cannot show it to a normal user: `sshd` marks itself non-dumpable, so even your own session's sockets are hidden.

**Fix:**

```bash
# End the stale session. ClearAllForwardings keeps this ssh from taking the port itself.
ssh -o ClearAllForwardings=yes myserver 'kill <PID>'

# Then open a new session (reconnect the one you had) and check
ssh myserver
curl -s http://127.0.0.1:18339/health
# Expected: {"service":"cc-clip","status":"ok",...}
```

**Prevention:**

- If you administer the remote, set `ClientAliveInterval 30` and `ClientAliveCountMax 3` in its `sshd_config`, so `sshd` drops a session whose client is gone after about 90 seconds.
- cc-clip's own `connect`, `doctor` and `send` connections use `ClearAllForwardings=yes`, so they never take the port. Other tools that open background SSH connections to the same host alias do take it.
- `cc-clip tunnel run myserver` (experimental, #108) keeps the forward on a private SSH connection that reconnects by itself. It does not take over a port someone else already holds: end the stale holder first (Fix above), and comment out the host's `RemoteForward` in your SSH config while you use it, so other SSH connections do not take the port again.

---

## Reconnecting a Persistent Screen Session

The repository includes `scripts/ssh-cc-clip.sh` for a foreground SSH connection
with a persistent remote `screen` session:

```bash
mkdir -p ~/scripts
cp scripts/ssh-cc-clip.sh ~/scripts/ssh-cc-clip.sh
chmod +x ~/scripts/ssh-cc-clip.sh
~/scripts/ssh-cc-clip.sh myserver                  # screen: claude-myserver
~/scripts/ssh-cc-clip.sh myserver my-screen-name
```

It reads the host's numeric `RemoteForward` listen port from SSH config. When
that port is free, the screen connection owns the forward. If a cc-clip health
check already answers there, it attaches with `ClearAllForwardings=yes` to
reuse the existing tunnel. That existing connection must stay open for paste
to keep working. The health check identifies the service, not which local
installation owns it; use `cc-clip tunnel run` for authenticated identity checks.

SSH transport failures retry after three seconds and probe the port again.
A normal screen detach or exit ends the wrapper. Closing the terminal stops
its SSH child and retry loop; the remote screen remains available for reattach.
A lock prevents two wrappers on this machine from fighting over the same host
and port. Host aliases for the same server have separate locks.

An occupied but unresponsive port stops with `cc-clip doctor` guidance. The
wrapper never kills remote sessions automatically. Follow the stale-session
diagnosis above before ending a holder, especially if another computer also
connects to this server.

Older custom wrappers that print `Releasing a stale reverse forward` can hide
cleanup failures: `sudo -n` requires passwordless sudo, and a parser matching
only `sshd` misses newer OpenSSH's `sshd-session` process name. An inner
`autossh` retry loop also bypasses cleanup between attempts and can survive
its original terminal. Replace those wrappers with the repository version;
rebuilding the cc-clip binary alone does not update a script in `~/scripts`.

---

## Auto-Starting the Daemon with SSH LocalCommand

If you skip the launchd service (`cc-clip service install`) and keep forgetting
to run `cc-clip serve` before SSHing, OpenSSH can start it for you. Add to the
host's block in `~/.ssh/config`:

```ssh-config
Host myserver
    RemoteForward 18339 127.0.0.1:18339
    PermitLocalCommand yes
    LocalCommand pgrep -f 'cc-clip serve' >/dev/null || (cc-clip serve >/dev/null 2>&1 &)
```

`LocalCommand` runs **on your local machine** after each successful connection
to that host; the `pgrep` guard makes it a no-op when the daemon is already
up. Notes:

- `PermitLocalCommand` is required — without it `LocalCommand` is silently
  ignored.
- This starts the daemon *after* the SSH connection is established, so the
  very first clipboard request may race the daemon's startup; a retry pastes
  fine. The launchd service does not have this race and remains the
  recommended path on macOS.
- Scope it to specific `Host` blocks rather than `Host *`: `LocalCommand`
  executes for every match, and a global one runs on every connection you
  make, including ones that have nothing to do with cc-clip.

---

## Token Expired or Invalid

**Symptom:** "fetch type failed" or "token invalid" / "401" in shim debug logs. Image paste silently falls back to the remote (empty) clipboard.

**Cause:** Token TTL (30 days) expired due to prolonged inactivity, or daemon restarted and generated a new token.

**Fix:**

```bash
# Re-sync token without re-uploading the binary
cc-clip connect myserver --token-only
```

The token uses **sliding expiration** — it auto-renews on every successful request. You'll only hit this after 30+ days of zero usage.

To force a new token: `cc-clip serve --rotate-token`.

---

## `cc-clip update` Fails with "API rate limit exceeded"

**Symptom:** `cc-clip update` (or `install.sh`) stops with `failed to query latest release: GitHub API returned 403: {"message":"API rate limit exceeded for <your IP>..."}` or `could not determine latest version`.

**Cause:** Looking up the latest release uses the GitHub API, which allows 60 unauthenticated requests per hour per public IP. Behind a university or company NAT that budget is shared with everyone on the network, so it can be used up without you running `update` at all (#170).

**Fix:** In releases after v0.12.2, `update` and `install.sh` fall back to the `github.com/.../releases/latest` redirect, which is not rate-limited, so this should no longer stop them. On an older cc-clip, or if both lookups fail, skip the lookup or authenticate it:

```bash
# Name the version (see https://github.com/ShunmeiCho/cc-clip/releases); no API call is made
cc-clip update --to vX.Y.Z

# Or send a token: 5000 requests/hour instead of 60
GH_TOKEN=$(gh auth token) cc-clip update

# Fresh install: pin the version for install.sh
curl -fsSL https://raw.githubusercontent.com/ShunmeiCho/cc-clip/main/scripts/install.sh | CC_CLIP_VERSION=vX.Y.Z sh
```

`update` also prints when the API limit resets. Downloading the release archive itself is not affected by this limit.

---

## Launchd Daemon Returns "empty" for Image Clipboard

**Symptom:** `cc-clip service install` is running, but `/clipboard/type` returns `{"type":"empty"}` even when you have an image in your Mac clipboard. Running `cc-clip serve` in the foreground works correctly.

**Cause:** macOS `launchd` does not source your shell profile, so `PATH` doesn't include Homebrew directories (`/opt/homebrew/bin` on Apple Silicon, `/usr/local/bin` on Intel). The daemon can't find `pngpaste`.

**Fix:** Reinstall the service to regenerate the plist with correct PATH:

```bash
cc-clip service uninstall
cc-clip service install
```

---

## Empty Image Data (API Error 400)

**Symptom:** Claude Code returns `API Error: 400 — image cannot be empty`. The conversation becomes corrupted and all subsequent image pastes fail in the same session.

**Cause:** A race condition where the clipboard content changes between the TARGETS check and the image fetch. The shim outputs empty data, and Claude Code sends an empty base64 image to the API.

**Fix:**

1. In Claude Code, run `/clear` or start a new session (the old conversation is corrupted)
2. Update to the latest cc-clip and re-run `cc-clip connect myserver`

---

## `~/.local/bin` Not in PATH

**Symptom:** `cc-clip connect` shows WARNING: `'which xclip' resolves to /usr/bin/xclip, not ~/.local/bin/xclip`.

**Cause:** The shim is installed to `~/.local/bin/` but it's not first in PATH, so the system uses `/usr/bin/xclip` instead.

**Fix:** `cc-clip connect` auto-detects your remote shell and prepends a PATH marker to the appropriate rc file. If auto-fix didn't work, add manually:

```bash
# Add to the TOP of ~/.bashrc (before the interactive guard)
export PATH="$HOME/.local/bin:$PATH"
```

Verify with `which xclip` — it should point to `~/.local/bin/xclip`.

---

## No Image in Clipboard

**Symptom:** Shim returns `image/png` for TARGETS but Claude Code says "No image found in clipboard".

**Cause:** You may not have an image in your Mac clipboard.

**Fix:** Copy an image on your Mac first:
- **Screenshot to clipboard:** `Cmd + Shift + Ctrl + 4` (select area) or `Cmd + Shift + Ctrl + 3` (full screen)
- **Copy from an app:** Right-click an image → Copy Image

## Clipboard-History Manager "Paste" Button Bypasses cc-clip

**Symptom:** You copied an image, but pasting it into remote Claude Code / Codex
via a clipboard-history manager's **"Paste to <terminal>"** button inserts the
image's file *path* as text instead of the image. cc-clip never seems to fire.

**Cause:** This is a usage gotcha, not a cc-clip failure. Clipboard-history
managers (Raycast, Maccy, Paste) "Paste to <app>" buttons inject the file path as
text — they do **not** trigger the agent's own clipboard read. cc-clip only
serves the clipboard when the agent itself reads it, via the xclip shim (Claude
Code) or arboard (Codex). A manager that types a path on your behalf goes around
that read entirely.

**Fix:** Paste with the **agent's own `Ctrl+V`**, after copying the image as
**raw image data** (not a file reference):

- Take a fresh screenshot to the clipboard (`Cmd + Shift + Ctrl + 4` /
  `Cmd + Shift + Ctrl + 3`), or right-click an image → **Copy Image**.
- In the remote Claude Code / Codex session, press `Ctrl+V` directly.

Do **not** use the clipboard manager's paste button for images — it never hands
the raw image to the agent, so cc-clip is never asked for it.

## Setup Fails: "killed" During Re-Deployment

**Symptom:** `cc-clip setup` was working before, but now shows `zsh: killed` when re-running.

**Cause:** The launchd service is running the old binary. Replacing the binary while the daemon holds it open can cause conflicts.

**Fix:**

```bash
cc-clip service uninstall
curl -fsSL https://raw.githubusercontent.com/ShunmeiCho/cc-clip/main/scripts/install.sh | sh
cc-clip setup myserver
```
