package shim

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// agyStubExecutor records the last command and returns canned stdout with no
// error, so RemoteHasAgy's tri-state parsing and probe shape can be asserted
// without a real agy binary.
type agyStubExecutor struct {
	out     string
	lastCmd string
}

func (s *agyStubExecutor) Exec(cmd string) (string, error) {
	s.lastCmd = cmd
	return s.out, nil
}

func TestRemoteHasAgy(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		want    bool
		wantErr bool
	}{
		{"present", "yes", true, false},
		{"absent", "no", false, false},
		{"trailing whitespace present", "yes\n", true, false},
		{"garbage", "maybe", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RemoteHasAgy(&agyStubExecutor{out: tt.out})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for probe output %q", tt.out)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("RemoteHasAgy(%q) = %v, want %v", tt.out, got, tt.want)
			}
		})
	}
}

func TestRemoteHasAgyProbesCommandV(t *testing.T) {
	stub := &agyStubExecutor{out: "no"}
	if _, err := RemoteHasAgy(stub); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stub.lastCmd, "command -v agy") {
		t.Fatalf("RemoteHasAgy must probe via `command -v agy`, got %q", stub.lastCmd)
	}
	// Must NOT copy RemoteHasCodex's directory-existence probe: a present
	// ~/.gemini/antigravity-cli/ directory does not mean the agy CLI is runnable.
	if strings.Contains(stub.lastCmd, "[ -d") || strings.Contains(stub.lastCmd, ".gemini") {
		t.Fatalf("RemoteHasAgy must not use a directory probe, got %q", stub.lastCmd)
	}
}

func TestRemoteHasAgyReturnsExecError(t *testing.T) {
	_, err := RemoteHasAgy(&errorExecutor{err: fmt.Errorf("ssh failed")})
	if err == nil {
		t.Fatal("RemoteHasAgy should surface executor errors")
	}
}

func TestAntigravityPluginJSONValid(t *testing.T) {
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(antigravityPluginJSON()), &m); err != nil {
		t.Fatalf("plugin.json is not valid JSON: %v", err)
	}
	if m["name"] != "cc-clip-notify" {
		t.Fatalf("plugin.json name = %v, want cc-clip-notify", m["name"])
	}
	if s, _ := m["version"].(string); strings.TrimSpace(s) == "" {
		t.Fatal("plugin.json must carry a non-empty version")
	}
	if s, _ := m["description"].(string); strings.TrimSpace(s) == "" {
		t.Fatal("plugin.json must carry a non-empty description")
	}
}

// parseAgyHooks unmarshals the generated ROOT hooks.json (agy 1.4.3+ named-hook
// shape: {hookName: {event: [handlers]}}) and returns the cc-clip hook's events
// so the Stop entry's command/type/timeout can be asserted.
func parseAgyHooks(t *testing.T, port int) map[string][]map[string]interface{} {
	t.Helper()
	var h map[string]map[string][]map[string]interface{}
	if err := json.Unmarshal([]byte(antigravityHooksJSON(port)), &h); err != nil {
		t.Fatalf("root hooks.json must be {hookName: {event: [handlers]}}: %v\n%s", err, antigravityHooksJSON(port))
	}
	if len(h) != 1 {
		t.Fatalf("root hooks.json must define exactly one named hook, got %d", len(h))
	}
	events, ok := h["cc-clip-notify"]
	if !ok {
		t.Fatalf("root hooks.json must name its hook cc-clip-notify, got %v", h)
	}
	return events
}

// The flat {"Stop": [...]} shape at the root is what agy 1.5.16 rejects with
// "cannot unmarshal array into ... JSONHookSpec" and skips entirely; it must
// never come back to the root file.
func TestAntigravityRootHooksJSONIsNotFlatShape(t *testing.T) {
	var flat map[string][]interface{}
	if err := json.Unmarshal([]byte(antigravityHooksJSON(18339)), &flat); err == nil {
		t.Fatalf("root hooks.json must not use the flat event->array shape:\n%s", antigravityHooksJSON(18339))
	}
}

// hooks/hooks.json keeps the flat shape early agy (1.0.x) reads from that path.
func TestAntigravityLegacyHooksJSONFlatShape(t *testing.T) {
	var h map[string][]map[string]interface{}
	if err := json.Unmarshal([]byte(antigravityLegacyHooksJSON(9999)), &h); err != nil {
		t.Fatalf("legacy hooks/hooks.json must be flat {event: [handlers]}: %v", err)
	}
	if len(h) != 1 || len(h["Stop"]) != 1 {
		t.Fatalf("legacy hooks/hooks.json must hold exactly one Stop handler, got %v", h)
	}
	if cmd, _ := h["Stop"][0]["command"].(string); cmd != "env CC_CLIP_PORT=9999 cc-clip plugin run agy-notify" {
		t.Fatalf("legacy Stop command = %q", cmd)
	}
}

func TestAntigravityHooksJSONDefaultPort(t *testing.T) {
	stop := parseAgyHooks(t, 18339)["Stop"]
	if len(stop) != 1 {
		t.Fatalf("expected exactly one Stop hook, got %d", len(stop))
	}
	if stop[0]["type"] != "command" {
		t.Fatalf("Stop hook type = %v, want command", stop[0]["type"])
	}
	cmd, _ := stop[0]["command"].(string)
	if cmd != "cc-clip plugin run agy-notify" {
		t.Fatalf("default-port Stop command = %q, want %q", cmd, "cc-clip plugin run agy-notify")
	}
	if _, ok := stop[0]["timeout"]; !ok {
		t.Fatal("Stop hook must carry a timeout")
	}
}

