package tunnel

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRemoteWatchdogReleasesOnlyOwnDeadSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux remote shell")
	}
	for _, tc := range []struct {
		name, comm, uid, health string
		want                    int
	}{
		{"bootstrap-never-forwarded", "sshd-session", "same", "down", 10},
		{"blackhole-after-healthy", "sshd-session", "same", "once", 4},
		{"classic-sshd", "sshd", "same", "once", 4},
		{"wrong-process", "screen", "same", "down", 0},
		{"other-user", "sshd-session", "999999", "down", 0},
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
			fields[19] = "123"
			for name, body := range map[string]string{"comm": tc.comm + "\n", "stat": "4242 (" + tc.comm + ") " + strings.Join(fields, " ") + "\n"} {
				if err := os.WriteFile(filepath.Join(proc, name), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			prelude := `ps() { case "$*" in *ppid*) echo 1 ;; *) if [ "$FAKE_UID" = same ]; then id -u; else echo "$FAKE_UID"; fi ;; esac; }
sleep() { :; }
curl() {
 echo x >> "$HOME/probes"
 if [ "$FAKE_HEALTH" = once ] && [ "$(wc -l < "$HOME/probes" | tr -d ' ')" = 1 ]; then
  echo '{"service":"cc-clip"}'; else return 28; fi
}
kill() { echo "$*" > "$HOME/signals"; }
`
			body := strings.ReplaceAll(RemoteWatchdogCommand(18339), "/proc/", dir+"/proc/")
			body = strings.Replace(body, "p=$PPID", "p=4242", 1)
			cmd := exec.Command("sh", "-c", prelude+body)
			cmd.Env = append(os.Environ(), "HOME="+dir, "FAKE_UID="+tc.uid, "FAKE_HEALTH="+tc.health)
			out, err := cmd.CombinedOutput()
			signals, _ := os.ReadFile(filepath.Join(dir, "signals"))
			probes, _ := os.ReadFile(filepath.Join(dir, "probes"))
			if tc.want == 0 {
				if err == nil || len(signals) > 0 {
					t.Fatalf("unsafe watchdog: %v %s %s", err, out, signals)
				}
			} else if err != nil || strings.TrimSpace(string(signals)) != "-TERM 4242" || strings.Count(string(probes), "x") != tc.want {
				t.Fatalf("watchdog failed: %v %s signals=%s probes=%s", err, out, signals, probes)
			}
		})
	}
}
