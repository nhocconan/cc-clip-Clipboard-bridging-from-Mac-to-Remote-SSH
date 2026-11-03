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
 *" -G "*) echo 'remoteforward 18339 [127.0.0.1]:18339'; exit 0 ;;
esac
printf '%s\n' "$*" >> "$EVENTS"
if [[ "$MODE" == auth && " $* " == *" BatchMode=yes "* ]]; then exit 255; fi
case "$*" in
 *cc-clip-forward-healthy*)
   if [[ "$MODE" == retry && ! -f "$EVENTS.failed" ]]; then echo cc-clip-forward-free; else echo "cc-clip-forward-$STATE"; fi
   exit 0 ;;
esac
if [[ "$MODE" == retry && ! -f "$EVENTS.failed" ]]; then touch "$EVENTS.failed"; exit 255; fi
if [[ "$MODE" == hang ]]; then
 echo $$ > "$EVENTS.child"
 exec sleep 60
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(fake), 0755); err != nil {
		t.Fatal(err)
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
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+dir, "XDG_CACHE_HOME="+dir, "EVENTS="+events, "STATE="+state, "MODE="+mode)
	return cmd, events, filepath.Join(dir, "cc-clip", "screen-locks", "test-host-18339")
}

func TestSSHWrapperForwardStates(t *testing.T) {
	for _, state := range []string{"held", "healthy", "free"} {
		t.Run(state, func(t *testing.T) {
			cmd, events, lock := wrapperFixture(t, state, "")
			out, err := cmd.CombinedOutput()
			data, _ := os.ReadFile(events)
			attached := strings.Contains(string(data), "screen -D -RR")
			if state == "held" {
				if err == nil || attached || !strings.Contains(string(out), "cc-clip doctor --host test-host") {
					t.Fatalf("occupied port must stop with guidance, err=%v output=%s events=%s", err, out, data)
				}
			} else {
				if err != nil || !attached {
					t.Fatalf("attachment failed: %v output=%s events=%s", err, out, data)
				}
				for _, line := range strings.Split(string(data), "\n") {
					if strings.Contains(line, "screen -D -RR") && strings.Contains(line, "ClearAllForwardings=yes") != (state == "healthy") {
						t.Fatalf("incorrect forward ownership: %s", line)
					}
				}
			}
			if strings.Contains(string(data), "sudo") || strings.Contains(string(data), "kill -TERM") {
				t.Fatalf("must not kill remote sessions: %s", data)
			}
			if _, err := os.Stat(lock); !os.IsNotExist(err) {
				t.Fatalf("lock leaked: %v", err)
			}
		})
	}
}

func TestSSHWrapperReprobesAfterDisconnect(t *testing.T) {
	cmd, events, _ := wrapperFixture(t, "healthy", "retry")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	data, _ := os.ReadFile(events)
	if strings.Count(string(data), "screen -D -RR") != 2 || strings.Count(string(out), "Reusing the healthy clipboard tunnel") != 1 {
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
	for _, args := range [][]string{nil, {"bad;host"}, {"test-host", "bad;screen"}, {"test-host", "screen", "extra"}} {
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
	if !strings.Contains(string(data), "screen -D -RR") {
		t.Fatalf("did not attach: %s", data)
	}
}