func TestAntigravityHooksJSONNonDefaultPort(t *testing.T) {
	cmd, _ := parseAgyHooks(t, 9999)["Stop"][0]["command"].(string)
	want := "env CC_CLIP_PORT=9999 cc-clip plugin run agy-notify"
	if cmd != want {
		t.Fatalf("non-default-port Stop command = %q, want %q", cmd, want)
	}
}

func TestAntigravityHooksUsesAgyNotifyAdapter(t *testing.T) {
	cmd, _ := parseAgyHooks(t, 18339)["Stop"][0]["command"].(string)
	if !strings.Contains(cmd, string(AdapterAntigravityNotify)) {
		t.Fatalf("Stop command %q must invoke the %q adapter", cmd, AdapterAntigravityNotify)
	}
	// Guard against the stale design-doc name; the runner dispatch key is agy-notify.
	if strings.Contains(cmd, "antigravity-notify") {
		t.Fatalf("Stop command must use the canonical adapter id %q, not 'antigravity-notify': %q", AdapterAntigravityNotify, cmd)
	}
}

func TestBuildAntigravityPluginScript(t *testing.T) {
	script := buildAntigravityPluginScript(18339)
	mustContain := []string{
		"set -e",
		"mkdir -p ~/.cache/cc-clip",
		"mktemp -d",
		"agy-plugin-src.",
		"cc-clip-notify",
		`mkdir -p "$pdir/hooks"`,
		`cat > "$pdir/hooks.json"`,
		`cat > "$pdir/hooks/hooks.json"`,
		`"cc-clip-notify": {`,
		"plugin.json",
		"agy plugin validate",
		"agy plugin install",
		"rm -rf",
	}
	for _, frag := range mustContain {
		if !strings.Contains(script, frag) {
			t.Fatalf("script missing %q:\n%s", frag, script)
		}
	}
	// validate must precede install so set -e aborts a bad layout before install.
	vi := strings.Index(script, "agy plugin validate")
	ii := strings.Index(script, "agy plugin install")
	if vi < 0 || ii < 0 || vi > ii {
		t.Fatalf("validate must precede install (validate=%d install=%d)", vi, ii)
	}
	// Staging source must NOT be the final plugins dir (avoid self-copy / manifest pollution).
	if strings.Contains(script, ".gemini") || strings.Contains(script, "antigravity-cli/plugins") {
		t.Fatalf("script must stage to a temp dir, never the final plugins dir:\n%s", script)
	}
	// agy plugin install/validate are positional-only; never probe with --help.
	if strings.Contains(script, "plugin install --help") || strings.Contains(script, "plugin validate --help") {
		t.Fatalf("script must not pass `--help` to agy plugin subcommands:\n%s", script)
	}
	// Lock the port plumbing all the way into the heredoc'd hooks.json: a
	// regression that hardcoded the default port inside the script wrapper would
	// pass every other test but silently break non-default-port deploys.
	nonDefault := buildAntigravityPluginScript(9999)
	if !strings.Contains(nonDefault, "env CC_CLIP_PORT=9999 cc-clip plugin run agy-notify") {
		t.Fatalf("non-default port must be plumbed into the hooks.json command in the script:\n%s", nonDefault)
	}
}

func TestEnsureRemoteAntigravityPluginSurfacesExecError(t *testing.T) {
	err := EnsureRemoteAntigravityPlugin(&errorExecutor{err: fmt.Errorf("ssh failed")}, 18339)
	if err == nil {
		t.Fatal("EnsureRemoteAntigravityPlugin should surface executor errors")
	}
	if !strings.Contains(err.Error(), "agy") && !strings.Contains(err.Error(), "cc-clip-notify") {
		t.Fatalf("error should mention the agy plugin install context, got %v", err)
	}
}

// TestEnsureRemoteAntigravityPluginSmoke is an opt-in end-to-end check that the
// generated bundle is accepted by a real agy CLI. Double-gated so normal
// `go test ./...` never mutates the developer's ~/.gemini:
//   - CC_CLIP_AGY_SMOKE=1 must be set, and
//   - the agy binary must be resolvable on PATH.
//
// It runs against an isolated HOME so the install lands in a throwaway dir.
// It asserts agy's validator counts the Stop hook (agy 1.4.3+ silently skips
// an unreadable hooks.json yet still lists the plugin, so "listed" alone proved
// nothing). It is NOT proof the hook fires: that needs a real Stop event, which
// `cc-clip doctor --host` reports as a delivery receipt.
func TestEnsureRemoteAntigravityPluginSmoke(t *testing.T) {
	if os.Getenv("CC_CLIP_AGY_SMOKE") != "1" {
		t.Skip("set CC_CLIP_AGY_SMOKE=1 to run the agy install smoke test")
	}
	if _, err := exec.LookPath("agy"); err != nil {
		t.Skipf("agy not on PATH: %v", err)
	}
	home := t.TempDir()
	run := func(script string) (string, error) {
		c := exec.Command("bash", "-c", script)
		c.Env = append(os.Environ(), "HOME="+home)
		out, err := c.CombinedOutput()
		return string(out), err
	}
	out, err := run(buildAntigravityPluginScript(18339))
	if err != nil {
		t.Fatalf("agy validate+install failed: %v\n%s", err, out)
	}
	if !regexp.MustCompile(`hooks\s+1\b`).MatchString(out) {
		t.Fatalf("agy validate must count exactly one loaded hook:\n%s", out)
	}
	list, err := run("agy plugin list")
	if err != nil {
		t.Fatalf("agy plugin list failed: %v\n%s", err, list)
	}
	if !strings.Contains(list, "cc-clip-notify") {
		t.Fatalf("installed plugin not listed by agy:\n%s", list)
	}
}
