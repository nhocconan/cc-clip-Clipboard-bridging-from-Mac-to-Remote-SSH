package scripts_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func wrapperFixture(t *testing.T, state, mode string) (*exec.Cmd, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX SSH wrapper")
	}
	dir := t.TempDir()
	fake := `#!/usr/bin/env bash
case " $* " in
 *" -G "*) printf '%s\n' "${FAKE_CONFIG:-remoteforward 18339 [127.0.0.1]:18339}"; exit 0 ;;
esac
printf '%s' "$*" | tr '\n' ' ' >> "$EVENTS"
printf '\n' >> "$EVENTS"
if [[ "$MODE" == auth && " $* " == *" BatchMode=yes "* ]]; then exit 255; fi
case "$*" in
 *" sh probe "*)
   if [[ "$MODE" == failed-ss ]]; then bash -c "${!#}"; exit $?; fi
   if [[ "$MODE" == released && ! -f "$EVENTS.released" ]]; then touch "$EVENTS.released"; echo cc-clip-forward-released; exit 0; fi
   if [[ "$MODE" == probe-hang ]]; then echo $$ > "$EVENTS.child"; exec sleep 60; fi
   if [[ "$MODE" == recover && ! -f "$EVENTS.held" ]]; then
     touch "$EVENTS.held"
     echo cc-clip-forward-held
     exit 0
   fi
   if [[ "$MODE" == retry && ! -f "$EVENTS.failed" ]]; then echo cc-clip-forward-free; else echo "cc-clip-forward-$STATE"; fi
   exit 0 ;;
esac
if [[ "$MODE" == retry && ! -f "$EVENTS.failed" ]]; then touch "$EVENTS.failed"; exit 255; fi
if [[ "$MODE" == hang || "$MODE" == stubborn ]]; then
 if [[ "$MODE" == stubborn ]]; then trap '' TERM; fi
 echo $$ > "$EVENTS.child"
 if [[ "$MODE" == stubborn ]]; then while true; do sleep 1; done; else exec sleep 60; fi
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(fake), 0755); err != nil {
		t.Fatal(err)
	}
	if mode == "failed-ss" {
		for _, name := range []string{"curl", "ss"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Make the previous autossh implementation fail promptly instead of retrying forever.
	if err := os.WriteFile(filepath.Join(dir, "autossh"), []byte("#!/bin/sh\nkill -TERM \"$PPID\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("ssh-cc-clip.sh")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, "bash", script, "test-host")
	events := filepath.Join(dir, "events")
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+dir, "XDG_CACHE_HOME="+dir, "EVENTS="+events, "STATE="+state, "MODE="+mode, "CC_CLIP_FORWARD_WAIT_SECONDS=0")
	return cmd, events, filepath.Join(dir, "cc-clip", "screen-locks", "test-host-18339")
}

func TestSSHWrapperForwardStates(t *testing.T) {
	for _, state := range []string{"held", "unknown", "healthy", "free"} {
		t.Run(state, func(t *testing.T) {
			cmd, events, lock := wrapperFixture(t, state, "")
			out, err := cmd.CombinedOutput()
			data, _ := os.ReadFile(events)
			attached := (strings.Contains(string(data), " sh own ") || strings.Contains(string(data), " sh reuse "))
			if state == "held" || state == "unknown" {
				if err == nil || attached || !strings.Contains(string(out), "cc-clip doctor --host test-host") {
					t.Fatalf("occupied port must stop with guidance, err=%v output=%s events=%s", err, out, data)
				}
			} else {
				if err != nil || !attached {
					t.Fatalf("attachment failed: %v output=%s events=%s", err, out, data)
				}
				for _, line := range strings.Split(string(data), "\n") {
					if (strings.Contains(line, " sh own ") || strings.Contains(line, " sh reuse ")) && strings.Contains(line, "ClearAllForwardings=yes") != (state == "healthy") {
						t.Fatalf("incorrect forward ownership: %s", line)
					}
				}
			}
			if strings.Contains(string(data), "sudo -n") {
				t.Fatalf("must not kill remote sessions: %s", data)
			}
			if _, err := os.Stat(lock); !os.IsNotExist(err) {
				t.Fatalf("lock leaked: %v", err)
			}
		})
	}
}

func TestSSHWrapperWaitsForStaleForwardAfterVPNDrop(t *testing.T) {
	for _, state := range []string{"free", "healthy"} {
		t.Run(state, func(t *testing.T) {
			cmd, events, lock := wrapperFixture(t, state, "recover")
			cmd.Env = append(cmd.Env, "CC_CLIP_FORWARD_WAIT_SECONDS=10")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("did not recover after stale forward cleared: %v\n%s", err, out)
			}
			data, _ := os.ReadFile(events)
			if !strings.Contains(string(out), "Waiting for") || (strings.Count(string(data), " sh own ")+strings.Count(string(data), " sh reuse ")) != 1 {
				t.Fatalf("must wait, reprobe, then attach once: %s\n%s", out, data)
			}
			for _, line := range strings.Split(string(data), "\n") {
				if (strings.Contains(line, " sh own ") || strings.Contains(line, " sh reuse ")) && strings.Contains(line, "ClearAllForwardings=yes") != (state == "healthy") {
					t.Fatalf("incorrect forward ownership after recovery: %s", line)
				}
			}
			if _, err := os.Stat(lock); !os.IsNotExist(err) {
				t.Fatalf("lock leaked: %v", err)
			}
		})
	}
}

func TestSSHWrapperBoundsStaleForwardWait(t *testing.T) {
	cmd, events, _ := wrapperFixture(t, "held", "")
	cmd.Env = append(cmd.Env, "CC_CLIP_FORWARD_WAIT_SECONDS=1")
	out, err := cmd.CombinedOutput()
	data, _ := os.ReadFile(events)
	if err == nil || !strings.Contains(string(out), "still occupied") || (strings.Contains(string(data), " sh own ") || strings.Contains(string(data), " sh reuse ")) {
		t.Fatalf("must stop after bounded recovery wait: %v\n%s\n%s", err, out, data)
	}
}

func TestSSHWrapperFailedListenerCheckIsNotFree(t *testing.T) {
	cmd, events, _ := wrapperFixture(t, "free", "failed-ss")
	out, err := cmd.CombinedOutput()
	data, _ := os.ReadFile(events)
	if err == nil || !strings.Contains(string(out), "cannot be checked") || (strings.Contains(string(data), " sh own ") || strings.Contains(string(data), " sh reuse ")) {
		t.Fatalf("failed ss must not attempt a new forward: %v\n%s\n%s", err, out, data)
	}
}

func TestSSHWrapperReprobesAfterDisconnect(t *testing.T) {
	cmd, events, _ := wrapperFixture(t, "healthy", "retry")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	data, _ := os.ReadFile(events)
	if (strings.Count(string(data), " sh own ")+strings.Count(string(data), " sh reuse ")) != 2 || strings.Count(string(out), "Reusing the healthy clipboard tunnel") != 1 {
		t.Fatalf("must reprobe and reuse new holder after failure: %s\n%s", out, data)
	}
}

func TestSSHWrapperHangupStopsChildAndReleasesLock(t *testing.T) {
	cmd, events, lock := wrapperFixture(t, "free", "hang")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	var pid string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(events + ".child")
		pid = strings.TrimSpace(string(b))
		if pid != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == "" {
		t.Fatal("SSH child did not start")
	}
	// A second wrapper must not fight the first screen attachment.
	second := exec.Command("bash", cmd.Args[1:]...)
	second.Env = cmd.Env
	if out, err := second.CombinedOutput(); err == nil || !strings.Contains(string(out), "already owns") {
		t.Fatalf("duplicate wrapper: %v %s", err, out)
	}
	if err := exec.Command("kill", "-HUP", fmt.Sprint(cmd.Process.Pid)).Run(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("wrapper did not stop")
	}
	if err := exec.Command("kill", "-0", pid).Run(); err == nil {
		_ = exec.Command("kill", "-TERM", pid).Run()
		t.Fatal("SSH child survived terminal hangup")
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("lock leaked: %v", err)
	}
}

func TestSSHWrapperRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"bad;host"}, {"-bad"}, {"test-host", "-bad"}, {"test-host", "bad;screen"}, {"test-host", "screen", "extra"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			cmd, events, _ := wrapperFixture(t, "free", "")
			cmd.Args = append(cmd.Args[:2], args...)
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), "Usage:") {
				t.Fatalf("expected usage error: %v %s", err, out)
			}
			if _, err := os.Stat(events); !os.IsNotExist(err) {
				t.Fatal("invalid arguments reached SSH")
			}
		})
	}
}

func TestSSHWrapperAllowsInteractiveAuthentication(t *testing.T) {
	cmd, events, _ := wrapperFixture(t, "free", "auth")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("authentication never reached the shell attachment: %v %s", err, out)
	}
	data, _ := os.ReadFile(events)
	if !(strings.Contains(string(data), " sh own ") || strings.Contains(string(data), " sh reuse ")) {
		t.Fatalf("did not attach: %s", data)
	}
}

func TestSSHWrapperReleasesOwnedStaleForward(t *testing.T) {
	cmd, events, _ := wrapperFixture(t, "free", "released")
	out, err := cmd.CombinedOutput()
	data, _ := os.ReadFile(events)
	if err != nil || !strings.Contains(string(out), "Released this workstation's stale") || strings.Count(string(data), " sh own ") != 1 {
		t.Fatalf("must release, reprobe and attach: %v\n%s\n%s", err, out, data)
	}
}

func TestSSHWrapperRecoversDeadLock(t *testing.T) {
	cmd, _, lock := wrapperFixture(t, "free", "")
	if err := os.MkdirAll(lock, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lock, "pid"), []byte("99999999\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// A stale acquisition directory from the old implementation cannot block us.
	if err := os.MkdirAll(lock+".acquire", 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestSSHWrapperStopsBlockedProbeAndStubbornSSH(t *testing.T) {
	for _, mode := range []string{"probe-hang", "stubborn"} {
		t.Run(mode, func(t *testing.T) {
			cmd, events, _ := wrapperFixture(t, "free", mode)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			deadline := time.Now().Add(4 * time.Second)
			var pid string
			for time.Now().Before(deadline) {
				b, _ := os.ReadFile(events + ".child")
				pid = strings.TrimSpace(string(b))
				if pid != "" {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if pid == "" {
				t.Fatal("child did not start")
			}
			if err := exec.Command("kill", "-TERM", fmt.Sprint(cmd.Process.Pid)).Run(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(4 * time.Second):
				t.Fatal("cleanup hung")
			}
			if exec.Command("kill", "-0", pid).Run() == nil {
				_ = exec.Command("kill", "-KILL", pid).Run()
				t.Fatal("child survived cleanup")
			}
		})
	}
}

func remoteGuard(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("ssh-cc-clip.sh")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(b), "IFS= read -r -d '' remote_script <<'REMOTE' || true\n")
	if !ok {
		t.Fatal("remote guard missing")
	}
	body, _, ok := strings.Cut(rest, "\nREMOTE\n")
	if !ok {
		t.Fatal("remote guard end missing")
	}
	return body
}

// Execute the real remote probe, replacing only OS process inspection and
// signalling. A plausible-looking lease must never authorize another process.
func TestRemoteGuardRecoveryOwnership(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX remote guard")
	}
	for _, tc := range []struct {
		name, comm, ticks, uid, health, owner string
		kill                                  bool
	}{
		{"owned-classic", "sshd", "123", "same", "down", "same", true},
		{"owned-modern", "sshd-session", "123", "same", "down", "same", true},
		{"healthy", "sshd-session", "123", "same", "up", "same", false},
		{"reused-pid", "sshd-session", "999", "same", "down", "same", false},
		{"other-user", "sshd-session", "123", "999999", "down", "same", false},
		{"wrong-command", "screen", "123", "same", "down", "same", false},
		{"other-workstation", "sshd-session", "123", "same", "down", "other", false},
		{"missing-process", "", "", "same", "down", "same", false},
		{"previous-boot", "sshd-session", "123", "same", "down", "same", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			proc := filepath.Join(dir, "proc", "4242")
			if err := os.MkdirAll(proc, 0700); err != nil {
				t.Fatal(err)
			}
			fields := make([]string, 20)
			for i := range fields {
				fields[i] = "0"
			}
			fields[19] = tc.ticks
			for name, body := range map[string]string{"comm": tc.comm + "\n", "stat": "4242 (" + tc.comm + ") " + strings.Join(fields, " ") + "\n"} {
				if err := os.WriteFile(filepath.Join(proc, name), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			bootDir := filepath.Join(dir, "proc", "sys", "kernel", "random")
			if err := os.MkdirAll(bootDir, 0700); err != nil {
				t.Fatal(err)
			}
			bootID := "boot-current\n"
			if tc.name == "previous-boot" {
				bootID = "boot-new\n"
			}
			if err := os.WriteFile(filepath.Join(bootDir, "boot_id"), []byte(bootID), 0600); err != nil {
				t.Fatal(err)
			}
			leaseDir := filepath.Join(dir, ".cache", "cc-clip", "screen-tunnels")
			if err := os.MkdirAll(leaseDir, 0700); err != nil {
				t.Fatal(err)
			}
			owner := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			if tc.owner == "other" {
				owner = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			}
			if err := os.WriteFile(filepath.Join(leaseDir, owner+"-18339"), []byte("4242 123 boot-current\n"), 0600); err != nil {
				t.Fatal(err)
			}
			prelude := `ps() { if [ "$FAKE_UID" = same ]; then id -u; else echo "$FAKE_UID"; fi; }
curl() { if [ "$FAKE_HEALTH" = up ]; then echo '{"service":"cc-clip"}'; else return 28; fi; }
ss() { echo 'LISTEN 127.0.0.1:18339'; }
kill() { echo "signal:$*" >> "$HOME/signals"; }
`
			body := strings.ReplaceAll(remoteGuard(t), "/proc/", dir+"/proc/")
			cmd := exec.Command("sh", "-c", prelude+body, "sh", "probe", "18339", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "test-screen")
			cmd.Env = append(os.Environ(), "HOME="+dir, "FAKE_UID="+tc.uid, "FAKE_HEALTH="+tc.health)
			out, err := cmd.CombinedOutput()
			signals, _ := os.ReadFile(filepath.Join(dir, "signals"))
			if err != nil || (len(signals) > 0) != tc.kill {
				t.Fatalf("ownership violation: %v output=%s signals=%s", err, out, signals)
			}
			if tc.kill && !strings.Contains(string(out), "cc-clip-forward-released") {
				t.Fatalf("recovery not reported: %s", out)
			}
		})
	}
}

func TestSSHWrapperSystemBash(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS Bash 3.2 compatibility")
	}
	cmd, _, _ := wrapperFixture(t, "free", "")
	cmd.Path = "/bin/bash"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("system Bash: %v\n%s", err, out)
	}
}

func TestSSHWrapperImmediateRestartReleasesKernelLock(t *testing.T) {
	cmd, _, _ := wrapperFixture(t, "free", "")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("first run: %v %s", err, out)
	}
	// On Linux a timer's orphaned sleep used to inherit the flock descriptor.
	second := exec.Command(cmd.Path, cmd.Args[1:]...)
	second.Env = cmd.Env
	if out, err := second.CombinedOutput(); err != nil {
		t.Fatalf("immediate restart: %v %s", err, out)
	}
}

func TestSSHWrapperValidatesForwardConfig(t *testing.T) {
	for _, tc := range []struct {
		config string
		valid  bool
	}{
		{"remoteforward [127.0.0.1]:18339 [127.0.0.1]:18339", true},
		{"remoteforward 127.0.0.1:18339 [127.0.0.1]:18339", true},
		{"remoteforward 0 [127.0.0.1]:18339", false},
		{"remoteforward 65536 [127.0.0.1]:18339", false},
		{"remoteforward 0.0.0.0:18339 [127.0.0.1]:18339", false},
		{"hostname test-host", false},
		{"remoteforward 18339 [127.0.0.1]:18339\nremoteforward 18340 [127.0.0.1]:18340", false},
	} {
		t.Run(tc.config, func(t *testing.T) {
			cmd, events, _ := wrapperFixture(t, "free", "")
			cmd.Env = append(cmd.Env, "FAKE_CONFIG="+tc.config)
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.valid {
				t.Fatalf("config acceptance: %v %s", err, out)
			}
			if !tc.valid {
				if _, err := os.Stat(events); !os.IsNotExist(err) {
					t.Fatal("invalid config reached network SSH")
				}
			}
		})
	}
}
